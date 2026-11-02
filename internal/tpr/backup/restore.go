package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
)

const stagingName = ".restore-pending"

func stagingDir() string { return filepath.Join(home.Dir(), stagingName) }

// PendingRestore reports whether a staged restore is waiting for the next start.
func PendingRestore() bool {
	_, err := os.Stat(filepath.Join(stagingDir(), manifestName))
	return err == nil
}

// Stage validates an archive, snapshots the current state into backups/, and
// stages the archive so the next server start applies it. A database stays open
// and locked while the server runs, so applying only while it is stopped is what
// makes restore behave the same on Windows and Linux.
func Stage(ctx context.Context, r io.Reader, passphrase string, opts Options) (Inspect, string, error) {
	if err := home.Ensure(); err != nil {
		return Inspect{}, "", err
	}
	scratch, err := os.MkdirTemp(home.Dir(), ".restore-scratch-*")
	if err != nil {
		return Inspect{}, "", err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	inspect, err := extract(r, passphrase, scratch)
	if err != nil {
		return Inspect{}, "", err
	}
	snapshot, err := SnapshotCurrent(ctx, passphrase, opts)
	if err != nil {
		return Inspect{}, "", fmt.Errorf("could not save the current state first, so nothing was restored: %w", err)
	}
	if err := os.RemoveAll(stagingDir()); err != nil {
		return Inspect{}, "", err
	}
	if err := os.Rename(scratch, stagingDir()); err != nil {
		return Inspect{}, "", err
	}
	return inspect, snapshot, nil
}

// SnapshotCurrent saves the current state as a normal archive, encrypted with the
// same passphrase so it can be restored the same way.
func SnapshotCurrent(ctx context.Context, passphrase string, opts Options) (string, error) {
	name := fmt.Sprintf("pre-restore-%s.tprbak", time.Now().UTC().Format("20060102-150405"))
	target := filepath.Join(home.BackupsDir(), name)
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	if err := Write(ctx, file, passphrase, opts); err != nil {
		_ = file.Close()
		_ = os.Remove(target)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(target)
		return "", err
	}
	return target, nil
}

// ApplyPending moves a staged restore into place. It runs before the server loads
// its config. configPath receives the archive's config.
func ApplyPending(configPath string) (bool, error) {
	if !PendingRestore() {
		return false, nil
	}
	staging := stagingDir()
	raw, err := os.ReadFile(filepath.Join(staging, manifestName))
	if err != nil {
		return false, err
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return false, err
	}
	stagedConfig := filepath.Join(staging, configEntry)
	authTarget := home.AuthDir()
	hasConfig := false
	if rawConfig, errConfig := os.ReadFile(stagedConfig); errConfig == nil {
		hasConfig = true
		rewritten, errRewrite := rewriteAuthDir(rawConfig, manifest)
		if errRewrite != nil {
			return false, errRewrite
		}
		if parsed, errParse := config.ParseConfigBytes(rewritten); errParse == nil && parsed.AuthDir != "" {
			authTarget = expandHome(parsed.AuthDir)
		}
		if err := setup.AtomicWrite(stagedConfig, rewritten, 0o600); err != nil {
			return false, err
		}
	}
	if err := replaceTree(filepath.Join(staging, "auth"), authTarget); err != nil {
		return false, fmt.Errorf("restore auth files: %w", err)
	}
	// Everything except auth and config goes to the same relative path.
	entries, err := os.ReadDir(staging)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case manifestName, configEntry, "auth":
			continue
		}
		if err := copyTreeOver(filepath.Join(staging, entry.Name()), filepath.Join(home.Dir(), entry.Name())); err != nil {
			return false, fmt.Errorf("restore %s: %w", entry.Name(), err)
		}
	}
	if hasConfig {
		if err := copyFile(stagedConfig, configPath); err != nil {
			return false, fmt.Errorf("restore config: %w", err)
		}
	}
	// Removing the manifest first means an interrupted cleanup cannot re-apply.
	if err := os.Remove(filepath.Join(staging, manifestName)); err != nil {
		return false, err
	}
	_ = os.RemoveAll(staging)
	return true, nil
}

// rewriteAuthDir moves an auth-dir that lived inside the old data folder to this
// machine's data folder, so a backup restores cleanly on another machine or OS.
func rewriteAuthDir(raw []byte, manifest Manifest) ([]byte, error) {
	if manifest.AuthDir == "" || manifest.DataDir == "" {
		return raw, nil
	}
	oldAuth := strings.ReplaceAll(manifest.AuthDir, `\`, "/")
	oldData := strings.ReplaceAll(manifest.DataDir, `\`, "/")
	if !strings.HasPrefix(oldAuth, strings.TrimRight(oldData, "/")+"/") || manifest.AuthDir == home.AuthDir() {
		return raw, nil
	}
	parsed, err := config.ParseConfigBytes(raw)
	if err != nil {
		return nil, err
	}
	if parsed.AuthDir != manifest.AuthDir {
		return raw, nil
	}
	tmp, err := os.CreateTemp(home.Dir(), ".restore-config-*")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	_ = tmp.Close()
	if err := os.WriteFile(tmpName, raw, 0o600); err != nil {
		return nil, err
	}
	if err := config.SaveConfigPreserveCommentsUpdateNestedScalar(tmpName, setup.AuthDirKeyPath(raw), home.AuthDir()); err != nil {
		return nil, err
	}
	return os.ReadFile(tmpName)
}

// replaceTree makes target match source. The old contents were saved by the
// pre-restore snapshot.
func replaceTree(source, target string) error {
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	keep := map[string]bool{}
	err := filepath.WalkDir(source, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		relative, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		keep[relative] = true
		return nil
	})
	if err != nil {
		return err
	}
	err = filepath.WalkDir(target, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(target, current)
		if err != nil {
			return err
		}
		if entry.IsDir() && relative == "logs" {
			return filepath.SkipDir
		}
		if entry.IsDir() || keep[relative] {
			return nil
		}
		return os.Remove(current)
	})
	if err != nil {
		return err
	}
	return copyTreeOver(source, target)
}

func copyTreeOver(source, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(source, target)
	}
	return filepath.WalkDir(source, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		return copyFile(current, filepath.Join(target, relative))
	})
}

func copyFile(source, target string) error {
	raw, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return setup.AtomicWrite(target, raw, 0o600)
}
