package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
)

const passphrase = "correct horse battery"

func setupHome(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "home")
	t.Setenv(home.EnvHome, dir)
	if err := home.Ensure(); err != nil {
		t.Fatal(err)
	}
	config := "host: \"127.0.0.1\"\nport: 8317\nauth-dir: \"" + filepath.ToSlash(home.AuthDir()) + "\"\napi-keys: [\"client-key\"]\n"
	mustWrite(t, home.ConfigPath(), config)
	mustWrite(t, filepath.Join(home.AuthDir(), "claude-alice.json"), `{"type":"claude","token":"secret-one"}`)
	mustWrite(t, filepath.Join(home.AuthDir(), "nested", "codex.json"), `{"type":"codex"}`)
	mustWrite(t, filepath.Join(home.DataDir(), "state.bin"), "router state")
	return dir
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func makeArchive(t *testing.T, opts Options) []byte {
	t.Helper()
	var out bytes.Buffer
	if opts.ConfigPath == "" {
		opts.ConfigPath = home.ConfigPath()
	}
	if err := Write(context.Background(), &out, passphrase, opts); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestRoundTripRestoresEverythingAndSnapshotsFirst(t *testing.T) {
	setupHome(t)
	original := mustRead(t, home.ConfigPath())
	archive := makeArchive(t, Options{})

	if bytes.Contains(archive, []byte("secret-one")) || bytes.Contains(archive, []byte("client-key")) {
		t.Fatal("archive must be encrypted")
	}

	mustWrite(t, home.ConfigPath(), "port: 9999\n")
	mustWrite(t, filepath.Join(home.AuthDir(), "claude-alice.json"), `{"token":"changed"}`)
	mustWrite(t, filepath.Join(home.AuthDir(), "added-later.json"), `{}`)
	mustWrite(t, filepath.Join(home.DataDir(), "state.bin"), "newer state")

	inspect, snapshot, err := Stage(context.Background(), bytes.NewReader(archive), passphrase, Options{ConfigPath: home.ConfigPath()})
	if err != nil {
		t.Fatal(err)
	}
	if inspect.Files < 4 {
		t.Fatalf("files = %d", inspect.Files)
	}
	if mustRead(t, home.ConfigPath()) != "port: 9999\n" {
		t.Fatal("staging must not touch the live config")
	}
	if !PendingRestore() {
		t.Fatal("restore should be pending")
	}
	if _, err := os.Stat(snapshot); err != nil {
		t.Fatalf("pre-restore snapshot: %v", err)
	}

	applied, err := ApplyPending(home.ConfigPath())
	if err != nil || !applied {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	if mustRead(t, home.ConfigPath()) != original {
		t.Fatal("config not restored")
	}
	if !strings.Contains(mustRead(t, filepath.Join(home.AuthDir(), "claude-alice.json")), "secret-one") {
		t.Fatal("auth not restored")
	}
	if mustRead(t, filepath.Join(home.AuthDir(), "nested", "codex.json")) != `{"type":"codex"}` {
		t.Fatal("nested auth not restored")
	}
	if _, err := os.Stat(filepath.Join(home.AuthDir(), "added-later.json")); !os.IsNotExist(err) {
		t.Fatal("auth folder should match the backup")
	}
	if mustRead(t, filepath.Join(home.DataDir(), "state.bin")) != "router state" {
		t.Fatal("data not restored")
	}
	if PendingRestore() {
		t.Fatal("restore should be done")
	}
	if applied, err := ApplyPending(home.ConfigPath()); err != nil || applied {
		t.Fatalf("second apply: applied=%v err=%v", applied, err)
	}

	// The snapshot is itself restorable with the same passphrase.
	file, err := os.Open(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, _, err := Stage(context.Background(), file, passphrase, Options{ConfigPath: home.ConfigPath()}); err != nil {
		t.Fatalf("snapshot not restorable: %v", err)
	}
	if applied, err := ApplyPending(home.ConfigPath()); err != nil || !applied {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	// A config that named no auth folder now names this data folder's.
	if !strings.Contains(mustRead(t, home.ConfigPath()), "port: 9999") {
		t.Fatal("snapshot held the wrong config")
	}
}

func TestLargeFileAcrossChunks(t *testing.T) {
	setupHome(t)
	big := strings.Repeat("0123456789abcdef", (3*chunkSize)/16)
	mustWrite(t, filepath.Join(home.DataDir(), "big.bin"), big)
	archive := makeArchive(t, Options{})
	mustWrite(t, filepath.Join(home.DataDir(), "big.bin"), "x")
	if _, _, err := Stage(context.Background(), bytes.NewReader(archive), passphrase, Options{ConfigPath: home.ConfigPath()}); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPending(home.ConfigPath()); err != nil {
		t.Fatal(err)
	}
	if mustRead(t, filepath.Join(home.DataDir(), "big.bin")) != big {
		t.Fatal("big file differs")
	}
}

func TestLiveSnapshotsUseRegisteredFunctions(t *testing.T) {
	setupHome(t)
	tpr.AddSnapshot("data/from-snapshot.db", func(w io.Writer) error {
		_, err := w.Write([]byte("consistent copy"))
		return err
	})
	tpr.AddSnapshot("data/empty-snapshot.db", func(io.Writer) error { return nil })
	archive := makeArchive(t, Options{LiveSnapshots: true})
	if _, _, err := Stage(context.Background(), bytes.NewReader(archive), passphrase, Options{ConfigPath: home.ConfigPath()}); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPending(home.ConfigPath()); err != nil {
		t.Fatal(err)
	}
	if mustRead(t, filepath.Join(home.DataDir(), "from-snapshot.db")) != "consistent copy" {
		t.Fatal("snapshot not restored")
	}
	if _, err := os.Stat(filepath.Join(home.DataDir(), "empty-snapshot.db")); !os.IsNotExist(err) {
		t.Fatal("an empty snapshot is left out")
	}
}

func TestLiveBackupIncludesPlainStateButNotOpenDatabases(t *testing.T) {
	setupHome(t)
	mustWrite(t, filepath.Join(home.DataDir(), "router-codex.json"), "router json")
	mustWrite(t, filepath.Join(home.DataDir(), "other.db"), "database in use")
	mustWrite(t, filepath.Join(home.DataDir(), "update-pending.json"), "stale")
	mustWrite(t, filepath.Join(home.DataDir(), "supervisor.json"), "stale")
	archive := makeArchive(t, Options{LiveSnapshots: true})
	if _, _, err := Stage(context.Background(), bytes.NewReader(archive), passphrase, Options{ConfigPath: home.ConfigPath()}); err != nil {
		t.Fatal(err)
	}
	staged := func(name string) bool {
		_, err := os.Stat(filepath.Join(stagingDir(), filepath.FromSlash(name)))
		return err == nil
	}
	if !staged("data/router-codex.json") {
		t.Fatal("plain router state belongs in a backup")
	}
	for _, name := range []string{"data/other.db", "data/update-pending.json", "data/supervisor.json"} {
		if staged(name) {
			t.Fatalf("%s should not be in a backup", name)
		}
	}
}

func TestLiveBackupLeavesOutTheOpenKeyValueStore(t *testing.T) {
	setupHome(t)
	kvPath := filepath.Join(home.AuthDir(), "kv.db")
	mustWrite(t, kvPath, "store in use")
	archive := makeArchive(t, Options{LiveSnapshots: true})
	mustWrite(t, kvPath, "this machine's store")
	if _, _, err := Stage(context.Background(), bytes.NewReader(archive), passphrase, Options{ConfigPath: home.ConfigPath()}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stagingDir(), "auth", "kv.db")); !os.IsNotExist(err) {
		t.Fatal("an open database in the auth folder should not be in a live backup")
	}
	if _, err := ApplyPending(home.ConfigPath()); err != nil {
		t.Fatal(err)
	}
	if mustRead(t, kvPath) != "this machine's store" {
		t.Fatal("a restore without the store should keep this machine's copy")
	}
	if mustRead(t, filepath.Join(home.AuthDir(), "claude-alice.json")) == "" {
		t.Fatal("auth files should still be restored")
	}
}

func TestOfflineBackupRestoresTheKeyValueStore(t *testing.T) {
	setupHome(t)
	kvPath := filepath.Join(home.AuthDir(), "kv.db")
	mustWrite(t, kvPath, "store at backup time")
	archive := makeArchive(t, Options{})
	mustWrite(t, kvPath, "changed later")
	if _, _, err := Stage(context.Background(), bytes.NewReader(archive), passphrase, Options{ConfigPath: home.ConfigPath()}); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPending(home.ConfigPath()); err != nil {
		t.Fatal(err)
	}
	if mustRead(t, kvPath) != "store at backup time" {
		t.Fatal("a backup taken with the server stopped should restore the store")
	}
}

func TestFailingSnapshotFailsTheBackup(t *testing.T) {
	setupHome(t)
	tpr.AddSnapshot("data/broken.db", func(io.Writer) error { return errors.New("db closed") })
	var out bytes.Buffer
	err := Write(context.Background(), &out, passphrase, Options{ConfigPath: home.ConfigPath(), LiveSnapshots: true})
	if err == nil || !strings.Contains(err.Error(), "db closed") {
		t.Fatalf("err = %v", err)
	}
	// Remove it for the other tests in this process.
	tpr.AddSnapshot("data/broken.db", func(io.Writer) error { return nil })
}

func assertUntouched(t *testing.T) {
	t.Helper()
	if PendingRestore() {
		t.Fatal("nothing should be staged")
	}
	if !strings.Contains(mustRead(t, home.ConfigPath()), "port: 8317") {
		t.Fatal("config changed")
	}
	if !strings.Contains(mustRead(t, filepath.Join(home.AuthDir(), "claude-alice.json")), "secret-one") {
		t.Fatal("auth changed")
	}
	entries, err := os.ReadDir(home.Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".restore") {
			t.Fatalf("left behind %s", entry.Name())
		}
	}
	backups, err := os.ReadDir(home.BackupsDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatal("a rejected archive should not cause a snapshot")
	}
}

func TestWrongPassphraseIsRejected(t *testing.T) {
	setupHome(t)
	archive := makeArchive(t, Options{})
	_, _, err := Stage(context.Background(), bytes.NewReader(archive), "a different passphrase", Options{ConfigPath: home.ConfigPath()})
	if !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("err = %v", err)
	}
	assertUntouched(t)
}

func TestShortPassphraseIsRejected(t *testing.T) {
	setupHome(t)
	err := Write(context.Background(), io.Discard, "too short", Options{ConfigPath: home.ConfigPath()})
	if err == nil || !strings.Contains(err.Error(), "at least 12") {
		t.Fatalf("err = %v", err)
	}
}

func TestTamperedArchivesAreRejected(t *testing.T) {
	setupHome(t)
	archive := makeArchive(t, Options{})
	headerLen := len(magic) + saltSize + 12 + noncePrefixLen

	cases := map[string]func([]byte) []byte{
		"flip a body byte": func(b []byte) []byte {
			out := bytes.Clone(b)
			out[headerLen+50] ^= 0x01
			return out
		},
		"flip the last byte": func(b []byte) []byte {
			out := bytes.Clone(b)
			out[len(out)-1] ^= 0x01
			return out
		},
		"flip the salt": func(b []byte) []byte {
			out := bytes.Clone(b)
			out[len(magic)] ^= 0x01
			return out
		},
		"truncate": func(b []byte) []byte { return bytes.Clone(b[:len(b)-20]) },
		"drop to the header": func(b []byte) []byte {
			return bytes.Clone(b[:headerLen])
		},
		"extra data": func(b []byte) []byte { return append(bytes.Clone(b), 1, 2, 3) },
		"wrong magic": func(b []byte) []byte {
			out := bytes.Clone(b)
			out[0] = 'X'
			return out
		},
		"not a backup": func([]byte) []byte { return []byte("hello") },
		"huge scrypt cost": func(b []byte) []byte {
			out := bytes.Clone(b)
			binary.BigEndian.PutUint32(out[len(magic)+saltSize:], 1<<30)
			return out
		},
		"oversized chunk length": func(b []byte) []byte {
			out := bytes.Clone(b)
			binary.BigEndian.PutUint32(out[headerLen:], 1<<31)
			return out
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := Stage(context.Background(), bytes.NewReader(mutate(archive)), passphrase, Options{ConfigPath: home.ConfigPath()})
			if err == nil {
				t.Fatal("tampering was not detected")
			}
			assertUntouched(t)
		})
	}
}

