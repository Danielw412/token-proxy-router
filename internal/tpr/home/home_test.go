package home

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirHonoursOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvHome, dir)
	if got := Dir(); got != dir {
		t.Fatalf("Dir() = %q, want %q", got, dir)
	}
	if got := ConfigPath(); got != filepath.Join(dir, "config.yaml") {
		t.Fatalf("ConfigPath() = %q", got)
	}
}

func TestDirDefaultsInsideUserHome(t *testing.T) {
	t.Setenv(EnvHome, "")
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no user home directory in this environment")
	}
	if got, want := Dir(), filepath.Join(userHome, DirName); got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
}

func TestEnsureCreatesTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	t.Setenv(EnvHome, dir)
	if err := Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for _, path := range []string{AuthDir(), DataDir(), LogsDir(), BackupsDir(), BinDir()} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", path)
		}
	}
}
