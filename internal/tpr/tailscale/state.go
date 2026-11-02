package tailscale

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
)

// Identity holds the identity sign-in settings. It is off by default.
type Identity struct {
	Enabled       bool     `json:"enabled"`
	AllowedLogins []string `json:"allowed_logins"`
}

func (i Identity) allows(login string) bool {
	for _, allowed := range i.AllowedLogins {
		if strings.EqualFold(strings.TrimSpace(allowed), login) {
			return true
		}
	}
	return false
}

// State is the persisted tailnet configuration.
type State struct {
	Port     int      `json:"port,omitempty"`
	Identity Identity `json:"identity"`
}

var stateMu sync.Mutex

func statePath() string { return filepath.Join(home.DataDir(), "tailscale.json") }

// LoadState reads the persisted state. Anything unreadable means identity
// sign-in stays off.
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

// SaveState writes the persisted state.
func SaveState(state State) error {
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	return setup.AtomicWrite(statePath(), raw, 0o600)
}

// NormalizeLogins validates an allow list and removes duplicates.
func NormalizeLogins(logins []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(logins))
	for _, login := range logins {
		login = strings.TrimSpace(login)
		if login == "" {
			continue
		}
		if strings.EqualFold(login, taggedLogin) {
			return nil, errors.New("tagged devices have no personal identity, so they cannot be allowed")
		}
		// GitHub and other identity providers use `name@github`, which has no
		// domain dot, so only the general address form is enforced.
		if _, err := mail.ParseAddress(login); err != nil {
			return nil, fmt.Errorf("%q does not look like a Tailscale login", login)
		}
		key := strings.ToLower(login)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, login)
	}
	sort.Strings(out)
	return out, nil
}

func init() {
	tpr.AddSnapshot("data/tailscale.json", func(w io.Writer) error {
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