// craft builds a validly encrypted archive with arbitrary tar headers, which is
// what a malicious backup with a known passphrase looks like.
func craft(t *testing.T, write func(*tar.Writer)) []byte {
	t.Helper()
	var out bytes.Buffer
	encrypted, err := newEncryptWriter(&out, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(encrypted)
	archive := tar.NewWriter(compressed)
	write(archive)
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := encrypted.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func addFile(t *testing.T, archive *tar.Writer, name, content string) {
	t.Helper()
	if err := archive.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
}

const validManifest = `{"format":1,"created_at":"2026-01-01T00:00:00Z"}`

func TestUnsafePathsAreRefused(t *testing.T) {
	for _, name := range []string{
		"../escape.txt",
		"auth/../../escape.txt",
		"/etc/passwd",
		"data/../../../escape",
		`..\escape.txt`,
		`auth\..\..\escape`,
		`C:\Windows\evil.txt`,
		"C:evil",
		"bin/tpr",
		"bin/tpr.previous",
		"backups/x",
		"logs/x",
		"credentials.txt",
		"static/management.html",
		".restore-pending/manifest.json",
		"data/./x",
		"data//x",
		"data/x. ",
		"data/x.",
		"data/update-pending.json",
		"data/supervisor.json",
		"DATA/update-pending.json",
		"data/UPDATE-PENDING.JSON",
		"data/Update.Lock",
		"BIN/tpr.previous.exe",
		"Bin/tpr",
		"AUTH/x.json",
		"Config.Yaml",
		"Manifest.json",
		"config.yaml/x",
		"manifest.json/x",
		"auth",
		"data",
		"anything-else.txt",
		"connect/journal.json",
		"auth/CON",
		"data/nul.txt",
		"auth/com1.json",
		"nul\x00byte",
		"",
	} {
		t.Run(name, func(t *testing.T) {
			setupHome(t)
			archive := craft(t, func(w *tar.Writer) {
				addFile(t, w, manifestName, validManifest)
				if err := w.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: 1}); err != nil {
					// The tar writer itself refused the name, which is also fine.
					t.Skipf("tar writer refused: %v", err)
				}
				_, _ = w.Write([]byte("x"))
			})
			_, _, err := Stage(context.Background(), bytes.NewReader(archive), passphrase, Options{ConfigPath: home.ConfigPath()})
			if err == nil {
				t.Fatal("unsafe path accepted")
			}
			assertUntouched(t)
			if _, errStat := os.Stat(filepath.Join(filepath.Dir(home.Dir()), "escape.txt")); !os.IsNotExist(errStat) {
				t.Fatal("a file escaped the data folder")
			}
		})
	}
}

