package setup

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"golang.org/x/crypto/bcrypt"
)

func tempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(home.EnvHome, dir)
	t.Setenv("HOME", filepath.Join(dir, "fake-user"))
	t.Setenv("USERPROFILE", filepath.Join(dir, "fake-user"))
	t.Setenv(EnvPort, freePort(t))
	return dir
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

func TestBootstrapGeneratesLoadableSafeConfig(t *testing.T) {
	dir := tempHome(t)
	first, err := Bootstrap(home.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if first == nil {
		t.Fatal("expected a first run")
	}
	for _, sub := range []string{"auth", "data", "logs", "backups", "bin"} {
		if _, errStat := os.Stat(filepath.Join(dir, sub)); errStat != nil {
			t.Fatalf("missing %s: %v", sub, errStat)
		}
	}
	keys, err := ReadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(keys.API, "sk-tpr-") || !strings.HasPrefix(keys.Management, "tpr-mgmt-") {
		t.Fatalf("unexpected key shapes")
	}
	raw, err := os.ReadFile(home.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), keys.Management) {
		t.Fatal("plaintext management key must not be in config.yaml")
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{home.ConfigPath(), home.CredentialsPath()} {
			info, errStat := os.Stat(path)
			if errStat != nil {
				t.Fatal(errStat)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("%s mode = %o", path, info.Mode().Perm())
			}
		}
	}

	cfg, err := config.LoadConfig(home.ConfigPath())
	if err != nil {
		t.Fatalf("upstream loader rejected generated config: %v", err)
	}
	if cfg.Host != "127.0.0.1" {
		t.Fatalf("host = %q", cfg.Host)
	}
	if cfg.Port != first.Port {
		t.Fatalf("port = %d, want %d", cfg.Port, first.Port)
	}
	if cfg.AuthDir != home.AuthDir() {
		t.Fatalf("auth dir = %q", cfg.AuthDir)
	}
	if !cfg.UsageStatisticsEnabled {
		t.Fatal("usage statistics should be on")
	}
	if len(cfg.APIKeys) != 1 || cfg.APIKeys[0] != keys.API {
		t.Fatal("client key not loaded")
	}
	if bcrypt.CompareHashAndPassword([]byte(cfg.RemoteManagement.SecretKey), []byte(keys.Management)) != nil {
		t.Fatal("management hash does not match the saved key")
	}
	if cfg.RemoteManagement.AllowRemote {
		t.Fatal("remote management must default off")
	}
	after, err := os.ReadFile(home.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(raw) {
		t.Fatal("a bcrypt key must not trigger a rewrite on load")
	}
}

func TestBootstrapLeavesExistingConfigAlone(t *testing.T) {
	tempHome(t)
	path := home.ConfigPath()
	if err := home.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("port: 1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := Bootstrap(path)
	if err != nil || first != nil {
		t.Fatalf("first=%v err=%v", first, err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "port: 1234\n" {
		t.Fatalf("config changed: %q", raw)
	}
	if _, errStat := os.Stat(home.CredentialsPath()); !os.IsNotExist(errStat) {
		t.Fatal("no credentials should be written for an existing config")
	}
}

func TestBootstrapReplacesEmptyConfigAndHonoursCustomPath(t *testing.T) {
	dir := tempHome(t)
	custom := filepath.Join(dir, "elsewhere", "my.yaml")
	if err := os.MkdirAll(filepath.Dir(custom), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(custom, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := Bootstrap(custom)
	if err != nil || first == nil {
		t.Fatalf("first=%v err=%v", first, err)
	}
	if ActiveConfigPath() != custom {
		t.Fatalf("active = %s", ActiveConfigPath())
	}
	if _, err := LoadServerInfo(custom); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapMovesOffABusyPort(t *testing.T) {
	tempHome(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	busy := l.Addr().(*net.TCPAddr).Port
	t.Setenv(EnvPort, strconv.Itoa(busy))
	first, err := Bootstrap(home.ConfigPath())
	if err != nil || first == nil {
		t.Fatalf("first=%v err=%v", first, err)
	}
	if first.Port == busy || first.WantedPort != busy {
		t.Fatalf("port=%d wanted=%d busy=%d", first.Port, first.WantedPort, busy)
	}
	var out strings.Builder
	first.Print(&out)
	if !strings.Contains(out.String(), "already in use") {
		t.Fatalf("message should explain the move: %s", out.String())
	}
}

func TestBootstrapRejectsBadEnvPort(t *testing.T) {
	tempHome(t)
	t.Setenv(EnvPort, "not-a-port")
	if _, err := Bootstrap(home.ConfigPath()); err == nil || !strings.Contains(err.Error(), EnvPort) {
		t.Fatalf("err = %v", err)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"127.0.0.1": true, "127.1.2.3": true, "::1": true, "[::1]": true, "localhost": true,
		"": false, "0.0.0.0": false, "::": false, "192.0.2.1": false, "example.com": false,
	} {
		if got := IsLoopbackHost(host); got != want {
			t.Errorf("IsLoopbackHost(%q) = %v", host, got)
		}
	}
}

func TestStateRoundTrip(t *testing.T) {
	tempHome(t)
	if err := home.Ensure(); err != nil {
		t.Fatal(err)
	}
	if LoadState().Completed {
		t.Fatal("fresh state should be incomplete")
	}
	if _, err := MarkCompleted(); err != nil {
		t.Fatal(err)
	}
	if !LoadState().Completed {
		t.Fatal("state should be complete")
	}
}

func TestManagementKeyPrefersEnv(t *testing.T) {
	tempHome(t)
	if err := home.Ensure(); err != nil {
		t.Fatal(err)
	}
	if ManagementKey() != "" {
		t.Fatal("no key expected")
	}
	if err := WriteCredentials(Keys{API: "a", Management: "from-file"}); err != nil {
		t.Fatal(err)
	}
	if ManagementKey() != "from-file" {
		t.Fatal("file key expected")
	}
	t.Setenv(EnvManagementKey, "from-env")
	if ManagementKey() != "from-env" {
		t.Fatal("env key expected")
	}
}
