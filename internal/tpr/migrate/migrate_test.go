package migrate

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
	"golang.org/x/crypto/bcrypt"
)

const hashedKey = "$2a$10$abcdefghijklmnopqrstuuOQ3GEeFoBpTY3cGVG3CtxKIwYz0MjqW"

type fakeInstall struct {
	userHome string
	oldDir   string
	oldAuth  string
}

func newFake(t *testing.T, config string) fakeInstall {
	t.Helper()
	dir := t.TempDir()
	f := fakeInstall{userHome: filepath.Join(dir, "user")}
	f.oldDir = filepath.Join(f.userHome, "cliproxyapi")
	f.oldAuth = filepath.Join(f.userHome, ".cli-proxy-api")
	t.Setenv(home.EnvHome, filepath.Join(f.userHome, ".token-proxy-router"))
	writeFile(t, filepath.Join(f.oldDir, "config.yaml"), config)
	writeFile(t, filepath.Join(f.oldAuth, "claude-alice@example.com.json"), `{"type":"claude","email":"alice@example.com"}`)
	writeFile(t, filepath.Join(f.oldAuth, "codex-alice.json"), `{"type":"codex"}`)
	writeFile(t, filepath.Join(f.oldAuth, "empty.json"), "")
	writeFile(t, filepath.Join(f.oldAuth, "logs", "main.log"), "not an account")
	return f
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			tree[path] = readFile(t, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func legacyConfig(f string) string {
	return `# my old config
host: "127.0.0.1"
port: 8317
auth-dir: "~/.cli-proxy-api"
api-keys:
  - "client-key-one"
  - "client-key-two"
remote-management:
  secret-key: "` + hashedKey + `"
  allow-remote: false
request-retry: 7
plugins:
  enabled: true
  dir: "./plugins"
  configs:
    custom-quota-router:
      enabled: true
      priority: 10
      threshold_percent: 42
      state_file: "./plugins/state/custom-quota-router.json"
`
}

const v8Config = `config-version: 8
server:
  host: "127.0.0.1"
  port: 8400
access:
  api-keys:
    - "client-key-v8"
management:
  secret-key: "` + hashedKey + `"
oauth:
  auth-dir: "~/.cli-proxy-api"
routing:
  retry:
    request-retry: 9
plugins:
  enabled: false
  configs: {}
`

type importCall struct {
	path     string
	settings map[string]any
	calls    int
}

func runOpts(f fakeInstall, calls *importCall) Options {
	return Options{UserHome: f.userHome, ImportPlugin: func(path string, settings map[string]any) error {
		calls.calls++
		calls.path, calls.settings = path, settings
		return nil
	}}
}

func TestMigrateLegacyInstall(t *testing.T) {
	f := newFake(t, legacyConfig(""))
	writeFile(t, filepath.Join(f.oldDir, "plugins", "state", "custom-quota-router.json"), `{"active":"x"}`)
	before := snapshot(t, f.oldDir)
	beforeAuth := snapshot(t, f.oldAuth)
	var calls importCall
	report, err := Run(runOpts(f, &calls))
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadConfig(home.ConfigPath())
	if err != nil {
		t.Fatalf("the new config does not load: %v", err)
	}
	if cfg.Port != 8317 || cfg.Host != "127.0.0.1" || cfg.RequestRetry != 7 {
		t.Fatalf("values lost: port=%d host=%q retry=%d", cfg.Port, cfg.Host, cfg.RequestRetry)
	}
	if len(cfg.APIKeys) != 2 || cfg.APIKeys[0] != "client-key-one" || cfg.APIKeys[1] != "client-key-two" {
		t.Fatalf("client keys = %v", cfg.APIKeys)
	}
	if cfg.RemoteManagement.SecretKey != hashedKey {
		t.Fatalf("management hash changed: %s", cfg.RemoteManagement.SecretKey)
	}
	if cfg.AuthDir != home.AuthDir() {
		t.Fatalf("auth dir = %s", cfg.AuthDir)
	}
	if cfg.Plugins.Enabled || len(cfg.Plugins.Configs) != 0 {
		t.Fatalf("plugins should be gone: %+v", cfg.Plugins)
	}
	newConfig := readFile(t, home.ConfigPath())
	if strings.Contains(newConfig, "plugins") || strings.Contains(newConfig, "cli-proxy-api") {
		t.Fatalf("old paths remain:\n%s", newConfig)
	}
	if !strings.Contains(newConfig, "# my old config") {
		t.Fatalf("comments should survive:\n%s", newConfig)
	}

	if readFile(t, filepath.Join(home.AuthDir(), "claude-alice@example.com.json")) == "" {
		t.Fatal("auth not copied")
	}
	if _, err := os.Stat(filepath.Join(home.AuthDir(), "empty.json")); !os.IsNotExist(err) {
		t.Fatal("zero-byte files must be skipped")
	}
	if _, err := os.Stat(filepath.Join(home.AuthDir(), "logs")); !os.IsNotExist(err) {
		t.Fatal("logs are not accounts")
	}
	if len(report.Copied) != 2 || len(report.SkippedEmpty) != 1 || report.SkippedEmpty[0] != "empty.json" {
		t.Fatalf("report = %+v", report)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(home.AuthDir(), "codex-alice.json"))
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %o", info.Mode().Perm())
		}
	}

	if calls.calls != 1 || !strings.HasSuffix(filepath.ToSlash(calls.path), "plugins/state/custom-quota-router.json") {
		t.Fatalf("import = %+v", calls)
	}
	if calls.settings["threshold_percent"] != 42 {
		t.Fatalf("settings = %v", calls.settings)
	}
	if !setup.LoadState().Completed {
		t.Fatal("a migrated install should skip the wizard")
	}

	// The source is copied, never moved or changed.
	for path, content := range before {
		if readFile(t, path) != content {
			t.Fatalf("%s changed", path)
		}
	}
	for path, content := range beforeAuth {
		if readFile(t, path) != content {
			t.Fatalf("%s changed", path)
		}
	}

	var out bytes.Buffer
	report.Print(&out, false, "linux")
	text := out.String()
	for _, want := range []string{"systemctl --user disable --now cliproxyapi.service", "tpr install", "only one of the two", "rotate refresh tokens", "empty.json", "tpr stop"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	for _, secret := range []string{"client-key-one", hashedKey} {
		if strings.Contains(text, secret) {
			t.Errorf("output contains a secret:\n%s", text)
		}
	}
}

func TestMigrateV8Layout(t *testing.T) {
	f := newFake(t, v8Config)
	var calls importCall
	if _, err := Run(runOpts(f, &calls)); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(home.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 8400 || cfg.RequestRetry != 9 || cfg.APIKeys[0] != "client-key-v8" || cfg.AuthDir != home.AuthDir() {
		t.Fatalf("cfg: port=%d retry=%d keys=%v auth=%s", cfg.Port, cfg.RequestRetry, cfg.APIKeys, cfg.AuthDir)
	}
	if strings.Contains(readFile(t, home.ConfigPath()), "plugins") {
		t.Fatal("plugins block should be gone")
	}
	if calls.calls != 0 {
		t.Fatal("no plugin, so no import")
	}
}

func TestMigrateHashesPlaintextKey(t *testing.T) {
	f := newFake(t, "port: 8317\nremote-management:\n  secret-key: \"plain-old-key\"\napi-keys: [\"k\"]\n")
	var calls importCall
	if _, err := Run(runOpts(f, &calls)); err != nil {
		t.Fatal(err)
	}
	raw := readFile(t, home.ConfigPath())
	if strings.Contains(raw, "plain-old-key") {
		t.Fatalf("plaintext key copied:\n%s", raw)
	}
	cfg, err := config.LoadConfig(home.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(cfg.RemoteManagement.SecretKey), []byte("plain-old-key")) != nil {
		t.Fatal("the key should still work, hashed")
	}
	if !strings.Contains(readFile(t, filepath.Join(f.oldDir, "config.yaml")), "plain-old-key") {
		t.Fatal("the old file must not change")
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	f := newFake(t, legacyConfig(""))
	var calls importCall
	opts := runOpts(f, &calls)
	opts.DryRun = true
	report, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home.Dir()); !os.IsNotExist(err) {
		t.Fatal("dry run must not create the data folder")
	}
	if calls.calls != 0 {
		t.Fatal("dry run must not import")
	}
	if len(report.Copied) != 2 || !report.PluginFound {
		t.Fatalf("report = %+v", report)
	}
	var out bytes.Buffer
	report.Print(&out, true, "linux")
	if !strings.Contains(out.String(), "nothing was written") {
		t.Fatal(out.String())
	}
}

func TestRefusesExistingConfigWithoutForce(t *testing.T) {
	f := newFake(t, legacyConfig(""))
	if err := home.Ensure(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, home.ConfigPath(), "port: 1\n")
	var calls importCall
	_, err := Run(runOpts(f, &calls))
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v", err)
	}
	if readFile(t, home.ConfigPath()) != "port: 1\n" {
		t.Fatal("config must not change")
	}
	if entries, _ := os.ReadDir(home.AuthDir()); len(entries) != 0 {
		t.Fatal("auth must not change")
	}
}

func TestForceReplacesAndKeepsACopy(t *testing.T) {
	f := newFake(t, legacyConfig(""))
	if err := home.Ensure(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, home.ConfigPath(), "port: 1\n")
	writeFile(t, filepath.Join(home.AuthDir(), "codex-alice.json"), `{"type":"newer"}`)
	var calls importCall
	opts := runOpts(f, &calls)
	opts.Force = true
	if _, err := Run(opts); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFile(t, home.ConfigPath()), "port: 1\n") {
		t.Fatal("config should be replaced")
	}
	snapshots, _ := filepath.Glob(filepath.Join(home.BackupsDir(), "config-*pre-migrate.yaml"))
	if len(snapshots) != 1 || readFile(t, snapshots[0]) != "port: 1\n" {
		t.Fatalf("snapshots = %v", snapshots)
	}
	if !strings.Contains(readFile(t, filepath.Join(home.AuthDir(), "codex-alice.json")), `"codex"`) {
		t.Fatal("--force should overwrite")
	}
}

