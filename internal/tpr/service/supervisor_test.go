package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
)

// TestMain lets the test binary act as the server child, so the supervisor is
// exercised with real processes on every OS, including Windows in CI.
func TestMain(m *testing.M) {
	if mode := os.Getenv("TPR_TEST_CHILD"); mode != "" {
		runFakeChild(mode)
		return
	}
	os.Exit(m.Run())
}

func runFakeChild(mode string) {
	marker := os.Getenv("TPR_TEST_MARKER")
	if marker != "" {
		f, err := os.OpenFile(marker, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString(mode + "\n")
			_ = f.Close()
		}
	}
	switch mode {
	case "crash":
		os.Exit(3)
	case "server":
		listener, err := net.Listen("tcp", "127.0.0.1:"+os.Getenv("TPR_TEST_PORT"))
		if err != nil {
			os.Exit(4)
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		_ = http.Serve(listener, mux)
	case "hang":
		time.Sleep(time.Hour)
	}
}

type supervisorEnv struct {
	marker string
	port   string
}

func newSupervisor(t *testing.T, mode string) (*Supervisor, supervisorEnv) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(home.EnvHome, dir)
	if err := home.Ensure(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	_ = listener.Close()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := supervisorEnv{marker: filepath.Join(dir, "marker"), port: port}
	return &Supervisor{
		Exe:         exe,
		Env:         append(os.Environ(), "TPR_TEST_CHILD="+mode, "TPR_TEST_MARKER="+env.marker, "TPR_TEST_PORT="+port),
		Dir:         dir,
		HealthURL:   func() string { return "http://127.0.0.1:" + port + "/healthz" },
		Backoff:     []time.Duration{10 * time.Millisecond},
		Poll:        20 * time.Millisecond,
		StopGrace:   2 * time.Second,
		StableAfter: time.Hour,
		Log:         &syncBuffer{},
	}, env
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func markerCount(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(raw), "\n")
}

func runInBackground(t *testing.T, s *Supervisor, ctx context.Context) chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	return done
}

func waitDone(t *testing.T, done chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("supervisor: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("supervisor did not stop")
	}
}

func TestSupervisorRestartsCrashedChild(t *testing.T) {
	s, env := newSupervisor(t, "crash")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(t, s, ctx)
	waitFor(t, "several restarts", func() bool { return markerCount(env.marker) >= 3 })
	cancel()
	waitDone(t, done)
}

