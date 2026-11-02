// Package store is Token Proxy Router's database: one bbolt file at
// data/tpr.db holding quota history, alert events and usage per client key.
// It is pure Go and crash safe, and works the same on Linux and Windows.
package store

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	log "github.com/sirupsen/logrus"
	bolt "go.etcd.io/bbolt"
)

// FileName is the database's name inside the data folder.
const FileName = "tpr.db"

// SnapshotName is the database's path inside a backup archive.
const SnapshotName = "data/" + FileName

// openTimeout bounds the wait for the file lock. Another process holding the
// database gets a clear error instead of a hang, on Windows especially.
var openTimeout = 2 * time.Second

// Retention windows from docs/DESIGN.md.
const (
	FullDetailFor = 30 * 24 * time.Hour
	HourlyFor     = 180 * 24 * time.Hour
	// retentionBatch bounds one retention transaction, so a large backlog is
	// worked off in small steps rather than one long write.
	retentionBatch = 2000
	retentionEvery = time.Hour
)

var (
	bucketSamples = []byte("samples")
	bucketHourly  = []byte("samples_hourly")
	bucketKV      = []byte("kv")
	bucketEvents  = []byte("events")
	bucketUsage   = []byte("key_usage")
	allBuckets    = [][]byte{bucketSamples, bucketHourly, bucketKV, bucketEvents, bucketUsage}
)

// ErrInUse means another process has the database open.
var ErrInUse = errors.New("the database is in use by another Token Proxy Router process")

// Store is safe for concurrent use.
type Store struct {
	db   *bolt.DB
	path string
	seq  atomic.Uint64
}

var current atomic.Pointer[Store]

// Current returns the store opened by OpenDefault, or nil.
func Current() *Store { return current.Load() }

// DefaultPath returns the database path inside the data folder.
func DefaultPath() string { return filepath.Join(home.DataDir(), FileName) }

// OpenDefault opens the database in the data folder, makes it current and adds
// it to backups.
func OpenDefault() (*Store, error) {
	s, err := Open(DefaultPath())
	if err != nil {
		return nil, err
	}
	current.Store(s)
	tpr.AddSnapshot(SnapshotName, func(w io.Writer) error {
		live := Current()
		if live == nil {
			return errors.New("the database is not open")
		}
		return live.Backup(w)
	})
	return s, nil
}

// Open opens or creates a database file.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create data folder: %w", err)
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: openTimeout})
	if err != nil {
		if errors.Is(err, bolt.ErrTimeout) {
			return nil, fmt.Errorf("%w: %s", ErrInUse, path)
		}
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	s := &Store{db: db, path: path}
	errInit := db.Update(func(tx *bolt.Tx) error {
		for _, name := range allBuckets {
			if _, errCreate := tx.CreateBucketIfNotExists(name); errCreate != nil {
				return errCreate
			}
		}
		return nil
	})
	if errInit != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize database: %w", errInit)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	current.CompareAndSwap(s, nil)
	return s.db.Close()
}

// Path returns the database file's path.
func (s *Store) Path() string { return s.path }

// Backup writes a consistent copy of the database while it stays usable.
func (s *Store) Backup(w io.Writer) error {
	return s.db.View(func(tx *bolt.Tx) error {
		_, err := tx.WriteTo(w)
		return err
	})
}

// timeKey encodes a time so byte order is time order. Times before 1970 clamp to
// zero, so a bad clock cannot produce a key that sorts after every real one.
func timeKey(t time.Time) []byte {
	ms := t.UnixMilli()
	if ms < 0 {
		ms = 0
	}
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, uint64(ms))
	return key
}

func keyTime(key []byte) (time.Time, bool) {
	if len(key) < 8 {
		return time.Time{}, false
	}
	return time.UnixMilli(int64(binary.BigEndian.Uint64(key[:8]))).UTC(), true
}

// GetJSON reads a value from the key-value bucket. ok is false if it is absent.
func (s *Store) GetJSON(key string, out any) (ok bool, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketKV).Get([]byte(key))
		if raw == nil {
			return nil
		}
		ok = true
		return json.Unmarshal(raw, out)
	})
	return ok, err
}

// PutJSON writes a value to the key-value bucket.
func (s *Store) PutJSON(key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketKV).Put([]byte(key), raw)
	})
}

// AppendEvent adds one record to the events log.
func (s *Store) AppendEvent(at time.Time, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	key := append(timeKey(at), make([]byte, 8)...)
	binary.BigEndian.PutUint64(key[8:], s.seq.Add(1))
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketEvents).Put(key, raw)
	})
}