func TestExistingAuthFilesAreKeptWithoutForce(t *testing.T) {
	f := newFake(t, legacyConfig(""))
	if err := home.Ensure(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home.AuthDir(), "codex-alice.json"), `{"type":"newer"}`)
	var calls importCall
	report, err := Run(runOpts(f, &calls))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, filepath.Join(home.AuthDir(), "codex-alice.json")), "newer") {
		t.Fatal("an existing file must not be overwritten")
	}
	if len(report.KeptExisting) != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestFromPointsAtFolderOrFile(t *testing.T) {
	f := newFake(t, legacyConfig(""))
	moved := filepath.Join(f.userHome, "elsewhere")
	writeFile(t, filepath.Join(moved, "config.yaml"), "port: 9000\napi-keys: [\"k\"]\n")
	var calls importCall
	opts := runOpts(f, &calls)
	opts.From = moved
	opts.DryRun = true
	report, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if report.Port != 9000 {
		t.Fatalf("port = %d", report.Port)
	}
	opts.From = filepath.Join(moved, "config.yaml")
	if report, err = Run(opts); err != nil || report.Port != 9000 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestMissingOrInvalidSourceGivesNextStep(t *testing.T) {
	f := newFake(t, legacyConfig(""))
	var calls importCall
	opts := runOpts(f, &calls)
	opts.From = filepath.Join(f.userHome, "nope")
	if _, err := Run(opts); err == nil || !strings.Contains(err.Error(), "--from") {
		t.Fatalf("err = %v", err)
	}
	empty := filepath.Join(f.userHome, "empty-dir")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	opts.From = empty
	if _, err := Run(opts); err == nil || !strings.Contains(err.Error(), "no config.yaml") {
		t.Fatalf("err = %v", err)
	}
	bad := filepath.Join(f.userHome, "bad")
	writeFile(t, filepath.Join(bad, "config.yaml"), "port: [oops\n")
	opts.From = bad
	if _, err := Run(opts); err == nil || !strings.Contains(err.Error(), "not valid") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(home.ConfigPath()); !os.IsNotExist(err) {
		t.Fatal("nothing should be written")
	}
}

func TestNonLoopbackHostIsKeptButWarned(t *testing.T) {
	f := newFake(t, "host: \"\"\nport: 8317\napi-keys: [\"k\"]\n")
	var calls importCall
	report, err := Run(runOpts(f, &calls))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "not loopback") {
		t.Fatalf("warnings = %v", report.Warnings)
	}
}

