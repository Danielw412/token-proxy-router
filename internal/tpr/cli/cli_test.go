package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/service"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/tailscale"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/update"
)

type cliEnv struct {
	dir   string
	user  string
	calls []string
	out   map[string]string
	fail  map[string]string
}

func newEnv(t *testing.T) *cliEnv {
	t.Helper()
	dir := t.TempDir()
	env := &cliEnv{dir: dir, user: filepath.Join(dir, "user"), out: map[string]string{}, fail: map[string]string{}}
	if err := os.MkdirAll(env.user, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(home.EnvHome, filepath.Join(env.user, ".token-proxy-router"))
	t.Setenv("HOME", env.user)
	t.Setenv("USERPROFILE", env.user)
	// T3 Code exports this to its agents and it wins over HOME, so without
	// clearing it a test run from a T3 shell would edit the real T3 settings.
	t.Setenv("T3CODE_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("SHELL", "/bin/bash")
	t.Setenv("PATH", "")
	t.Setenv(setup.EnvManagementKey, "")
	t.Setenv(setup.EnvPort, freePortString(t))
	return env
}

func freePortString(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	return server.URL[strings.LastIndex(server.URL, ":")+1:]
}

func (e *cliEnv) run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	key := name + " " + strings.Join(args, " ")
	e.calls = append(e.calls, key)
	for prefix, stderr := range e.fail {
		if strings.HasPrefix(key, prefix) {
			return nil, []byte(stderr), errors.New("exit status 1")
		}
	}
	for prefix, stdout := range e.out {
		if strings.HasPrefix(key, prefix) {
			return []byte(stdout), nil, nil
		}
	}
	return nil, nil, nil
}

func (e *cliEnv) ctx(stdin string) (*context_, *bytes.Buffer) {
	var out bytes.Buffer
	return &context_{
		build:  Build{Version: "v1.2.3", Commit: "abc", Date: "today"},
		out:    &out,
		errOut: &out,
		in:     strings.NewReader(stdin),
		runner: e.run,
	}, &out
}

func TestRecognizesEveryDesignedCommand(t *testing.T) {
	for _, name := range []string{"install", "uninstall", "start", "stop", "restart", "status", "logs", "open", "creds", "login", "expose", "unexpose", "connect", "update", "backup", "restore", "migrate", "doctor", "version", "service-run"} {
		if _, ok := commands[name]; !ok {
			t.Errorf("missing command %s", name)
		}
	}
	for _, name := range []string{"-config", "--help", "discover", "-claude-login", "foo"} {
		if _, ok := commands[name]; ok {
			t.Errorf("%s must fall through to upstream", name)
		}
	}
}

func TestVersion(t *testing.T) {
	env := newEnv(t)
	c, out := env.ctx("")
	if err := cmdVersion(c, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tpr v1.2.3", "commit abc", "upstream server " + update.UpstreamVersion, "panel " + update.PanelVersion} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestDefaultConfigPath(t *testing.T) {
	newEnv(t)
	if got := DefaultConfigPath(""); got != home.ConfigPath() {
		t.Fatalf("got %s", got)
	}
	if got := DefaultConfigPath("/custom.yaml"); got != "/custom.yaml" {
		t.Fatalf("got %s", got)
	}
}

func TestBeforeConfigLoadCreatesConfig(t *testing.T) {
	newEnv(t)
	t.Setenv("WRITABLE_PATH", "")
	BeforeConfigLoad(home.ConfigPath())
	if _, err := setup.LoadServerInfo(home.ConfigPath()); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("WRITABLE_PATH") != home.Dir() {
		t.Fatalf("WRITABLE_PATH = %q", os.Getenv("WRITABLE_PATH"))
	}
	if setup.ActiveConfigPath() != home.ConfigPath() {
		t.Fatal("active path not set")
	}
}

func TestBeforeConfigLoadLeavesCloudModeAlone(t *testing.T) {
	newEnv(t)
	t.Setenv("DEPLOY", "cloud")
	BeforeConfigLoad(home.ConfigPath())
	if _, err := os.Stat(home.ConfigPath()); !os.IsNotExist(err) {
		t.Fatal("cloud deployments stand by for their own config")
	}
}

func TestLoginRewritesToUpstreamFlags(t *testing.T) {
	env := newEnv(t)
	saved := os.Args
	defer func() { os.Args = saved }()
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"claude"}, []string{"-claude-login"}},
		{[]string{"codex", "--no-browser"}, []string{"-codex-login", "-no-browser"}},
		{[]string{"--no-browser", "codex"}, []string{"-codex-login", "-no-browser"}},
		{[]string{"codex", "--device"}, []string{"-codex-device-login"}},
		{[]string{"claude", "--config", "/x.yaml"}, []string{"-claude-login", "-config", "/x.yaml"}},
	}
	for _, tc := range cases {
		os.Args = []string{"tpr", "login"}
		c, _ := env.ctx("")
		if err := cmdLogin(c, tc.args); !errors.Is(err, errFallThrough) {
			t.Fatalf("args %v: err = %v", tc.args, err)
		}
		if got := strings.Join(os.Args[1:], " "); got != strings.Join(tc.want, " ") {
			t.Errorf("args %v: rewrote to %q", tc.args, got)
		}
	}
	for _, bad := range [][]string{{}, {"gemini"}, {"claude", "codex"}} {
		c, _ := env.ctx("")
		if err := cmdLogin(c, bad); err == nil || errors.Is(err, errFallThrough) {
			t.Errorf("args %v should be rejected: %v", bad, err)
		}
	}
}

