package update

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
)

func TestLockAllowsOneUpdateAtATime(t *testing.T) {
	t.Setenv(home.EnvHome, t.TempDir())
	release, err := acquireLock()
	if err != nil {
		t.Fatal(err)
	}
	if _, errSecond := acquireLock(); errSecond == nil || !strings.Contains(errSecond.Error(), "already running") {
		t.Fatalf("second lock: %v", errSecond)
	}
	release()
	again, err := acquireLock()
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again()
}

// TestLockHelper holds the lock until killed, standing in for a process that dies
// in the middle of an update.
func TestLockHelper(t *testing.T) {
	if os.Getenv("TPR_LOCK_HELPER") != "1" {
		t.Skip("helper for TestLockIsFreedWhenItsHolderDies")
	}
	if _, err := acquireLock(); err != nil {
		t.Fatal(err)
	}
	_, _ = os.Stdout.WriteString("locked\n")
	select {}
}

func TestLockIsFreedWhenItsHolderDies(t *testing.T) {
	t.Setenv(home.EnvHome, t.TempDir())
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "TPR_LOCK_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	buffer := make([]byte, 4096)
	var seen strings.Builder
	for !strings.Contains(seen.String(), "locked") {
		n, errRead := stdout.Read(buffer)
		seen.Write(buffer[:n])
		if errRead != nil {
			t.Fatalf("helper did not take the lock: %v: %s", errRead, seen.String())
		}
	}
	if _, errHeld := acquireLock(); errHeld == nil {
		t.Fatal("the lock must be held while the helper runs")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	release, err := acquireLock()
	if err != nil {
		t.Fatalf("the lock must be free once its holder has died: %v", err)
	}
	release()
}
