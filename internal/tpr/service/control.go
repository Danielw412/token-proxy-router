// Package service keeps the server running: a systemd user unit on Linux, a
// Scheduled Task or HKCU Run entry on Windows, and on every OS the same small Go
// supervisor (`tpr service-run`). The supervisor is what makes restart, update
// and rollback behave the same everywhere.
package service

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
)

const (
	// EnvSupervised tells the server child it runs under the supervisor.
	EnvSupervised = "TPR_SUPERVISED"
	// heartbeatMaxAge is how old the supervisor's state file may get before the
	// supervisor counts as gone, which also protects against reused process IDs.
	heartbeatMaxAge = 20 * time.Second
)

func controlDir() string     { return home.DataDir() }
func statePath() string      { return filepath.Join(controlDir(), "supervisor.json") }
func restartPath() string    { return filepath.Join(controlDir(), "supervisor.restart") }
func stopPath() string       { return filepath.Join(controlDir(), "supervisor.stop") }
func serviceLogPath() string { return filepath.Join(home.LogsDir(), "service.log") }

// LogPath returns the file holding the server's output when it runs as a service.
func LogPath() string { return serviceLogPath() }

// SupervisorState is what the running supervisor publishes.
type SupervisorState struct {
	PID            int    `json:"pid"`
	ChildPID       int    `json:"child_pid"`
	Exe            string `json:"exe"`
	StartedAt      string `json:"started_at"`
	ChildStartedAt string `json:"child_started_at,omitempty"`
	Restarts       int    `json:"restarts"`
}

// ReadSupervisorState returns the state of a live supervisor, or ok=false.
func ReadSupervisorState() (SupervisorState, bool) {
	info, err := os.Stat(statePath())
	if err != nil || time.Since(info.ModTime()) > heartbeatMaxAge {
		return SupervisorState{}, false
	}
	raw, err := os.ReadFile(statePath())
	if err != nil {
		return SupervisorState{}, false
	}
	var state SupervisorState
	if json.Unmarshal(raw, &state) != nil || state.PID <= 0 || !pidAlive(state.PID) {
		return SupervisorState{}, false
	}
	return state, true
}

// Supervised reports whether this process is the supervisor's server child.
func Supervised() bool { return os.Getenv(EnvSupervised) == "1" }

// SupervisorRunning reports whether a live supervisor exists on this data folder.
func SupervisorRunning() bool {
	_, ok := ReadSupervisorState()
	return ok
}

// RequestRestart asks the supervisor to restart the server. Any process can call
// it, including the server itself.
func RequestRestart() error {
	if !SupervisorRunning() {
		return errors.New("the supervisor is not running")
	}
	return setup.AtomicWrite(restartPath(), []byte(time.Now().UTC().Format(time.RFC3339)), 0o600)
}

// requestStop asks the supervisor to stop the server and exit.
func requestStop() error {
	return setup.AtomicWrite(stopPath(), []byte(time.Now().UTC().Format(time.RFC3339)), 0o600)
}

func takeSignalFile(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	_ = os.Remove(path)
	return true
}
