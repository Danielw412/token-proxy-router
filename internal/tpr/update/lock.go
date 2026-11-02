package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	bolt "go.etcd.io/bbolt"
)

const lockWait = 100 * time.Millisecond

func lockPath() string { return filepath.Join(home.DataDir(), "update.lock") }

// acquireLock lets one update or rollback run at a time across every process: the
// panel's server and any `tpr update` in a terminal. Without it, a second update
// could save a broken first update as the rollback copy, replacing the working one.
//
// bbolt is only used here for its cross-platform OS file lock, which the operating
// system drops if the holder dies, so a crash can never leave updates stuck and
// nothing has to guess whether an old lock is stale.
func acquireLock() (release func(), err error) {
	if err := os.MkdirAll(home.DataDir(), 0o700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(lockPath(), 0o600, &bolt.Options{Timeout: lockWait})
	if errors.Is(err, bolt.ErrTimeout) {
		return nil, errors.New("another update is already running; wait for it to finish")
	}
	if err != nil {
		return nil, fmt.Errorf("take the update lock: %w", err)
	}
	return func() { _ = db.Close() }, nil
}
