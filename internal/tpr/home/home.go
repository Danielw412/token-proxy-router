// Package home resolves Token Proxy Router's single data folder.
//
// Everything the router owns lives under one directory, on every OS:
//
//	~/.token-proxy-router/        (override with TPR_HOME)
//	  config.yaml                 server configuration
//	  credentials.txt             first-run keys, owner-only (deleted by `tpr creds --forget`)
//	  auth/                       OAuth credential files
//	  data/                       router state and quota history
//	  logs/                       server logs
//	  backups/                    automatic and manual backups
//	  bin/                        installed binary and its rollback copy
//	  static/                     reserved for an override panel (normally unused)
package home

import (
	"os"
	"path/filepath"
)

// EnvHome overrides the data folder. Tests and side-by-side installs use it.
const EnvHome = "TPR_HOME"

// DirName is the folder name created inside the user's home directory.
const DirName = ".token-proxy-router"

// Dir returns the absolute data folder path. It never creates the folder.
func Dir() string {
	if override := os.Getenv(EnvHome); override != "" {
		if abs, err := filepath.Abs(override); err == nil {
			return abs
		}
		return override
	}
	userHome, err := os.UserHomeDir()
	if err != nil || userHome == "" {
		// A service account with no home directory still gets a usable path.
		return filepath.Join(os.TempDir(), DirName)
	}
	return filepath.Join(userHome, DirName)
}

// ConfigPath returns the path of config.yaml.
func ConfigPath() string { return filepath.Join(Dir(), "config.yaml") }

// CredentialsPath returns the owner-only file holding first-run keys.
func CredentialsPath() string { return filepath.Join(Dir(), "credentials.txt") }

// AuthDir returns the OAuth credential folder.
func AuthDir() string { return filepath.Join(Dir(), "auth") }

// DataDir returns the folder for router state and quota history.
func DataDir() string { return filepath.Join(Dir(), "data") }

// LogsDir returns the server log folder.
func LogsDir() string { return filepath.Join(Dir(), "logs") }

// BackupsDir returns the backup folder.
func BackupsDir() string { return filepath.Join(Dir(), "backups") }

// BinDir returns the folder holding the installed binary and its rollback copy.
func BinDir() string { return filepath.Join(Dir(), "bin") }

// Ensure creates the data folder tree with owner-only permissions.
func Ensure() error {
	for _, dir := range []string{Dir(), AuthDir(), DataDir(), LogsDir(), BackupsDir(), BinDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}
