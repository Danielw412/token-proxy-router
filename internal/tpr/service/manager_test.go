package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
)

type fakeRun struct {
	calls []string
	fail  map[string]string
	out   map[string]string
}

func (f *fakeRun) run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	key := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, key)
	for prefix, stderr := range f.fail {
		if strings.HasPrefix(key, prefix) {
			return nil, []byte(stderr), errors.New("exit status 1")
		}
	}
	for prefix, stdout := range f.out {
		if strings.HasPrefix(key, prefix) {
			return []byte(stdout), nil, nil
		}
	}
	return nil, nil, nil
}

func (f *fakeRun) called(prefix string) bool {
	for _, call := range f.calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}

func testManager(t *testing.T, goos string, fake *fakeRun) (*Manager, *bytes.Buffer, *bool) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(home.EnvHome, filepath.Join(dir, "data"))
	t.Setenv("XDG_CONFIG_HOME", "")
	if err := home.Ensure(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	started := false
	return &Manager{
		Run:      fake.run,
		GOOS:     goos,
		UserHome: filepath.Join(dir, "user"),
		Username: "alice",
		Out:      &out,
		StartDetached: func(string) error {
			started = true
			return nil
		},
		StopWait: time.Second,
	}, &out, &started
}

func TestRenderUnitEscapesAndPassesHome(t *testing.T) {
	unit := RenderUnit("/opt/my tools/tpr%x", "/data dir", true)
	for _, want := range []string{
		`ExecStart="/opt/my tools/tpr%%x" service-run`,
		`WorkingDirectory="/data dir"`,
		`Environment="TPR_HOME=/data dir"`,
		"Restart=always",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
	if strings.Contains(RenderUnit("/x", "/y", false), "Environment=") {
		t.Error("no override means no Environment line")
	}
}

func TestInstallSystemd(t *testing.T) {
	fake := &fakeRun{out: map[string]string{"loginctl show-user": "Linger=no\n"}}
	m, _, _ := testManager(t, "linux", fake)
	kind, err := m.Install("/home/alice/.token-proxy-router/bin/tpr")
	if err != nil || kind != KindSystemd {
		t.Fatalf("kind=%s err=%v", kind, err)
	}
	raw, err := os.ReadFile(m.UnitPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "service-run") {
		t.Fatalf("unit = %s", raw)
	}
	for _, want := range []string{"systemctl --user daemon-reload", "systemctl --user enable --now token-proxy-router.service", "loginctl enable-linger alice"} {
		if !fake.called(want) {
			t.Errorf("missing call %q in %v", want, fake.calls)
		}
	}
	// The old install's unit name must never appear.
	for _, call := range fake.calls {
		if strings.Contains(call, "cliproxyapi") {
			t.Fatalf("touched the old service: %s", call)
		}
	}
}

func TestInstallSystemdLingerFailureGuides(t *testing.T) {
	fake := &fakeRun{fail: map[string]string{"loginctl enable-linger": "Access denied"}}
	m, out, _ := testManager(t, "linux", fake)
	kind, err := m.Install("/bin/tpr")
	if err != nil || kind != KindSystemd {
		t.Fatalf("kind=%s err=%v", kind, err)
	}
	if !strings.Contains(out.String(), "sudo loginctl enable-linger alice") {
		t.Fatalf("out = %s", out)
	}
}

func TestInstallSystemdSkipsLingerWhenOn(t *testing.T) {
	fake := &fakeRun{out: map[string]string{"loginctl show-user": "Linger=yes\n"}}
	m, _, _ := testManager(t, "linux", fake)
	if _, err := m.Install("/bin/tpr"); err != nil {
		t.Fatal(err)
	}
	if fake.called("loginctl enable-linger") {
		t.Fatal("linger is already on")
	}
}

func TestInstallFallsBackWithoutSystemd(t *testing.T) {
	fake := &fakeRun{fail: map[string]string{"systemctl --user show-environment": "Failed to connect to bus"}}
	m, out, started := testManager(t, "linux", fake)
	kind, err := m.Install("/bin/tpr")
	if err != nil || kind != KindNone || !*started {
		t.Fatalf("kind=%s err=%v started=%v", kind, err, *started)
	}
	if !strings.Contains(out.String(), "not available") {
		t.Fatalf("out = %s", out)
	}
	if _, errStat := os.Stat(m.UnitPath()); !os.IsNotExist(errStat) {
		t.Fatal("no unit should exist")
	}
}

func TestInstallWindowsPrefersTask(t *testing.T) {
	fake := &fakeRun{}
	m, _, started := testManager(t, "windows", fake)
	kind, err := m.Install(`C:\Users\alice\.token-proxy-router\bin\tpr.exe`)
	if err != nil || kind != KindWindowsTask {
		t.Fatalf("kind=%s err=%v", kind, err)
	}
	if !fake.called(`schtasks /Create /TN TokenProxyRouter /SC ONLOGON /RL LIMITED /TR "C:\Users\alice\.token-proxy-router\bin\tpr.exe" service-run --detach`) {
		t.Fatalf("calls = %v", fake.calls)
	}
	if !fake.called("schtasks /Run /TN TokenProxyRouter") || *started {
		t.Fatalf("task should start the service: %v", fake.calls)
	}
	if fake.called("reg add") {
		t.Fatal("no Run entry when the task worked")
	}
}

func TestInstallWindowsFallsBackToRunEntry(t *testing.T) {
	fake := &fakeRun{fail: map[string]string{"schtasks /Create": "ERROR: Access is denied."}}
	m, out, started := testManager(t, "windows", fake)
	kind, err := m.Install(`C:\t\tpr.exe`)
	if err != nil || kind != KindWindowsRun || !*started {
		t.Fatalf("kind=%s err=%v started=%v", kind, err, *started)
	}
	if !fake.called(`reg add HKCU\Software\Microsoft\Windows\CurrentVersion\Run /v TokenProxyRouter /t REG_SZ /d "C:\t\tpr.exe" service-run --detach`) {
		t.Fatalf("calls = %v", fake.calls)
	}
	if !strings.Contains(out.String(), "logon entry") {
		t.Fatalf("should say what it did: %s", out)
	}
}

func TestInstallWindowsBothFail(t *testing.T) {
	fake := &fakeRun{fail: map[string]string{"schtasks /Create": "denied", "reg add": "denied"}}
	m, _, _ := testManager(t, "windows", fake)
	if kind, err := m.Install(`C:\t\tpr.exe`); err == nil || kind != KindNone {
		t.Fatalf("kind=%s err=%v", kind, err)
	}
}

func TestStatusByKind(t *testing.T) {
	t.Run("systemd active", func(t *testing.T) {
		fake := &fakeRun{out: map[string]string{"systemctl --user is-active": "active\n"}}
		m, _, _ := testManager(t, "linux", fake)
		if _, err := m.Install("/bin/tpr"); err != nil {
			t.Fatal(err)
		}
		status := m.Status()
		if status.Kind != KindSystemd || !status.Installed || !status.Running {
			t.Fatalf("status = %+v", status)
		}
	})
	t.Run("linux not installed", func(t *testing.T) {
		m, _, _ := testManager(t, "linux", &fakeRun{})
		status := m.Status()
		if status.Kind != KindNone || status.Installed || status.Running {
			t.Fatalf("status = %+v", status)
		}
	})
	t.Run("windows task", func(t *testing.T) {
		m, _, _ := testManager(t, "windows", &fakeRun{})
		status := m.Status()
		if status.Kind != KindWindowsTask || !status.Installed {
			t.Fatalf("status = %+v", status)
		}
	})
	t.Run("windows run entry", func(t *testing.T) {
		fake := &fakeRun{fail: map[string]string{"schtasks /Query": "not found"}}
		m, _, _ := testManager(t, "windows", fake)
		status := m.Status()
		if status.Kind != KindWindowsRun || !status.Installed {
			t.Fatalf("status = %+v", status)
		}
	})
	t.Run("windows nothing", func(t *testing.T) {
		fake := &fakeRun{fail: map[string]string{"schtasks /Query": "not found", "reg query": "not found"}}
		m, _, _ := testManager(t, "windows", fake)
		status := m.Status()
		if status.Kind != KindNone || status.Installed {
			t.Fatalf("status = %+v", status)
		}
	})
}

func TestUninstallRemovesOnlyOurRegistrations(t *testing.T) {
	t.Run("linux", func(t *testing.T) {
		fake := &fakeRun{}
		m, _, _ := testManager(t, "linux", fake)
		if _, err := m.Install("/bin/tpr"); err != nil {
			t.Fatal(err)
		}
		fake.calls = nil
		if err := m.Uninstall(); err != nil {
			t.Fatal(err)
		}
		if !fake.called("systemctl --user disable token-proxy-router.service") {
			t.Fatalf("calls = %v", fake.calls)
		}
		if _, err := os.Stat(m.UnitPath()); !os.IsNotExist(err) {
			t.Fatal("unit should be removed")
		}
		if _, err := os.Stat(home.Dir()); err != nil {
			t.Fatal("data folder must stay")
		}
	})
	t.Run("windows", func(t *testing.T) {
		fake := &fakeRun{}
		m, _, _ := testManager(t, "windows", fake)
		if err := m.Uninstall(); err != nil && !errors.Is(err, ErrNotRunning) {
			t.Fatal(err)
		}
		if !fake.called("schtasks /Delete /TN TokenProxyRouter /F") || !fake.called(`reg delete HKCU\Software\Microsoft\Windows\CurrentVersion\Run /v TokenProxyRouter /f`) {
			t.Fatalf("calls = %v", fake.calls)
		}
	})
}

func TestStartStopRestartSystemd(t *testing.T) {
	fake := &fakeRun{out: map[string]string{"systemctl --user is-active": "inactive\n"}}
	m, _, _ := testManager(t, "linux", fake)
	if _, err := m.Install("/bin/tpr"); err != nil {
		t.Fatal(err)
	}
	fake.calls = nil
	if err := m.Start("/bin/tpr"); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := m.Restart("/bin/tpr"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"start", "stop", "restart"} {
		if !fake.called("systemctl --user " + want + " token-proxy-router.service") {
			t.Errorf("missing %s in %v", want, fake.calls)
		}
	}
}

func TestStopWithoutSupervisorSaysSo(t *testing.T) {
	m, _, _ := testManager(t, "windows", &fakeRun{fail: map[string]string{"schtasks /Query": "x", "reg query": "x"}})
	if err := m.Stop(); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("err = %v", err)
	}
}

func TestTailReturnsLastLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	var content strings.Builder
	for i := range 10 {
		content.WriteString(strings.Repeat("x", i+1) + "\n")
	}
	if err := os.WriteFile(path, []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Tail(context.Background(), path, 2, false, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != strings.Repeat("x", 9)+"\n"+strings.Repeat("x", 10)+"\n" {
		t.Fatalf("out = %q", out.String())
	}
}

func TestRotatingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	r := &RotatingFile{Path: path, MaxSize: 10}
	defer func() { _ = r.Close() }()
	for _, chunk := range []string{"12345678", "abcdef"} {
		if _, err := r.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	current, _ := os.ReadFile(path)
	old, _ := os.ReadFile(path + ".1")
	if string(current) != "abcdef" || string(old) != "12345678" {
		t.Fatalf("current=%q old=%q", current, old)
	}
}