// RecentEvents returns up to limit raw events, newest first.
func (s *Store) RecentEvents(limit int) ([]json.RawMessage, error) {
	out := []json.RawMessage{}
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketEvents).Cursor()
		for k, v := c.Last(); k != nil && len(out) < limit; k, v = c.Prev() {
			out = append(out, append(json.RawMessage(nil), v...))
		}
		return nil
	})
	return out, err
}

// RunRetention applies retention every hour until ctx ends, starting shortly
// after launch.
func (s *Store) RunRetention(ctx context.Context) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if err := s.ApplyRetention(time.Now()); err != nil {
			log.Warnf("store: retention pass failed: %v", err)
		}
		timer.Reset(retentionEvery)
	}
}

// ApplyRetention keeps full detail for 30 days, one sample per hour to 180 days,
// and drops the rest. Its cost follows the rows that are due, not the size of the
// database: each series is read oldest first and stops at the first row to keep.
func (s *Store) ApplyRetention(now time.Time) error {
	for {
		worked, err := s.retentionStep(now)
		if err != nil {
			return err
		}
		if worked < retentionBatch {
			return nil
		}
	}
}

func (s *Store) retentionStep(now time.Time) (int, error) {
	worked := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		fullCut := timeKey(now.Add(-FullDetailFor))
		dropCut := timeKey(now.Add(-HourlyFor))
		samples, hourly := tx.Bucket(bucketSamples), tx.Bucket(bucketHourly)

		// Move samples past full detail into the hourly series, last one per hour.
		names := nestedNames(samples)
		for _, name := range names {
			series := samples.Bucket(name)
			target, errCreate := hourly.CreateBucketIfNotExists(name)
			if errCreate != nil {
				return errCreate
			}
			n, errRoll := rollUp(series, target, fullCut, retentionBatch-worked)
			worked += n
			if errRoll != nil {
				return errRoll
			}
			if worked >= retentionBatch {
				return nil
			}
		}
		// Drop hourly history past its window, and anything else on the same clock.
		for _, parent := range []*bolt.Bucket{hourly, tx.Bucket(bucketUsage)} {
			for _, name := range nestedNames(parent) {
				n, errDrop := dropOlder(parent.Bucket(name), dropCut, retentionBatch-worked)
				worked += n
				if errDrop != nil {
					return errDrop
				}
				if worked >= retentionBatch {
					return nil
				}
			}
		}
		n, errEvents := dropOlder(tx.Bucket(bucketEvents), dropCut, retentionBatch-worked)
		worked += n
		if errEvents != nil {
			return errEvents
		}
		// A series with nothing left is an account that is gone.
		if worked < retentionBatch {
			for _, parent := range []*bolt.Bucket{samples, hourly, tx.Bucket(bucketUsage)} {
				for _, name := range nestedNames(parent) {
					if k, _ := parent.Bucket(name).Cursor().First(); k == nil {
						if errDelete := parent.DeleteBucket(name); errDelete != nil {
							return errDelete
						}
					}
				}
			}
		}
		return nil
	})
	return worked, err
}

func nestedNames(parent *bolt.Bucket) [][]byte {
	var names [][]byte
	c := parent.Cursor()
	for k, v := c.First(); k != nil; k, v = c.Next() {
		if v == nil {
			names = append(names, append([]byte(nil), k...))
		}
	}
	return names
}

// rollUp moves rows older than cut into target, keyed by hour. Rows arrive oldest
// first, so the last one written for an hour is that hour's latest.
func rollUp(series, target *bolt.Bucket, cut []byte, limit int) (int, error) {
	worked := 0
	c := series.Cursor()
	for k, v := c.First(); k != nil && worked < limit && string(k) < string(cut); k, v = c.First() {
		if at, ok := keyTime(k); ok {
			if err := target.Put(timeKey(at.Truncate(time.Hour)), v); err != nil {
				return worked, err
			}
		}
		if err := c.Delete(); err != nil {
			return worked, err
		}
		worked++
	}
	return worked, nil
}

func dropOlder(series *bolt.Bucket, cut []byte, limit int) (int, error) {
	worked := 0
	c := series.Cursor()
	for k, _ := c.First(); k != nil && worked < limit && string(k) < string(cut); k, _ = c.First() {
		if err := c.Delete(); err != nil {
			return worked, err
		}
		worked++
	}
	return worked, nil
}