func TestSymlinkAndDeviceEntriesAreRefused(t *testing.T) {
	for name, header := range map[string]*tar.Header{
		"symlink":  {Typeflag: tar.TypeSymlink, Name: "auth/link", Linkname: "/etc/passwd", Mode: 0o777},
		"hardlink": {Typeflag: tar.TypeLink, Name: "auth/link", Linkname: "/etc/passwd", Mode: 0o777},
		"folder":   {Typeflag: tar.TypeDir, Name: "auth/dir/", Mode: 0o777},
		"device":   {Typeflag: tar.TypeChar, Name: "auth/dev", Mode: 0o666},
	} {
		t.Run(name, func(t *testing.T) {
			setupHome(t)
			archive := craft(t, func(w *tar.Writer) {
				addFile(t, w, manifestName, validManifest)
				if err := w.WriteHeader(header); err != nil {
					t.Fatal(err)
				}
			})
			_, _, err := Stage(context.Background(), bytes.NewReader(archive), passphrase, Options{ConfigPath: home.ConfigPath()})
			if err == nil || !strings.Contains(err.Error(), "not a regular file") {
				t.Fatalf("err = %v", err)
			}
			assertUntouched(t)
		})
	}
}

func TestDuplicateAndCaseCollidingEntriesAreRefused(t *testing.T) {
	setupHome(t)
	archive := craft(t, func(w *tar.Writer) {
		addFile(t, w, manifestName, validManifest)
		addFile(t, w, "auth/a.json", "1")
		addFile(t, w, "auth/A.JSON", "2")
	})
	_, _, err := Stage(context.Background(), bytes.NewReader(archive), passphrase, Options{ConfigPath: home.ConfigPath()})
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("err = %v", err)
	}
	assertUntouched(t)
}

