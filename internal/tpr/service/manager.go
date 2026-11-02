package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
)

// Kind names how the service is installed.
type Kind string

// Service kinds, matching the /system response.
const (
	KindSystemd     Kind = "systemd"
	KindWindowsTask Kind = "windows-task"
	KindWindowsRun  Kind = "windows-run"
	KindNone        Kind = "none"
)

const (
	unitName    = "token-proxy-router.service"
	taskName    = "TokenProxyRouter"
	runValue    = "TokenProxyRouter"
	runKey      = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	cmdTimeout  = 30 * time.Second
	stopTimeout = 30 * time.Second
)

// Status describes the service as `tpr status` and /system report it.
type Status struct {
	Kind       Kind   `json:"kind"`
	Installed  bool   `json:"installed"`
	Running    bool   `json:"running"`
	Supervised bool   `json:"supervised"`
	PID        int    `json:"-"`
	ChildPID   int    `json:"-"`
	Detail     string `json:"-"`
}

// Manager installs and controls the service. Every external command goes through
// Run, so tests never touch the real systemctl, schtasks or reg.
type Manager struct {
	Run setup.Runner
	// GOOS is the target OS. Empty means the current one.
	GOOS string
	// UserHome is the user's home folder. Empty means the current one.
	UserHome string
	// Username is used for loginctl guidance. Empty means the current user.
	Username string
	// Out receives guidance for the user.
	Out io.Writer
	// StartDetached starts `service-run` in the background. Nil means the real one.
	StartDetached func(exe string) error
	// StopWait bounds how long Stop waits for the supervisor. Zero means the default.
	StopWait time.Duration
}

// NewManager returns a manager for this machine.
func NewManager(out io.Writer) *Manager {
	return &Manager{Run: setup.ExecRunner, Out: out}
}

func (m *Manager) goos() string {
	if m.GOOS != "" {
		return m.GOOS
	}
	return runtime.GOOS
}

func (m *Manager) userHome() string {
	if m.UserHome != "" {
		return m.UserHome
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return dir
}

func (m *Manager) username() string {
	if m.Username != "" {
		return m.Username
	}
	if current, err := user.Current(); err == nil {
		return current.Username
	}
	return "$USER"
}

func (m *Manager) out() io.Writer {
	if m.Out != nil {
		return m.Out
	}
	return io.Discard
}

func (m *Manager) run(name string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	stdout, stderr, err := m.Run(ctx, name, args...)
	return string(stdout), string(stderr), err
}

func (m *Manager) systemctl(args ...string) (string, string, error) {
	return m.run("systemctl", append([]string{"--user"}, args...)...)
}

// UnitPath returns where the systemd user unit lives.
func (m *Manager) UnitPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" || m.UserHome != "" {
		base = filepath.Join(m.userHome(), ".config")
	}
	return filepath.Join(base, "systemd", "user", unitName)
}

func kindPath() string { return filepath.Join(controlDir(), "service.json") }

func saveKind(kind Kind) {
	raw, err := json.Marshal(map[string]string{"kind": string(kind)})
	if err == nil {
		_ = setup.AtomicWrite(kindPath(), raw, 0o600)
	}
}

func loadKind() Kind {
	raw, err := os.ReadFile(kindPath())
	if err != nil {
		return KindNone
	}
	var parsed struct {
		Kind Kind `json:"kind"`
	}
	if json.Unmarshal(raw, &parsed) != nil || parsed.Kind == "" {
		return KindNone
	}
	return parsed.Kind
}

// systemdQuote quotes a path for ExecStart and Environment lines.
func systemdQuote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, `%`, `%%`)
	value = strings.ReplaceAll(value, `$`, `$$`)
	return `"` + value + `"`
}

// RenderUnit returns the systemd user unit for a binary.
func RenderUnit(exe, dataDir string, setHome bool) string {
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=Token Proxy Router\nAfter=network-online.target\nWants=network-online.target\n\n")
	b.WriteString("[Service]\nType=simple\n")
	fmt.Fprintf(&b, "ExecStart=%s service-run\n", systemdQuote(exe))
	fmt.Fprintf(&b, "WorkingDirectory=%s\n", systemdQuote(dataDir))
	if setHome {
		fmt.Fprintf(&b, "Environment=%s\n", systemdQuote(home.EnvHome+"="+dataDir))
	}
	b.WriteString("Restart=always\nRestartSec=3\nTimeoutStopSec=30\n\n")
	b.WriteString("[Install]\nWantedBy=default.target\n")
	return b.String()
}

// Install registers the service for exe and starts it. It reports honestly which
// kind it managed to set up.
func (m *Manager) Install(exe string) (Kind, error) {
	switch m.goos() {
	case "linux":
		return m.installSystemd(exe)
	case "windows":
		return m.installWindows(exe)
	default:
		fmt.Fprintln(m.out(), "Automatic start is not set up on this OS. Start the server with `tpr start`.")
		saveKind(KindNone)
		return KindNone, m.startDetached(exe)
	}
}

