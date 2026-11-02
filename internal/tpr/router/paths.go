package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router/engine"
)

// Providers lists the pools that get an engine, in display order. Every other
// provider is left entirely to upstream selection.
var Providers = []string{engine.ProviderClaude, engine.ProviderCodex}

// routerDir holds one state file and one settings file per pool. It sits in the
// data folder, not config.yaml, so upstream config handling stays untouched.
func routerDir() string { return filepath.Join(home.DataDir(), "router") }

// StatePath returns the file holding a pool's saved state.
func StatePath(provider string) string { return engineStatePath(provider) }

// SettingsPath returns the file holding a pool's saved settings.
func SettingsPath(provider string) string { return settingsPath(provider) }

func engineStatePath(provider string) string {
	return filepath.Join(routerDir(), provider+".state.json")
}

func settingsPath(provider string) string {
	return filepath.Join(routerDir(), provider+".settings.json")
}

func importMarkerPath(provider string) string {
	return filepath.Join(routerDir(), provider+".import.json")
}

// loadSettings reads a pool's settings over its defaults. A missing file gives
// the defaults, and a damaged one gives the defaults plus a warning, so a bad
// file can never take routing offline.
func loadSettings(provider string) (engine.Settings, []string) {
	s := engine.DefaultSettingsFor(provider)
	var warnings []string
	raw, err := os.ReadFile(settingsPath(provider))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		warnings = append(warnings, fmt.Sprintf("could not read saved settings, defaults in use: %v", err))
	default:
		decoded := s
		if errDecode := json.Unmarshal(raw, &decoded); errDecode != nil {
			warnings = append(warnings, fmt.Sprintf("saved settings could not be parsed, defaults in use: %v", errDecode))
		} else {
			s = decoded
		}
	}
	return s.NormalizeFor(provider, warnings)
}

// saveSettings writes a pool's settings atomically with owner-only permissions.
func saveSettings(provider string, s engine.Settings) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(settingsPath(provider), raw)
}

func writeFileAtomic(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	// Windows has no owner-only mode bits, and the data folder's ACL covers it.
	if errChmod := tmp.Chmod(0o600); errChmod != nil && runtime.GOOS != "windows" {
		_ = tmp.Close()
		return errChmod
	}
	if _, errWrite := tmp.Write(raw); errWrite != nil {
		_ = tmp.Close()
		return errWrite
	}
	if errSync := tmp.Sync(); errSync != nil {
		_ = tmp.Close()
		return errSync
	}
	if errClose := tmp.Close(); errClose != nil {
		return errClose
	}
	return os.Rename(name, path)
}
