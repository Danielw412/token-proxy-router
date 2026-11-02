package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
)

// staleLockAfter frees a lock left by a process that died mid-update. It is longer
// than any real update, because taking over a live update could lose the only
// working rollback copy.
const staleLockAfter = 30 * time.Minute

func lockPath() string { return filepath.Join(home.DataDir(), "update.lock") }

// acquireLock lets one update or rollback run at a time across every process: the
// panel's server and any `tpr update` in a terminal. Without it, a second update
// could save a broken first update as the rollback copy, replacing the working one.
func acquireLock(now time.Time) (release func(), err error) {
	if err := os.MkdirAll(home.DataDir(), 0o700); err != nil {
		return nil, err
	}
	path := lockPath()
	for attempt := 0; ; attempt++ {
		file, errOpen := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errOpen == nil {
			_, _ = fmt.Fprintf(file, "%d\n", os.Getpid())
			_ = file.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(errOpen, os.ErrExist) && !errors.Is(errOpen, os.ErrPermission) {
			return nil, errOpen
		}
		info, errStat := os.Stat(path)
		if errStat == nil && now.Sub(info.ModTime()) > staleLockAfter && attempt == 0 {
			_ = os.Remove(path)
			continue
		}
		return nil, fmt.Errorf("another update is already running. If it was interrupted, this clears on its own within %s, or delete %s", staleLockAfter, path)
	}
}
