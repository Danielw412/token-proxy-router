package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
)

const (
	manifestName = "manifest.json"
	configEntry  = "config.yaml"
	authPrefix   = "auth/"
	// maxExtracted caps a restore, so a crafted archive cannot fill the disk.
	maxExtracted = 4 << 30
	// maxEntries caps the number of files in an archive.
	maxEntries = 200000
	// formatVersion is bumped when the archive layout changes incompatibly.
	formatVersion = 1
)

// ephemeralData are data/ files describing this process and this moment. Restoring
// one would make the next start act on a stale update or a dead supervisor, so they
// are neither written nor accepted.
var ephemeralData = map[string]bool{
	"data/supervisor.json":     true,
	"data/supervisor.restart":  true,
	"data/supervisor.stop":     true,
	"data/service.json":        true,
	"data/update-state.json":   true,
	"data/update-pending.json": true,
	"data/update-check.json":   true,
}

// Manifest describes an archive. It holds no secrets.
type Manifest struct {
	Format    int    `json:"format"`
	CreatedAt string `json:"created_at"`
	Version   string `json:"version,omitempty"`
	DataDir   string `json:"data_dir"`
	AuthDir   string `json:"auth_dir"`
}

// Options controls what Write includes.
type Options struct {
	ConfigPath string
	Version    string
	// LiveSnapshots uses tpr.Snapshots(), for the running server. The alternative
	// copies data/ directly, which is only safe while the server is stopped.
	LiveSnapshots bool
}

// Write streams an encrypted archive of the config, auth files and router state.
func Write(ctx context.Context, w io.Writer, passphrase string, opts Options) error {
	encrypted, err := newEncryptWriter(w, passphrase)
	if err != nil {
		return err
	}
	compressed := gzip.NewWriter(encrypted)
	archive := tar.NewWriter(compressed)
	if err := writeEntries(ctx, archive, opts); err != nil {
		return err
	}
	if err := archive.Close(); err != nil {
		return err
	}
	if err := compressed.Close(); err != nil {
		return err
	}
	return encrypted.Close()
}

func writeEntries(ctx context.Context, archive *tar.Writer, opts Options) error {
	authDir := home.AuthDir()
	if info, err := setup.LoadServerInfo(opts.ConfigPath); err == nil && info.AuthDir != "" {
		authDir = expandHome(info.AuthDir)
	}
	manifest, err := json.Marshal(Manifest{
		Format:    formatVersion,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Version:   opts.Version,
		DataDir:   home.Dir(),
		AuthDir:   authDir,
	})
	if err != nil {
		return err
	}
	if err := writeEntry(archive, manifestName, manifest); err != nil {
		return err
	}
	if raw, errRead := os.ReadFile(opts.ConfigPath); errRead == nil {
		if err := writeEntry(archive, configEntry, raw); err != nil {
			return err
		}
	} else if !errors.Is(errRead, os.ErrNotExist) {
		return fmt.Errorf("read config: %w", errRead)
	}
	if err := writeTree(ctx, archive, authDir, "auth", nil); err != nil {
		return err
	}
	if !opts.LiveSnapshots {
		return writeTree(ctx, archive, home.DataDir(), "data", nil)
	}
	snapshots := tpr.Snapshots()
	names := make([]string, 0, len(snapshots))
	for name := range snapshots {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := writeSnapshot(ctx, archive, name, snapshots[name]); err != nil {
			return fmt.Errorf("snapshot %s: %w", name, err)
		}
	}
	// Plain state files need no snapshot function. A database does: copying one that
	// is in use is what the snapshot exists to avoid, so those stay out.
	return writeTree(ctx, archive, home.DataDir(), "data", func(name string) bool {
		_, covered := snapshots[name]
		return covered || strings.HasSuffix(name, ".db") || strings.HasSuffix(name, ".db-lock")
	})
}

func writeSnapshot(ctx context.Context, archive *tar.Writer, name string, snapshot tpr.SnapshotFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !safeEntryName(name) {
		return errors.New("unsafe snapshot name")
	}
	// Tar needs the size first and snapshots can be large, so go through a temp file.
	tmp, err := os.CreateTemp(home.BackupsDir(), ".snapshot-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	if err := snapshot(tmp); err != nil {
		return err
	}
	size, err := tmp.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if size == 0 {
		return nil
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := archive.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: size, ModTime: time.Now()}); err != nil {
		return err
	}
	_, err = io.Copy(archive, tmp)
	return err
}

func writeEntry(archive *tar.Writer, name string, data []byte) error {
	if err := archive.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: time.Now()}); err != nil {
		return err
	}
	_, err := archive.Write(data)
	return err
}

