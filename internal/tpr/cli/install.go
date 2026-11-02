package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/service"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/update"
)

const pathMarker = "# added by tpr install"

func installedBinary() string {
	name := "tpr"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(home.BinDir(), name)
}

func cmdInstall(c *context_, args []string) error {
	flags := newFlags(c, "install")
	noService := flags.Bool("no-service", false, "only copy the binary and add it to PATH (for machines that just use a hub)")
	configPath := configFlag(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := home.Ensure(); err != nil {
		return fmt.Errorf("create data folder %s: %w", home.Dir(), err)
	}
	source, err := update.TargetPath()
	if err != nil {
		return err
	}
	target := installedBinary()
	if source != target {
		if err := update.InstallBinary(source, target); err != nil {
			return fmt.Errorf("copy the binary to %s: %w", target, err)
		}
		fmt.Fprintf(c.out, "Installed %s\n", target)
	}
	path := pathEditor{run: c.run(), goos: runtime.GOOS, shell: os.Getenv("SHELL"), pathEnv: os.Getenv("PATH")}
	if path.userHome, err = os.UserHomeDir(); err != nil {
		return fmt.Errorf("find the home folder: %w", err)
	}
	message, err := path.add(home.BinDir())
	if err != nil {
		fmt.Fprintf(c.out, "Could not add tpr to PATH (%v). Add %s yourself.\n", err, home.BinDir())
	} else if message != "" {
		fmt.Fprintln(c.out, message)
	}
	if *noService {
		fmt.Fprintln(c.out, "Client setup done. Next: tpr connect --hub <hub-url> --key <client key>")
		return nil
	}
	first, err := setup.Bootstrap(*configPath)
	if err != nil {
		return err
	}
	if first != nil {
		first.Print(c.out)
	}
	kind, err := c.manager().Install(target)
	if err != nil {
		return err
	}
	info, err := loadInfo(*configPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "Service: %s\n", describeKind(kind))
	fmt.Fprintf(c.out, "Panel: %s\n", info.PanelURL())
	return nil
}

func describeKind(kind service.Kind) string {
	switch kind {
	case service.KindSystemd:
		return "systemd user service, starts at boot"
	case service.KindWindowsTask:
		return "Scheduled Task, starts when you log in"
	case service.KindWindowsRun:
		return "registry logon entry, starts when you log in"
	default:
		return "running in the background, but it will not start again after a reboot"
	}
}

func cmdUninstall(c *context_, args []string) error {
	flags := newFlags(c, "uninstall")
	purge := flags.Bool("purge", false, "also delete the data folder, including accounts and keys")
	yes := flags.Bool("yes", false, "do not ask before deleting the data folder")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := c.manager().Uninstall(); err != nil {
		return fmt.Errorf("remove the service: %w", err)
	}
	fmt.Fprintln(c.out, "Service removed.")
	path := pathEditor{run: c.run(), goos: runtime.GOOS, shell: os.Getenv("SHELL"), pathEnv: os.Getenv("PATH")}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find the home folder: %w", err)
	}
	path.userHome = userHome
	if err := path.remove(home.BinDir()); err != nil {
		fmt.Fprintf(c.out, "Could not remove tpr from PATH (%v). Remove %s yourself.\n", err, home.BinDir())
	}
	if !*purge {
		fmt.Fprintf(c.out, "Your data stays in %s. To delete it too: tpr uninstall --purge\n", home.Dir())
		return nil
	}
	return purgeData(c, userHome, *yes)
}