func (m *Manager) installSystemd(exe string) (Kind, error) {
	if _, stderr, err := m.systemctl("show-environment"); err != nil {
		fmt.Fprintf(m.out(), "systemd user services are not available here (%s).\n", firstLine(stderr, err))
		fmt.Fprintln(m.out(), "Starting the server in the background instead. It will not start again after a reboot.")
		fmt.Fprintln(m.out(), "If this is an SSH session, `sudo loginctl enable-linger $USER` and logging in again usually fixes it; then run `tpr install` again.")
		saveKind(KindNone)
		return KindNone, m.startDetached(exe)
	}
	_, homeOverride := os.LookupEnv(home.EnvHome)
	unit := RenderUnit(exe, home.Dir(), homeOverride)
	if err := setup.AtomicWrite(m.UnitPath(), []byte(unit), 0o644); err != nil {
		return KindNone, fmt.Errorf("write %s: %w", m.UnitPath(), err)
	}
	if _, stderr, err := m.systemctl("daemon-reload"); err != nil {
		return KindNone, fmt.Errorf("systemctl daemon-reload failed: %s", firstLine(stderr, err))
	}
	if _, stderr, err := m.systemctl("enable", "--now", unitName); err != nil {
		return KindNone, fmt.Errorf("could not start the service: %s. See `tpr logs`", firstLine(stderr, err))
	}
	saveKind(KindSystemd)
	m.ensureLinger()
	return KindSystemd, nil
}

// ensureLinger keeps the user's services running with nobody logged in.
func (m *Manager) ensureLinger() {
	name := m.username()
	stdout, _, err := m.run("loginctl", "show-user", name, "--property=Linger")
	if err == nil && strings.Contains(stdout, "Linger=yes") {
		return
	}
	if _, stderr, errLinger := m.run("loginctl", "enable-linger", name); errLinger != nil {
		fmt.Fprintf(m.out(), "The service will stop when you log out (%s).\n", firstLine(stderr, errLinger))
		fmt.Fprintf(m.out(), "To keep it running, run: sudo loginctl enable-linger %s\n", name)
	}
}

func autostartCommand(exe string) string {
	cmd := `"` + exe + `" service-run --detach`
	if override, ok := os.LookupEnv(home.EnvHome); ok && override != "" {
		cmd += ` --home "` + home.Dir() + `"`
	}
	return cmd
}

func (m *Manager) installWindows(exe string) (Kind, error) {
	command := autostartCommand(exe)
	_, stderrTask, errTask := m.run("schtasks", "/Create", "/TN", taskName, "/SC", "ONLOGON", "/RL", "LIMITED", "/TR", command, "/F")
	if errTask == nil {
		saveKind(KindWindowsTask)
		if _, _, errRun := m.run("schtasks", "/Run", "/TN", taskName); errRun != nil {
			return KindWindowsTask, m.startDetached(exe)
		}
		return KindWindowsTask, nil
	}
	// Creating a logon task usually needs admin. A Run entry does not.
	fmt.Fprintf(m.out(), "Could not create a Scheduled Task (%s), so using a logon entry in the registry instead.\n", firstLine(stderrTask, errTask))
	if _, stderrRun, errRun := m.run("reg", "add", runKey, "/v", runValue, "/t", "REG_SZ", "/d", command, "/f"); errRun != nil {
		return KindNone, fmt.Errorf("could not register autostart: %s", firstLine(stderrRun, errRun))
	}
	saveKind(KindWindowsRun)
	return KindWindowsRun, m.startDetached(exe)
}

func (m *Manager) startDetached(exe string) error {
	if m.StartDetached != nil {
		return m.StartDetached(exe)
	}
	return StartDetached(exe)
}

// StartDetached starts the supervisor in the background.
func StartDetached(exe string) error {
	cmd := exec.Command(exe, "service-run")
	cmd.Dir = home.Dir()
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the supervisor: %w", err)
	}
	return cmd.Process.Release()
}

// Uninstall removes the service registration. Data is never touched here.
func (m *Manager) Uninstall() error {
	var errs []error
	if err := m.Stop(); err != nil && !errors.Is(err, ErrNotRunning) {
		errs = append(errs, err)
	}
	switch m.goos() {
	case "linux":
		if _, err := os.Stat(m.UnitPath()); err == nil {
			if _, stderr, errDisable := m.systemctl("disable", unitName); errDisable != nil {
				errs = append(errs, fmt.Errorf("systemctl disable failed: %s", firstLine(stderr, errDisable)))
			}
			if errRemove := os.Remove(m.UnitPath()); errRemove != nil {
				errs = append(errs, errRemove)
			}
			_, _, _ = m.systemctl("daemon-reload")
		}
	case "windows":
		if _, _, err := m.run("schtasks", "/Query", "/TN", taskName); err == nil {
			if _, stderr, errDelete := m.run("schtasks", "/Delete", "/TN", taskName, "/F"); errDelete != nil {
				errs = append(errs, fmt.Errorf("could not remove the Scheduled Task: %s", firstLine(stderr, errDelete)))
			}
		}
		if _, _, err := m.run("reg", "query", runKey, "/v", runValue); err == nil {
			if _, stderr, errDelete := m.run("reg", "delete", runKey, "/v", runValue, "/f"); errDelete != nil {
				errs = append(errs, fmt.Errorf("could not remove the logon entry: %s", firstLine(stderr, errDelete)))
			}
		}
	}
	_ = os.Remove(kindPath())
	return errors.Join(errs...)
}

