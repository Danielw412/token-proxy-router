package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router/engine"
)

// pluginOnlySettings are keys of the old plugin's config block that mean nothing
// here: the plugin host's own knobs and the path of the file being imported.
var pluginOnlySettings = map[string]bool{
	"priority":         true,
	"state_file":       true,
	"deployed_version": true,
}

// ImportPluginState converts the state.json and config block of the old
// custom-quota-router plugin into native state for the codex pool. It needs no
// running server.
//
// Either input may be absent: an empty statePath or a missing file imports
// settings only, and nil settings keeps the settings already saved. It is
// idempotent: importing the same inputs again changes nothing, so a repeated
// migration cannot undo what the router has done since.
func ImportPluginState(statePath string, settings map[string]any) error {
	var state *engine.State
	var rawState []byte
	if statePath != "" {
		raw, err := os.ReadFile(statePath)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return fmt.Errorf("read plugin state: %w", err)
		default:
			parsed, errParse := parsePluginState(raw)
			if errParse != nil {
				return fmt.Errorf("parse plugin state %s: %w", statePath, errParse)
			}
			state, rawState = parsed, raw
		}
	}
	converted, warnings, err := convertPluginSettings(settings)
	if err != nil {
		return err
	}

	fingerprint, err := importFingerprint(rawState, settings)
	if err != nil {
		return err
	}
	if previous, errRead := os.ReadFile(importMarkerPath(engine.ProviderCodex)); errRead == nil {
		var marker importMarker
		if json.Unmarshal(previous, &marker) == nil && marker.Fingerprint == fingerprint {
			return nil
		}
	}

	if converted != nil {
		if errSave := saveSettings(engine.ProviderCodex, *converted); errSave != nil {
			return fmt.Errorf("save imported settings: %w", errSave)
		}
	}
	if state != nil {
		if errSave := engine.SaveState(engineStatePath(engine.ProviderCodex), *state); errSave != nil {
			return fmt.Errorf("save imported state: %w", errSave)
		}
	}
	marker, err := json.MarshalIndent(importMarker{Fingerprint: fingerprint, Warnings: warnings}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(importMarkerPath(engine.ProviderCodex), marker)
}

type importMarker struct {
	Fingerprint string   `json:"fingerprint"`
	Warnings    []string `json:"warnings,omitempty"`
}

// parsePluginState reads only the fields the router knows, so nothing unexpected
// in the old file is carried over. The plugin's state format is the native one.
func parsePluginState(raw []byte) (*engine.State, error) {
	var st engine.State
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	// Restore applies the same repairs the live engine would on startup, and
	// exports the normalized result.
	e := engine.NewForProvider(engine.ProviderCodex, engine.DefaultSettingsFor(engine.ProviderCodex), nil, nil)
	e.Restore(st)
	out, _ := e.Export()
	return &out, nil
}

// convertPluginSettings overlays the plugin's config block on the codex defaults.
// Values the router can use are kept, out-of-range ones are clamped, and unknown
// keys are dropped, matching how the plugin treated its own config.
func convertPluginSettings(settings map[string]any) (*engine.Settings, []string, error) {
	if settings == nil {
		return nil, nil, nil
	}
	known := map[string]any{}
	for key, value := range settings {
		if !pluginOnlySettings[key] {
			known[key] = value
		}
	}
	raw, err := json.Marshal(known)
	if err != nil {
		return nil, nil, fmt.Errorf("encode plugin settings: %w", err)
	}
	s := engine.DefaultSettingsFor(engine.ProviderCodex)
	if errDecode := json.Unmarshal(raw, &s); errDecode != nil {
		return nil, nil, fmt.Errorf("parse plugin settings: %w", errDecode)
	}
	s, warnings := s.NormalizeFor(engine.ProviderCodex, nil)
	return &s, warnings, nil
}

// importFingerprint identifies one set of inputs. Settings keys are sorted by
// encoding/json, so equal maps give equal fingerprints.
func importFingerprint(rawState []byte, settings map[string]any) (string, error) {
	h := sha256.New()
	var compact bytes.Buffer
	if len(rawState) > 0 {
		if err := json.Compact(&compact, rawState); err != nil {
			return "", err
		}
	}
	h.Write(compact.Bytes())
	h.Write([]byte{0})
	if settings != nil {
		keys := make([]string, 0, len(settings))
		for key := range settings {
			if !pluginOnlySettings[key] {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, err := json.Marshal(settings[key])
			if err != nil {
				return "", fmt.Errorf("encode plugin setting %s: %w", key, err)
			}
			h.Write([]byte(key))
			h.Write([]byte{0})
			h.Write(value)
			h.Write([]byte{0})
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