func TestArchiveWithoutManifestIsRefused(t *testing.T) {
	setupHome(t)
	archive := craft(t, func(w *tar.Writer) { addFile(t, w, "auth/a.json", "1") })
	if _, _, err := Stage(context.Background(), bytes.NewReader(archive), passphrase, Options{ConfigPath: home.ConfigPath()}); err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("err = %v", err)
	}
	assertUntouched(t)
}

func TestFutureFormatIsRefused(t *testing.T) {
	setupHome(t)
	archive := craft(t, func(w *tar.Writer) { addFile(t, w, manifestName, `{"format":99}`) })
	if _, _, err := Stage(context.Background(), bytes.NewReader(archive), passphrase, Options{ConfigPath: home.ConfigPath()}); err == nil || !strings.Contains(err.Error(), "format 99") {
		t.Fatalf("err = %v", err)
	}
	assertUntouched(t)
}

func TestInvalidConfigIsRefused(t *testing.T) {
	setupHome(t)
	archive := craft(t, func(w *tar.Writer) {
		addFile(t, w, manifestName, validManifest)
		addFile(t, w, configEntry, "port: [not valid\n")
	})
	if _, _, err := Stage(context.Background(), bytes.NewReader(archive), passphrase, Options{ConfigPath: home.ConfigPath()}); err == nil || !strings.Contains(err.Error(), "not valid") {
		t.Fatalf("err = %v", err)
	}
	assertUntouched(t)
}