func TestRelativeTLSPathsBecomeAbsolute(t *testing.T) {
	f := newFake(t, "port: 8317\napi-keys: [\"k\"]\ntls:\n  enable: true\n  cert: certs/c.pem\n  key: certs/k.pem\n")
	var calls importCall
	if _, err := Run(runOpts(f, &calls)); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(home.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLS.Cert != filepath.Join(f.oldDir, "certs", "c.pem") || cfg.TLS.Key != filepath.Join(f.oldDir, "certs", "k.pem") {
		t.Fatalf("tls = %+v", cfg.TLS)
	}
}

func TestStateFileFoundWithoutSetting(t *testing.T) {
	f := newFake(t, "port: 8317\napi-keys: [\"k\"]\nplugins:\n  configs:\n    custom-quota-router:\n      enabled: true\n")
	writeFile(t, filepath.Join(f.oldDir, "plugins", "custom-quota-router", "state.json"), `{}`)
	var calls importCall
	report, err := Run(runOpts(f, &calls))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(filepath.ToSlash(report.PluginState), "plugins/custom-quota-router/state.json") {
		t.Fatalf("state = %q", report.PluginState)
	}
	if calls.calls != 1 {
		t.Fatal("import expected")
	}
}

func TestRealRouterImportRuns(t *testing.T) {
	f := newFake(t, legacyConfig(""))
	writeFile(t, filepath.Join(f.oldDir, "plugins", "state", "custom-quota-router.json"), `{"current_account":""}`)
	if _, err := Run(Options{UserHome: f.userHome}); err != nil {
		t.Fatalf("real import failed: %v", err)
	}
}