func TestCredsShowAndForget(t *testing.T) {
	env := newEnv(t)
	c, out := env.ctx("")
	if err := cmdCreds(c, nil); err == nil || !strings.Contains(err.Error(), "no saved keys") {
		t.Fatalf("err = %v", err)
	}
	if err := setup.WriteCredentials(setup.Keys{API: "sk-a", Management: "m-b"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdCreds(c, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "sk-a") || !strings.Contains(out.String(), "m-b") {
		t.Fatalf("out = %s", out)
	}
	if err := cmdCreds(c, []string{"--forget"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home.CredentialsPath()); !os.IsNotExist(err) {
		t.Fatal("file should be gone")
	}
	if err := cmdCreds(c, []string{"--forget"}); err != nil {
		t.Fatal(err)
	}
}

func TestPathEditorProfileRoundTrip(t *testing.T) {
	env := newEnv(t)
	profile := filepath.Join(env.user, ".bashrc")
	if err := os.WriteFile(profile, []byte("# mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	editor := pathEditor{run: env.run, goos: "linux", shell: "/bin/bash", userHome: env.user}
	dir := filepath.Join(env.user, ".token-proxy-router", "bin")
	message, err := editor.add(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "new terminal") {
		t.Fatalf("message = %q", message)
	}
	raw, _ := os.ReadFile(profile)
	if !strings.Contains(string(raw), `export PATH="$HOME/.token-proxy-router/bin:$PATH" `+pathMarker) {
		t.Fatalf("profile:\n%s", raw)
	}
	if _, err := editor.add(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(profile)
	if strings.Count(string(raw), pathMarker) != 1 {
		t.Fatalf("should not repeat:\n%s", raw)
	}
	if err := editor.remove(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(profile)
	if strings.Contains(string(raw), pathMarker) || !strings.Contains(string(raw), "# mine") {
		t.Fatalf("profile:\n%s", raw)
	}
}

func TestPathEditorUsesLocalBinWhenOnPath(t *testing.T) {
	env := newEnv(t)
	localBin := filepath.Join(env.user, ".local", "bin")
	editor := pathEditor{run: env.run, goos: "linux", shell: "/bin/bash", userHome: env.user, pathEnv: localBin + string(os.PathListSeparator) + "/usr/bin"}
	dir := filepath.Join(env.user, ".token-proxy-router", "bin")
	if _, err := editor.add(dir); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(localBin, "tpr"))
	if err != nil || target != filepath.Join(dir, "tpr") {
		t.Fatalf("target=%q err=%v", target, err)
	}
	if _, err := os.Stat(filepath.Join(env.user, ".bashrc")); !os.IsNotExist(err) {
		t.Fatal("no profile edit needed")
	}
	if err := editor.remove(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(localBin, "tpr")); !os.IsNotExist(err) {
		t.Fatal("link should be gone")
	}
}

func TestPathEditorAlreadyOnPath(t *testing.T) {
	env := newEnv(t)
	dir := filepath.Join(env.user, "bin")
	editor := pathEditor{run: env.run, goos: "linux", shell: "/bin/bash", userHome: env.user, pathEnv: dir}
	if message, err := editor.add(dir); err != nil || message != "" {
		t.Fatalf("message=%q err=%v", message, err)
	}
}

func TestPathEditorWindowsUsesPowerShell(t *testing.T) {
	env := newEnv(t)
	editor := pathEditor{run: env.run, goos: "windows", userHome: env.user, pathEnv: `C:\Windows`}
	if _, err := editor.add(`C:\Users\alice's\.token-proxy-router\bin`); err != nil {
		t.Fatal(err)
	}
	if err := editor.remove(`C:\Users\alice's\.token-proxy-router\bin`); err != nil {
		t.Fatal(err)
	}
	if len(env.calls) != 2 {
		t.Fatalf("calls = %v", env.calls)
	}
	for _, call := range env.calls {
		if !strings.HasPrefix(call, "powershell -NoProfile") || !strings.Contains(call, `alice''s`) {
			t.Errorf("call = %s", call)
		}
	}
}

func TestCheckPurgeTargetRefusesDangerousFolders(t *testing.T) {
	env := newEnv(t)
	if err := checkPurgeTarget(env.user, env.user); err == nil {
		t.Fatal("must refuse the home folder")
	}
	if err := checkPurgeTarget(string(filepath.Separator), env.user); err == nil {
		t.Fatal("must refuse the root")
	}
	unrelated := filepath.Join(env.dir, "documents")
	if err := os.MkdirAll(unrelated, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := checkPurgeTarget(unrelated, env.user); err == nil {
		t.Fatal("must refuse a folder that is not a data folder")
	}
	if err := home.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := checkPurgeTarget(home.Dir(), env.user); err != nil {
		t.Fatal(err)
	}
}

func TestPurgeNeedsConfirmation(t *testing.T) {
	env := newEnv(t)
	if err := home.Ensure(); err != nil {
		t.Fatal(err)
	}
	c, _ := env.ctx("no\n")
	if err := purgeData(c, env.user, false); err == nil {
		t.Fatal("expected a refusal")
	}
	if _, err := os.Stat(home.Dir()); err != nil {
		t.Fatal("folder should stay")
	}
	c, _ = env.ctx("yes\n")
	if err := purgeData(c, env.user, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home.Dir()); !os.IsNotExist(err) {
		t.Fatal("folder should go")
	}
}

func TestAccountSummaryReadsTypesOnly(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.json", `{"type":"claude","access_token":"secret"}`)
	write("b.json", `{"type":"claude"}`)
	write("c.json", `{"type":"codex"}`)
	write("d.json", ``)
	write("e.json", `not json`)
	write("f.txt", `{"type":"ignored"}`)
	counts, empty := accountSummary(dir)
	if counts["claude"] != 2 || counts["codex"] != 1 || counts["other"] != 1 || counts["ignored"] != 0 {
		t.Fatalf("counts = %v", counts)
	}
	if len(empty) != 1 || empty[0] != "d.json" {
		t.Fatalf("empty = %v", empty)
	}
	if strings.Contains(describeCounts(counts), "secret") {
		t.Fatal("secret leaked")
	}
}

// doctorWorld is a config and a set of fake machines for doctor to look at.
type doctorWorld struct {
	env       *cliEnv
	server    *httptest.Server
	root      string
	clockDate string
	d         *doctor
	out       *bytes.Buffer
}

func newDoctorWorld(t *testing.T) *doctorWorld {
	t.Helper()
	env := newEnv(t)
	world := &doctorWorld{env: env, out: &bytes.Buffer{}, root: `<html>`, clockDate: time.Now().UTC().Format(http.TimeFormat)}
	world.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
		case "/clock":
			w.Header().Set("Date", world.clockDate)
			w.WriteHeader(http.StatusOK)
		case "/":
			_, _ = w.Write([]byte(world.root))
		case "/repos/" + update.Repo + "/releases/latest":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(world.server.Close)
	if err := home.Ensure(); err != nil {
		t.Fatal(err)
	}
	world.d = &doctor{
		out:      world.out,
		config:   home.ConfigPath(),
		version:  "v1.2.3",
		goos:     "linux",
		manager:  &service.Manager{Run: env.run, GOOS: "linux", UserHome: env.user},
		ts:       &tailscale.Client{Run: env.run, Binary: func() string { return "tailscale" }, GOOS: "linux"},
		run:      env.run,
		http:     world.server.Client(),
		source:   &update.ReleaseSource{BaseURL: world.server.URL, Client: world.server.Client()},
		clockURL: world.server.URL + "/clock",
		// Never the real default port: tests must not reach anything on this machine.
		oldPorts: nil,
	}
	env.out["tailscale status --json"] = `{"BackendState":"Running","Self":{"DNSName":"host.example.ts.net."}}`
	env.out["tailscale serve status --json"] = `{}`
	return world
}

func (w *doctorWorld) writeConfig(t *testing.T, extra string) {
	t.Helper()
	port := w.server.URL[strings.LastIndex(w.server.URL, ":")+1:]
	config := "host: \"127.0.0.1\"\nport: " + port + "\nauth-dir: \"" + filepath.ToSlash(home.AuthDir()) + "\"\n" + extra
	if err := os.WriteFile(home.ConfigPath(), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
}

const healthyKeys = "api-keys: [\"k\"]\nremote-management:\n  secret-key: \"$2a$10$abcdefghijklmnopqrstuuOQ3GEeFoBpTY3cGVG3CtxKIwYz0MjqW\"\n"

func TestDoctorHealthyMachine(t *testing.T) {
	w := newDoctorWorld(t)
	w.writeConfig(t, healthyKeys)
	if err := os.WriteFile(filepath.Join(home.AuthDir(), "a.json"), []byte(`{"type":"claude"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	w.d.run_(context.Background())
	text := w.out.String()
	for _, want := range []string{"[ok  ] config", "[ok  ] port", "[ok  ] management key", "[ok  ] API key: 1 configured", "[ok  ] accounts: 1 claude", "[ok  ] old install", "[ok  ] update"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	if w.d.failures != 0 {
		t.Errorf("failures = %d:\n%s", w.d.failures, text)
	}
	for _, call := range w.env.calls {
		if strings.Contains(call, "restart") || strings.Contains(call, " stop") || strings.Contains(call, "serve --bg") {
			t.Fatalf("doctor must only look: %s", call)
		}
	}
}

func TestDoctorFindsMissingConfigAndKeys(t *testing.T) {
	w := newDoctorWorld(t)
	w.d.run_(context.Background())
	if !strings.Contains(w.out.String(), "[FAIL] config") {
		t.Fatalf("output:\n%s", w.out)
	}

	w = newDoctorWorld(t)
	w.writeConfig(t, "")
	w.d.run_(context.Background())
	for _, want := range []string{"[FAIL] management key", "[FAIL] API key", "[warn] accounts: none yet", "tpr login claude"} {
		if !strings.Contains(w.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, w.out)
		}
	}
}

func TestDoctorDetectsOldInstallByService(t *testing.T) {
	w := newDoctorWorld(t)
	w.writeConfig(t, healthyKeys)
	w.env.out["systemctl --user is-active cliproxyapi.service"] = "active\n"
	w.d.run_(context.Background())
	text := w.out.String()
	if !strings.Contains(text, "[FAIL] old install") || !strings.Contains(text, "rotating refresh tokens") || !strings.Contains(text, "systemctl --user disable --now cliproxyapi.service") {
		t.Fatalf("output:\n%s", text)
	}
}

func TestDoctorDetectsOldInstallByRootPage(t *testing.T) {
	w := newDoctorWorld(t)
	w.writeConfig(t, healthyKeys)
	w.root = `{"message":"CLI Proxy API Server"}`
	w.d.run_(context.Background())
	if !strings.Contains(w.out.String(), "[FAIL] old install") {
		t.Fatalf("output:\n%s", w.out)
	}
}

func TestDoctorDetectsOldInstallOnWindows(t *testing.T) {
	w := newDoctorWorld(t)
	w.writeConfig(t, healthyKeys)
	w.d.goos = "windows"
	w.env.out["tasklist"] = "cli-proxy-api.exe  1234 Console\n"
	w.d.manager.GOOS = "windows"
	w.d.run_(context.Background())
	if !strings.Contains(w.out.String(), "[FAIL] old install: the cli-proxy-api.exe process is running") {
		t.Fatalf("output:\n%s", w.out)
	}
}

func TestDoctorReportsClockSkew(t *testing.T) {
	w := newDoctorWorld(t)
	w.writeConfig(t, healthyKeys)
	w.clockDate = time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	w.d.run_(context.Background())
	if !strings.Contains(w.out.String(), "[FAIL] clock: off by 1h") {
		t.Fatalf("output:\n%s", w.out)
	}
}

func TestDoctorPortConflict(t *testing.T) {
	w := newDoctorWorld(t)
	other := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(http.StatusNotFound) }))
	defer other.Close()
	port := other.URL[strings.LastIndex(other.URL, ":")+1:]
	config := "host: \"127.0.0.1\"\nport: " + port + "\n" + healthyKeys
	if err := os.WriteFile(home.ConfigPath(), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	w.d.http = other.Client()
	w.d.run_(context.Background())
	if !strings.Contains(w.out.String(), "[FAIL] port: something else is using port "+port) {
		t.Fatalf("output:\n%s", w.out)
	}
}

func TestDoctorServerDown(t *testing.T) {
	w := newDoctorWorld(t)
	stopped := httptest.NewServer(http.NotFoundHandler())
	port, _ := strconv.Atoi(stopped.URL[strings.LastIndex(stopped.URL, ":")+1:])
	stopped.Close()
	config := "host: \"127.0.0.1\"\nport: " + strconv.Itoa(port) + "\n" + healthyKeys
	if err := os.WriteFile(home.ConfigPath(), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	w.d.run_(context.Background())
	if !strings.Contains(w.out.String(), "[warn] port: nothing is listening") || !strings.Contains(w.out.String(), "tpr start") {
		t.Fatalf("output:\n%s", w.out)
	}
}

func TestStatusWithoutConfig(t *testing.T) {
	env := newEnv(t)
	c, out := env.ctx("")
	if err := cmdStatus(c, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no config yet") {
		t.Fatalf("out = %s", out)
	}
}

func TestBackupRefusesShortPassphraseAndExistingFile(t *testing.T) {
	env := newEnv(t)
	t.Setenv(EnvBackupPassphrase, "short")
	c, _ := env.ctx("")
	if err := cmdBackup(c, nil); err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("err = %v", err)
	}
	t.Setenv(EnvBackupPassphrase, "long enough passphrase")
	existing := filepath.Join(env.dir, "exists")
	if err := os.WriteFile(existing, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdBackup(c, []string{existing}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v", err)
	}
}

func TestOfflineBackupRestoreRoundTrip(t *testing.T) {
	env := newEnv(t)
	t.Setenv(EnvBackupPassphrase, "long enough passphrase")
	if err := home.Ensure(); err != nil {
		t.Fatal(err)
	}
	stopped := httptest.NewServer(http.NotFoundHandler())
	port := stopped.URL[strings.LastIndex(stopped.URL, ":")+1:]
	stopped.Close()
	original := "host: \"127.0.0.1\"\nport: " + port + "\nauth-dir: \"" + filepath.ToSlash(home.AuthDir()) + "\"\n"
	if err := os.WriteFile(home.ConfigPath(), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	account := filepath.Join(home.AuthDir(), "a.json")
	if err := os.WriteFile(account, []byte(`{"type":"claude"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(env.dir, "my.tprbak")
	c, out := env.ctx("")
	if err := cmdBackup(c, []string{archive}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), archive) {
		t.Fatalf("out = %s", out)
	}
	if err := os.Remove(account); err != nil {
		t.Fatal(err)
	}
	if err := cmdRestore(c, []string{archive}); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(account); err != nil || !strings.Contains(string(raw), "claude") {
		t.Fatalf("account not restored: %q %v", raw, err)
	}
	t.Setenv(EnvBackupPassphrase, "a different passphrase")
	if err := cmdRestore(c, []string{archive}); err == nil {
		t.Fatal("the wrong passphrase must fail")
	}
}

func TestParseAnywhere(t *testing.T) {
	env := newEnv(t)
	c, _ := env.ctx("")
	flags := newFlags(c, "x")
	verbose := flags.Bool("v", false, "")
	got, err := parseAnywhere(flags, []string{"file", "-v", "other"})
	if err != nil {
		t.Fatal(err)
	}
	if !*verbose || strings.Join(got, ",") != "file,other" {
		t.Fatalf("verbose=%v got=%v", *verbose, got)
	}
}
