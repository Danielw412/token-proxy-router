package setup

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
)

const dialProbeTimeout = 700 * time.Millisecond

// State is what the setup wizard remembers.
type State struct {
	Completed   bool   `json:"completed"`
	CompletedAt string `json:"completed_at,omitempty"`
}

var stateMu sync.Mutex

func statePath() string { return filepath.Join(home.DataDir(), "setup.json") }

// LoadState returns the wizard state. A missing or unreadable file means not done.
func LoadState() State {
	stateMu.Lock()
	defer stateMu.Unlock()
	raw, err := os.ReadFile(statePath())
	if err != nil {
		return State{}
	}
	var state State
	if json.Unmarshal(raw, &state) != nil {
		return State{}
	}
	return state
}

// MarkCompleted records that the wizard finished.
func MarkCompleted() (State, error) {
	state := State{Completed: true, CompletedAt: time.Now().UTC().Format(time.RFC3339)}
	raw, err := json.Marshal(state)
	if err != nil {
		return State{}, err
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	if err := AtomicWrite(statePath(), raw, 0o600); err != nil {
		return State{}, err
	}
	return state, nil
}

func init() {
	tpr.AddRoutes(func(group *gin.RouterGroup) {
		group.GET("/setup", handleGetSetup)
		group.POST("/setup/complete", handleCompleteSetup)
	})
	tpr.AddSnapshot("data/setup.json", func(w io.Writer) error {
		raw, err := os.ReadFile(statePath())
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = w.Write(raw)
		return err
	})
}

func handleGetSetup(c *gin.Context) {
	state := LoadState()
	_, errCreds := os.Stat(home.CredentialsPath())
	c.JSON(http.StatusOK, gin.H{
		"completed":        state.Completed,
		"completed_at":     state.CompletedAt,
		"data_dir":         home.Dir(),
		"credentials_file": errCreds == nil,
	})
}

func handleCompleteSetup(c *gin.Context) {
	state, err := MarkCompleted()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save setup state: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"completed": state.Completed, "completed_at": state.CompletedAt})
}