func TestRestoreOnAnotherMachineMovesAuthDir(t *testing.T) {
	setupHome(t)
	oldAuth := home.AuthDir()
	archive := makeArchive(t, Options{})

	// A different data folder stands in for another machine.
	newHome := filepath.Join(t.TempDir(), "other")
	t.Setenv(home.EnvHome, newHome)
	if err := home.Ensure(); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, home.ConfigPath(), "port: 1\n")
	if _, _, err := Stage(context.Background(), bytes.NewReader(archive), passphrase, Options{ConfigPath: home.ConfigPath()}); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPending(home.ConfigPath()); err != nil {
		t.Fatal(err)
	}
	restored := mustRead(t, home.ConfigPath())
	if strings.Contains(restored, filepath.ToSlash(oldAuth)) {
		t.Fatalf("old auth-dir kept: %s", restored)
	}
	// Parse it, because YAML escapes the backslashes in a Windows path.
	parsed, err := config.ParseConfigBytes([]byte(restored))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(parsed.AuthDir) != filepath.Clean(home.AuthDir()) {
		t.Fatalf("auth-dir not moved: %s", restored)
	}
	if !strings.Contains(mustRead(t, filepath.Join(home.AuthDir(), "claude-alice.json")), "secret-one") {
		t.Fatal("auth not in the new folder")
	}
}