func TestSupervisorBacksOffOnRepeatedCrashes(t *testing.T) {
	s, env := newSupervisor(t, "crash")
	s.Backoff = []time.Duration{10 * time.Millisecond, 10 * time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(t, s, ctx)
	waitFor(t, "two starts", func() bool { return markerCount(env.marker) >= 2 })
	time.Sleep(300 * time.Millisecond)
	if got := markerCount(env.marker); got != 2 {
		t.Fatalf("starts = %d, the long backoff should hold it at 2", got)
	}
	cancel()
	waitDone(t, done)
}

func TestSupervisorStopFileStopsChildAndExits(t *testing.T) {
	s, env := newSupervisor(t, "server")
	done := runInBackground(t, s, context.Background())
	waitFor(t, "healthy child", func() bool { return s.waitHealthyOnce(env.port) })
	if !SupervisorRunning() {
		t.Fatal("state file should show a live supervisor")
	}
	state, _ := ReadSupervisorState()
	if state.ChildPID == 0 {
		t.Fatal("child pid should be published")
	}
	if err := requestStop(); err != nil {
		t.Fatal(err)
	}
	waitDone(t, done)
	if SupervisorRunning() {
		t.Fatal("state should be gone")
	}
	if s.waitHealthyOnce(env.port) {
		t.Fatal("child should be stopped")
	}
}

func (s *Supervisor) waitHealthyOnce(port string) bool {
	client := http.Client{Timeout: time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func TestSupervisorRestartFileRestartsChild(t *testing.T) {
	s, env := newSupervisor(t, "server")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(t, s, ctx)
	waitFor(t, "healthy child", func() bool { return s.waitHealthyOnce(env.port) })
	first, _ := ReadSupervisorState()
	if err := RequestRestart(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second start", func() bool { return markerCount(env.marker) >= 2 })
	waitFor(t, "healthy again", func() bool { return s.waitHealthyOnce(env.port) })
	second, _ := ReadSupervisorState()
	if second.ChildPID == first.ChildPID {
		t.Fatal("child should be a new process")
	}
	cancel()
	waitDone(t, done)
}

func TestSupervisorIgnoresStaleSignalFiles(t *testing.T) {
	s, env := newSupervisor(t, "server")
	if err := requestStop(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(t, s, ctx)
	waitFor(t, "healthy child", func() bool { return s.waitHealthyOnce(env.port) })
	cancel()
	waitDone(t, done)
}

func TestSupervisorRefusesSecondInstance(t *testing.T) {
	s, env := newSupervisor(t, "server")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(t, s, ctx)
	waitFor(t, "healthy child", func() bool { return s.waitHealthyOnce(env.port) })

	// A state file naming another live process is what a second start would see.
	raw, err := json.Marshal(SupervisorState{PID: os.Getppid()})
	if err != nil {
		t.Fatal(err)
	}
	if os.Getppid() > 0 && pidAlive(os.Getppid()) {
		saved, _ := os.ReadFile(statePath())
		if err := os.WriteFile(statePath(), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		second := &Supervisor{Exe: s.Exe, Log: &syncBuffer{}}
		if err := second.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "already running") {
			t.Fatalf("err = %v", err)
		}
		_ = os.WriteFile(statePath(), saved, 0o600)
	}
	cancel()
	waitDone(t, done)
}

func TestSupervisorCallsOnHealthy(t *testing.T) {
	s, env := newSupervisor(t, "server")
	var once atomic.Bool
	var healthy, failed atomic.Int32
	s.Pending = func() *PendingCheck {
		if once.Swap(true) {
			return nil
		}
		return &PendingCheck{
			Timeout:   20 * time.Second,
			OnHealthy: func() { healthy.Add(1) },
			OnFailed:  func(string) { failed.Add(1) },
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(t, s, ctx)
	waitFor(t, "healthy callback", func() bool { return healthy.Load() == 1 })
	if failed.Load() != 0 {
		t.Fatal("no failure expected")
	}
	if !s.waitHealthyOnce(env.port) {
		t.Fatal("child should keep running")
	}
	cancel()
	waitDone(t, done)
}

func TestSupervisorFailsCheckWhenChildCrashes(t *testing.T) {
	s, env := newSupervisor(t, "crash")
	var once atomic.Bool
	var reason atomic.Value
	s.Pending = func() *PendingCheck {
		if once.Swap(true) {
			return nil
		}
		return &PendingCheck{
			Timeout:   20 * time.Second,
			OnHealthy: func() { t.Error("must not be healthy") },
			OnFailed:  func(why string) { reason.Store(why) },
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(t, s, ctx)
	waitFor(t, "failure callback", func() bool { return reason.Load() != nil })
	if !strings.Contains(reason.Load().(string), "exited before becoming healthy") {
		t.Fatalf("reason = %v", reason.Load())
	}
	waitFor(t, "another start", func() bool { return markerCount(env.marker) >= 2 })
	cancel()
	waitDone(t, done)
}

func TestSupervisorFailsCheckWhenChildStaysUnhealthy(t *testing.T) {
	s, _ := newSupervisor(t, "hang")
	var once atomic.Bool
	var reason atomic.Value
	s.Pending = func() *PendingCheck {
		if once.Swap(true) {
			return nil
		}
		return &PendingCheck{
			Timeout:   300 * time.Millisecond,
			OnHealthy: func() { t.Error("must not be healthy") },
			OnFailed:  func(why string) { reason.Store(why) },
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(t, s, ctx)
	waitFor(t, "failure callback", func() bool { return reason.Load() != nil })
	if !strings.Contains(reason.Load().(string), "did not become healthy") {
		t.Fatalf("reason = %v", reason.Load())
	}
	cancel()
	waitDone(t, done)
}

func TestSupervisorReportsStartFailure(t *testing.T) {
	s, _ := newSupervisor(t, "server")
	s.Exe = filepath.Join(t.TempDir(), "missing")
	var failed atomic.Int32
	var once atomic.Bool
	s.Pending = func() *PendingCheck {
		if once.Swap(true) {
			return nil
		}
		return &PendingCheck{Timeout: time.Second, OnHealthy: func() {}, OnFailed: func(string) { failed.Add(1) }}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(t, s, ctx)
	waitFor(t, "failure callback", func() bool { return failed.Load() == 1 })
	cancel()
	waitDone(t, done)
}

func TestChildSeesSupervisedEnv(t *testing.T) {
	if Supervised() {
		t.Fatal("the test process is not supervised")
	}
	t.Setenv(EnvSupervised, "1")
	if !Supervised() {
		t.Fatal("env var should mark supervision")
	}
}
