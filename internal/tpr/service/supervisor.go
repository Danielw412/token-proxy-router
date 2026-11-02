package service

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
)

// PendingCheck is a child start that must prove itself healthy, because a new
// binary was just put in place.
type PendingCheck struct {
	// Timeout is how long the new child gets to answer its health check.
	Timeout time.Duration
	// OnHealthy runs once the child answers.
	OnHealthy func()
	// OnFailed runs after the unhealthy child was stopped. It restores the
	// previous binary, so the next start uses that one.
	OnFailed func(reason string)
}

// Supervisor starts the server child, restarts it after a crash with backoff, and
// reacts to the restart and stop signal files.
type Supervisor struct {
	// Exe is the binary to run. It is fixed at startup because on Windows the
	// running file gets renamed aside during an update.
	Exe string
	// Args are the server child's arguments.
	Args []string
	// Env is the base environment for the child. Nil means the current environment.
	Env []string
	// Dir is the child's working directory.
	Dir string
	// HealthURL returns the URL to poll after an update.
	HealthURL func() string
	// Pending returns a check for the next child start, if one is needed.
	Pending func() *PendingCheck
	// Log receives the child's output and the supervisor's own messages.
	Log io.Writer
	// Backoff is the wait after successive crashes.
	Backoff []time.Duration
	// StableAfter is how long a child must run to reset the crash count.
	StableAfter time.Duration
	// Poll is how often signal files are checked.
	Poll time.Duration
	// StopGrace is how long a child gets to exit before it is killed.
	StopGrace time.Duration

	startedAt string
	restarts  int
	writeMu   sync.Mutex
}

func (s *Supervisor) defaults() {
	if len(s.Backoff) == 0 {
		s.Backoff = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}
	}
	if s.StableAfter == 0 {
		s.StableAfter = time.Minute
	}
	if s.Poll == 0 {
		s.Poll = 500 * time.Millisecond
	}
	if s.StopGrace == 0 {
		s.StopGrace = 15 * time.Second
	}
	if s.Log == nil {
		s.Log = io.Discard
	}
	if s.Pending == nil {
		s.Pending = func() *PendingCheck { return nil }
	}
	if s.startedAt == "" {
		s.startedAt = time.Now().UTC().Format(time.RFC3339)
	}
}

func (s *Supervisor) logf(format string, args ...any) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	fmt.Fprintf(s.Log, "%s [supervisor] %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

func (s *Supervisor) writeState(childPID int, childStarted string) {
	state := SupervisorState{
		PID:            os.Getpid(),
		ChildPID:       childPID,
		Exe:            s.Exe,
		StartedAt:      s.startedAt,
		ChildStartedAt: childStarted,
		Restarts:       s.restarts,
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return
	}
	if err := setup.AtomicWrite(statePath(), raw, 0o600); err != nil {
		s.logf("could not write state: %v", err)
	}
}

type outcome int

const (
	outcomeCrashed outcome = iota
	outcomeRestart
	outcomeStop
)

// Run supervises until ctx ends or a stop is requested.
func (s *Supervisor) Run(ctx context.Context) error {
	s.defaults()
	if state, ok := ReadSupervisorState(); ok && state.PID != os.Getpid() {
		return fmt.Errorf("a supervisor is already running as process %d; use `tpr stop` first", state.PID)
	}
	// A leftover request from before this start must not stop the new supervisor.
	_ = os.Remove(stopPath())
	_ = os.Remove(restartPath())
	defer func() { _ = os.Remove(statePath()) }()
	s.writeState(0, "")

	crashes := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		check := s.Pending()
		started := time.Now()
		result, err := s.runChild(ctx, check)
		if err != nil {
			s.logf("could not start the server: %v", err)
		}
		switch result {
		case outcomeStop:
			s.logf("stopped")
			return nil
		case outcomeRestart:
			crashes = 0
			s.restarts++
			continue
		}
		if ctx.Err() != nil {
			return nil
		}
		s.restarts++
		if time.Since(started) >= s.StableAfter {
			crashes = 0
		}
		wait := s.Backoff[min(crashes, len(s.Backoff)-1)]
		crashes++
		s.logf("server exited; restarting in %s", wait)
		if s.waitBackoff(ctx, wait) {
			s.logf("stopped")
			return nil
		}
	}
}