func TestSafeEntryNameAcceptsRealPaths(t *testing.T) {
	for _, name := range []string{"config.yaml", "manifest.json", "auth/claude-alice@example.com.json", "data/tpr.db", "data/tailscale.json"} {
		if !safeEntryName(name) {
			t.Errorf("%q should be allowed", name)
		}
	}
}

// An archive's config must not be able to choose where a restore writes or deletes.
func TestRestoreNeverTouchesAnAuthDirOutsideTheDataFolder(t *testing.T) {
	setupHome(t)
	outside := t.TempDir()
	victim := filepath.Join(outside, "keep-me.json")
	mustWrite(t, victim, "precious")

	archive := craft(t, func(w *tar.Writer) {
		addFile(t, w, manifestName, validManifest)
		addFile(t, w, configEntry, "auth-dir: \""+filepath.ToSlash(outside)+"\"\n")
		addFile(t, w, "auth/claude-alice@example.com.json", "new-login")
	})
	if _, _, err := Stage(context.Background(), bytes.NewReader(archive), passphrase, Options{ConfigPath: home.ConfigPath()}); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPending(home.ConfigPath()); err != nil {
		t.Fatal(err)
	}
	if mustRead(t, victim) != "precious" {
		t.Fatal("a file outside the data folder was changed")
	}
	if _, err := os.Stat(filepath.Join(outside, "claude-alice@example.com.json")); !os.IsNotExist(err) {
		t.Fatal("restore wrote outside the data folder")
	}
	if mustRead(t, filepath.Join(home.AuthDir(), "claude-alice@example.com.json")) != "new-login" {
		t.Fatal("login not restored into the data folder")
	}
	parsed, err := config.ParseConfigBytes([]byte(mustRead(t, home.ConfigPath())))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(parsed.AuthDir) != filepath.Clean(home.AuthDir()) {
		t.Fatalf("config still points elsewhere: %s", parsed.AuthDir)
	}
}

// A backup with no logins restores to no logins, and state created after the backup
// does not survive, apart from files describing this running process.
func TestRestoreMatchesTheBackupExactly(t *testing.T) {
	setupHome(t)
	mustWrite(t, filepath.Join(home.AuthDir(), "newer-login.json"), "newer")
	mustWrite(t, filepath.Join(home.DataDir(), "tailscale.json"), `{"identity":{"enabled":true}}`)
	mustWrite(t, filepath.Join(home.DataDir(), "update-state.json"), "keep")

	archive := craft(t, func(w *tar.Writer) {
		addFile(t, w, manifestName, validManifest)
		addFile(t, w, configEntry, "port: 8317\n")
		addFile(t, w, "data/setup.json", "{}")
	})
	if _, _, err := Stage(context.Background(), bytes.NewReader(archive), passphrase, Options{ConfigPath: home.ConfigPath()}); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPending(home.ConfigPath()); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{filepath.Join(home.AuthDir(), "newer-login.json"), filepath.Join(home.DataDir(), "tailscale.json")} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s survived the restore", gone)
		}
	}
	if mustRead(t, filepath.Join(home.DataDir(), "update-state.json")) != "keep" {
		t.Fatal("process state was removed")
	}
	if mustRead(t, filepath.Join(home.DataDir(), "setup.json")) != "{}" {
		t.Fatal("backup state not restored")
	}
}