func purgeData(c *context_, userHome string, yes bool) error {
	dir := home.Dir()
	if err := checkPurgeTarget(dir, userHome); err != nil {
		return err
	}
	if !yes {
		fmt.Fprintf(c.out, "This deletes %s, including your accounts and keys. Type yes to continue: ", dir)
		answer, _ := bufio.NewReader(c.in).ReadString('\n')
		if strings.TrimSpace(answer) != "yes" {
			return errors.New("not deleted")
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		// A running tpr.exe cannot delete itself on Windows.
		return fmt.Errorf("could not delete everything in %s (%w). Close tpr, then delete the folder by hand", dir, err)
	}
	fmt.Fprintf(c.out, "Deleted %s\n", dir)
	return nil
}

// checkPurgeTarget refuses to delete anything that does not look like a data
// folder, since TPR_HOME can point anywhere.
func checkPurgeTarget(dir, userHome string) error {
	clean := filepath.Clean(dir)
	if clean == filepath.Clean(userHome) || clean == filepath.Dir(clean) {
		return fmt.Errorf("refusing to delete %s: it is not a data folder", dir)
	}
	for _, marker := range []string{"config.yaml", "bin", "auth", "data"} {
		if _, err := os.Stat(filepath.Join(clean, marker)); err == nil {
			return nil
		}
	}
	return fmt.Errorf("refusing to delete %s: it does not look like a data folder", dir)
}

// pathEditor adds the bin folder to the user's PATH and takes it out again.
type pathEditor struct {
	run      setup.Runner
	goos     string
	shell    string
	pathEnv  string
	userHome string
}

func (p pathEditor) onPath(dir string) bool {
	separator := string(os.PathListSeparator)
	if p.goos == "windows" {
		separator = ";"
	}
	for _, entry := range strings.Split(p.pathEnv, separator) {
		if strings.EqualFold(strings.TrimRight(entry, `/\`), strings.TrimRight(dir, `/\`)) {
			return true
		}
	}
	return false
}

func (p pathEditor) add(dir string) (string, error) {
	if p.onPath(dir) {
		return "", nil
	}
	if p.goos == "windows" {
		return p.addWindows(dir)
	}
	// A folder that is already on PATH needs no profile edit.
	localBin := filepath.Join(p.userHome, ".local", "bin")
	if p.onPath(localBin) {
		link := filepath.Join(localBin, "tpr")
		if err := os.MkdirAll(localBin, 0o755); err != nil {
			return "", err
		}
		if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err := os.Symlink(filepath.Join(dir, "tpr"), link); err != nil {
			return "", err
		}
		return "Linked " + link, nil
	}
	profile := p.profilePath()
	raw, err := os.ReadFile(profile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if strings.Contains(string(raw), pathMarker) {
		return "", nil
	}
	updated := append([]byte{}, raw...)
	if len(updated) > 0 && updated[len(updated)-1] != '\n' {
		updated = append(updated, '\n')
	}
	updated = append(updated, "export PATH=\""+p.shellPath(dir)+":$PATH\" "+pathMarker+"\n"...)
	if err := setup.AtomicWrite(profile, updated, 0o644); err != nil {
		return "", err
	}
	return "Added " + dir + " to PATH in " + profile + ". Open a new terminal to use `tpr`.", nil
}

func (p pathEditor) shellPath(dir string) string {
	if relative, err := filepath.Rel(p.userHome, dir); err == nil && !strings.HasPrefix(relative, "..") {
		return "$HOME/" + filepath.ToSlash(relative)
	}
	return dir
}

func (p pathEditor) profilePath() string {
	rc := ".profile"
	switch filepath.Base(p.shell) {
	case "zsh":
		rc = ".zshrc"
	case "bash":
		rc = ".bashrc"
	}
	return filepath.Join(p.userHome, rc)
}

// addWindows edits the user PATH through .NET, which also tells running programs
// about the change. setx would silently cut a long PATH off at 1024 characters.
func (p pathEditor) addWindows(dir string) (string, error) {
	script := fmt.Sprintf(`$d='%s'; $p=[Environment]::GetEnvironmentVariable('Path','User'); if (-not (($p -split ';') -contains $d)) { [Environment]::SetEnvironmentVariable('Path', (($p.TrimEnd(';') + ';' + $d).TrimStart(';')), 'User') }`, strings.ReplaceAll(dir, "'", "''"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, stderr, err := p.run(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script); err != nil {
		return "", fmt.Errorf("%s", firstLine(stderr, err))
	}
	return "Added " + dir + " to your user PATH. Open a new terminal to use `tpr`.", nil
}

func (p pathEditor) remove(dir string) error {
	if p.goos == "windows" {
		script := fmt.Sprintf(`$d='%s'; $p=[Environment]::GetEnvironmentVariable('Path','User'); if ($p) { [Environment]::SetEnvironmentVariable('Path', (($p -split ';' | Where-Object { $_ -and $_ -ne $d }) -join ';'), 'User') }`, strings.ReplaceAll(dir, "'", "''"))
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, stderr, err := p.run(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script); err != nil {
			return fmt.Errorf("%s", firstLine(stderr, err))
		}
		return nil
	}
	link := filepath.Join(p.userHome, ".local", "bin", "tpr")
	if target, err := os.Readlink(link); err == nil && filepath.Dir(target) == dir {
		if err := os.Remove(link); err != nil {
			return err
		}
	}
	for _, rc := range []string{".bashrc", ".zshrc", ".profile"} {
		profile := filepath.Join(p.userHome, rc)
		raw, err := os.ReadFile(profile)
		if err != nil || !strings.Contains(string(raw), pathMarker) {
			continue
		}
		var kept strings.Builder
		for _, line := range strings.SplitAfter(string(raw), "\n") {
			if !strings.Contains(line, pathMarker) {
				kept.WriteString(line)
			}
		}
		if err := setup.AtomicWrite(profile, []byte(kept.String()), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func firstLine(stderr []byte, err error) string {
	for _, line := range strings.Split(string(stderr), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	if err != nil {
		return err.Error()
	}
	return "unknown error"
}