func writeTree(ctx context.Context, archive *tar.Writer, root, prefix string, skip func(name string) bool) error {
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		// A log folder may live inside auth-dir; it is not worth restoring.
		if prefix == "auth" && strings.HasPrefix(filepath.ToSlash(relative), "logs/") {
			return nil
		}
		name := path.Join(prefix, filepath.ToSlash(relative))
		if ephemeralData[name] || (skip != nil && skip(name)) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		file, err := os.Open(current)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		if err := archive.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: info.Size(), ModTime: info.ModTime()}); err != nil {
			return err
		}
		_, err = io.CopyN(archive, file, info.Size())
		return err
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// safeEntryName reports whether an archive path stays inside the data folder and
// is free of anything that means something different on another OS.
func safeEntryName(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") {
		return false
	}
	if path.Clean(name) != name || ephemeralData[name] {
		return false
	}
	first := true
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
		// Windows treats these as aliases of the same path.
		if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
		if first {
			first = false
			switch part {
			case "bin", "backups", "logs", "credentials.txt", "static":
				return false
			}
			if strings.HasPrefix(part, ".restore") {
				return false
			}
		}
	}
	return true
}

// Inspect is the result of validating an archive.
type Inspect struct {
	Manifest Manifest
	Files    int
}

// extract validates the archive and writes it to a new staging folder. The real
// data folder is not touched, so a failure here leaves everything as it was.
func extract(r io.Reader, passphrase, staging string) (Inspect, error) {
	decrypted, err := newDecryptReader(r, passphrase)
	if err != nil {
		return Inspect{}, err
	}
	compressed, err := gzip.NewReader(decrypted)
	if err != nil {
		return Inspect{}, plainError(err)
	}
	archive := tar.NewReader(compressed)
	var inspect Inspect
	var total int64
	seen := map[string]bool{}
	hasManifest := false
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Inspect{}, plainError(err)
		}
		if header.Typeflag != tar.TypeReg {
			return Inspect{}, fmt.Errorf("the archive contains %q, which is not a regular file; refusing to restore it", header.Name)
		}
		if !safeEntryName(header.Name) {
			return Inspect{}, fmt.Errorf("the archive contains the unsafe path %q; refusing to restore it", header.Name)
		}
		folded := strings.ToLower(header.Name)
		if seen[folded] {
			return Inspect{}, fmt.Errorf("the archive contains %q twice; refusing to restore it", header.Name)
		}
		seen[folded] = true
		inspect.Files++
		if inspect.Files > maxEntries {
			return Inspect{}, errors.New("the archive has too many files")
		}
		total += header.Size
		if header.Size < 0 || total > maxExtracted {
			return Inspect{}, errors.New("the archive is too large to restore")
		}
		destination := filepath.Join(staging, filepath.FromSlash(header.Name))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return Inspect{}, err
		}
		file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return Inspect{}, err
		}
		_, errCopy := io.CopyN(file, archive, header.Size)
		errClose := file.Close()
		if errCopy != nil {
			return Inspect{}, plainError(errCopy)
		}
		if errClose != nil {
			return Inspect{}, errClose
		}
		if header.Name == manifestName {
			hasManifest = true
		}
	}
	// Tar stops reading at its end marker, so read on to the final chunk. Only that
	// chunk proves the archive was not cut short.
	if _, err := io.Copy(io.Discard, compressed); err != nil {
		return Inspect{}, plainError(err)
	}
	if err := compressed.Close(); err != nil {
		return Inspect{}, plainError(err)
	}
	if _, err := io.Copy(io.Discard, decrypted); err != nil {
		return Inspect{}, plainError(err)
	}
	if !hasManifest {
		return Inspect{}, errors.New("the archive has no manifest, so it is not a Token Proxy Router backup")
	}
	raw, err := os.ReadFile(filepath.Join(staging, manifestName))
	if err != nil {
		return Inspect{}, err
	}
	if err := json.Unmarshal(raw, &inspect.Manifest); err != nil {
		return Inspect{}, fmt.Errorf("the archive's manifest is unreadable: %w", err)
	}
	if inspect.Manifest.Format != formatVersion {
		return Inspect{}, fmt.Errorf("this backup uses format %d, which this version cannot read. Update first", inspect.Manifest.Format)
	}
	if raw, errConfig := os.ReadFile(filepath.Join(staging, configEntry)); errConfig == nil {
		if _, err := config.ParseConfigBytes(raw); err != nil {
			return Inspect{}, fmt.Errorf("the archive's config is not valid: %w", err)
		}
	}
	return inspect, nil
}

// plainError keeps our own decryption errors and turns tar or gzip noise into a
// damage report, since encrypted data that opens correctly never yields either.
func plainError(err error) error {
	if errors.Is(err, ErrBadPassphrase) || errors.Is(err, ErrCorrupt) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrCorrupt, err)
}

func expandHome(dir string) string {
	if dir == "~" || strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, `~\`) {
		if userHome, err := os.UserHomeDir(); err == nil {
			return filepath.Join(userHome, dir[1:])
		}
	}
	return dir
}