// waitBackoff sleeps between restarts, still honouring signal files. It reports
// whether a stop was requested. A restart request just ends the wait early.
func (s *Supervisor) waitBackoff(ctx context.Context, wait time.Duration) bool {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	ticker := time.NewTicker(s.Poll)
	defer ticker.Stop()
	s.writeState(0, "")
	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-ticker.C:
			s.touchHeartbeat()
			if takeSignalFile(stopPath()) {
				return true
			}
			if takeSignalFile(restartPath()) {
				return false
			}
		}
	}
}

func (s *Supervisor) touchHeartbeat() {
	now := time.Now()
	_ = os.Chtimes(statePath(), now, now)
}

func (s *Supervisor) runChild(ctx context.Context, check *PendingCheck) (outcome, error) {
	cmd := exec.Command(s.Exe, s.Args...)
	cmd.Dir = s.Dir
	env := s.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = append(append([]string{}, env...), EnvSupervised+"=1")
	cmd.Stdout = lockedWriter{mu: &s.writeMu, w: s.Log}
	cmd.Stderr = lockedWriter{mu: &s.writeMu, w: s.Log}
	configureChild(cmd)
	if err := cmd.Start(); err != nil {
		if check != nil {
			check.OnFailed("could not start: " + err.Error())
		}
		return outcomeCrashed, err
	}
	s.logf("server started as process %d", cmd.Process.Pid)
	s.writeState(cmd.Process.Pid, time.Now().UTC().Format(time.RFC3339))

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	var healthResult chan bool
	var healthCancel context.CancelFunc = func() {}
	if check != nil {
		var healthCtx context.Context
		healthCtx, healthCancel = context.WithTimeout(ctx, check.Timeout)
		healthResult = make(chan bool, 1)
		go func() { healthResult <- s.waitHealthy(healthCtx) }()
	}
	defer healthCancel()

	stop := func() {
		stopChild(cmd)
		select {
		case <-exited:
		case <-time.After(s.StopGrace):
			s.logf("server did not exit in %s; killing it", s.StopGrace)
			_ = cmd.Process.Kill()
			<-exited
		}
	}

	ticker := time.NewTicker(s.Poll)
	defer ticker.Stop()
	lastBeat := time.Now()
	for {
		select {
		case <-ctx.Done():
			stop()
			return outcomeStop, nil
		case err := <-exited:
			if check != nil {
				reason := "the new version exited before becoming healthy"
				if err != nil {
					reason += ": " + err.Error()
				}
				s.logf("%s", reason)
				check.OnFailed(reason)
				return outcomeRestart, nil
			}
			if err != nil {
				s.logf("server exited: %v", err)
			}
			return outcomeCrashed, nil
		case healthy := <-healthResult:
			healthResult = nil
			if healthy {
				s.logf("the new version is healthy")
				check.OnHealthy()
				check = nil
				continue
			}
			reason := fmt.Sprintf("the new version did not become healthy within %s", check.Timeout)
			s.logf("%s", reason)
			stop()
			check.OnFailed(reason)
			return outcomeRestart, nil
		case <-ticker.C:
			if time.Since(lastBeat) >= heartbeatMaxAge/4 {
				s.touchHeartbeat()
				lastBeat = time.Now()
			}
			if takeSignalFile(stopPath()) {
				stop()
				return outcomeStop, nil
			}
			if takeSignalFile(restartPath()) {
				s.logf("restart requested")
				stop()
				return outcomeRestart, nil
			}
		}
	}
}

func (s *Supervisor) waitHealthy(ctx context.Context) bool {
	client := &http.Client{
		Timeout: 3 * time.Second,
		// Loopback only, and the server may use a self-signed certificate.
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // loopback health probe
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
		target := ""
		if s.HealthURL != nil {
			target = s.HealthURL()
		}
		if target == "" {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return false
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return true
		}
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// RotatingFile is an append-only log capped at a size, keeping one old copy.
type RotatingFile struct {
	Path    string
	MaxSize int64

	file *os.File
	size int64
}

// Write appends to the log, rotating first when it is full.
func (r *RotatingFile) Write(p []byte) (int, error) {
	if r.file == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if r.MaxSize > 0 && r.size+int64(len(p)) > r.MaxSize {
		_ = r.file.Close()
		r.file = nil
		_ = os.Rename(r.Path, r.Path+".1")
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *RotatingFile) open() error {
	f, err := os.OpenFile(r.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.file, r.size = f, info.Size()
	return nil
}

// Close closes the log file.
func (r *RotatingFile) Close() error {
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

// HealthURLFor returns the health endpoint of the server configured at path.
func HealthURLFor(configPath string) string {
	info, err := setup.LoadServerInfo(configPath)
	if err != nil || info.Port == 0 {
		return ""
	}
	return info.LocalURL() + "/healthz"
}
