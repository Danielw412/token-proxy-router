package update

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
)

func TestLockAllowsOneUpdateAtATime(t *testing.T) {
	t.Setenv(home.EnvHome, t.TempDir())
	now := time.Now()
	release, err := acquireLock(now)
	if err != nil {
		t.Fatal(err)
	}
	if _, errSecond := acquireLock(now); errSecond == nil || !strings.Contains(errSecond.Error(), "already running") {
		t.Fatalf("second lock: %v", errSecond)
	}
	release()
	again, err := acquireLock(now)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again()
}

func TestLockLeftByADeadProcessExpires(t *testing.T) {
	t.Setenv(home.EnvHome, t.TempDir())
	now := time.Now()
	if _, err := acquireLock(now); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLock(now.Add(staleLockAfter - time.Minute)); err == nil {
		t.Fatal("a recent lock must hold")
	}
	release, err := acquireLock(now.Add(staleLockAfter + time.Minute))
	if err != nil {
		t.Fatalf("stale lock: %v", err)
	}
	release()
	if _, err := os.Stat(lockPath()); !os.IsNotExist(err) {
		t.Fatal("release must remove the lock")
	}
}