// ErrNotRunning means there was nothing to stop.
var ErrNotRunning = errors.New("the service is not running")

// Start starts the service, or the supervisor directly when none is installed.
func (m *Manager) Start(exe string) error {
	status := m.Status()
	if status.Running {
		return nil
	}
	switch status.Kind {
	case KindSystemd:
		if _, stderr, err := m.systemctl("start", unitName); err != nil {
			return fmt.Errorf("could not start the service: %s", firstLine(stderr, err))
		}
		return nil
	case KindWindowsTask:
		if _, _, err := m.run("schtasks", "/Run", "/TN", taskName); err == nil {
			return nil
		}
	}
	return m.startDetached(exe)
}

// Stop stops the service.
func (m *Manager) Stop() error {
	status := m.Status()
	if status.Kind == KindSystemd {
		if _, stderr, err := m.systemctl("stop", unitName); err != nil {
			return fmt.Errorf("could not stop the service: %s", firstLine(stderr, err))
		}
		return nil
	}
	if !SupervisorRunning() {
		return ErrNotRunning
	}
	if err := requestStop(); err != nil {
		return fmt.Errorf("could not ask the supervisor to stop: %w", err)
	}
	wait := m.StopWait
	if wait == 0 {
		wait = stopTimeout
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if !SupervisorRunning() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("the supervisor did not stop in time. See `tpr logs`")
}

// Restart restarts the service, starting it when it was not running.
func (m *Manager) Restart(exe string) error {
	status := m.Status()
	if status.Kind == KindSystemd {
		if _, stderr, err := m.systemctl("restart", unitName); err != nil {
			return fmt.Errorf("could not restart the service: %s", firstLine(stderr, err))
		}
		return nil
	}
	if SupervisorRunning() {
		return RequestRestart()
	}
	return m.Start(exe)
}

// Status reports the current state.
func (m *Manager) Status() Status {
	status := Status{Kind: loadKind()}
	switch m.goos() {
	case "linux":
		if _, err := os.Stat(m.UnitPath()); err == nil {
			status.Kind = KindSystemd
			status.Installed = true
			stdout, _, _ := m.systemctl("is-active", unitName)
			status.Running = strings.TrimSpace(stdout) == "active"
		} else if status.Kind == KindSystemd {
			status.Kind = KindNone
		}
	case "windows":
		if _, _, err := m.run("schtasks", "/Query", "/TN", taskName); err == nil {
			status.Kind = KindWindowsTask
			status.Installed = true
		} else if _, _, err := m.run("reg", "query", runKey, "/v", runValue); err == nil {
			status.Kind = KindWindowsRun
			status.Installed = true
		} else {
			status.Kind = KindNone
		}
	}
	if state, ok := ReadSupervisorState(); ok {
		status.Running = true
		status.PID = state.PID
		status.ChildPID = state.ChildPID
	}
	status.Supervised = Supervised() || status.PID != 0
	return status
}

// Tail writes the last lines of the log, and keeps following it when follow is set.
func Tail(ctx context.Context, path string, lines int, follow bool, out io.Writer) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if err := writeLastLines(file, lines, out); err != nil {
		return err
	}
	if !follow {
		return nil
	}
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		info, errStat := os.Stat(path)
		if errStat != nil {
			continue
		}
		if info.Size() < offset || !sameFile(file, info) {
			// The log rotated: reopen and start from the top of the new file.
			_ = file.Close()
			file, err = os.Open(path)
			if err != nil {
				return err
			}
			offset = 0
		}
		n, errCopy := io.Copy(out, file)
		if errCopy != nil {
			return errCopy
		}
		offset += n
	}
}

func sameFile(file *os.File, info os.FileInfo) bool {
	current, err := file.Stat()
	return err == nil && os.SameFile(current, info)
}

func writeLastLines(file *os.File, lines int, out io.Writer) error {
	const maxScan = 8 << 20
	info, err := file.Stat()
	if err != nil {
		return err
	}
	start := int64(0)
	if info.Size() > maxScan {
		start = info.Size() - maxScan
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return err
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxScan)
	ring := make([]string, 0, lines)
	for scanner.Scan() {
		if len(ring) == lines {
			ring = ring[1:]
		}
		ring = append(ring, scanner.Text())
	}
	for _, line := range ring {
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func firstLine(stderr string, err error) string {
	for _, line := range strings.Split(stderr, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	if err != nil {
		return err.Error()
	}
	return "unknown error"
}
