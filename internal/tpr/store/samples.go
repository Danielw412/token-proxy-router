package store

import (
	"encoding/json"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

// WindowSample is one quota window at one moment. Remaining is percent left.
type WindowSample struct {
	Remaining float64   `json:"r"`
	ResetAt   time.Time `json:"-"`
	Reset     int64     `json:"t,omitempty"`
}

// Sample is one recorded quota reading of one account.
type Sample struct {
	At     time.Time     `json:"-"`
	Short  *WindowSample `json:"s,omitempty"`
	Long   *WindowSample `json:"l,omitempty"`
	Source string        `json:"o,omitempty"` // "response" or "poll"
	Plan   string        `json:"p,omitempty"`
}

func seriesName(provider, account string) []byte {
	return []byte(provider + "\x00" + account)
}

func encodeSample(s Sample) ([]byte, error) {
	for _, w := range []*WindowSample{s.Short, s.Long} {
		if w != nil && !w.ResetAt.IsZero() {
			w.Reset = w.ResetAt.Unix()
		}
	}
	return json.Marshal(s)
}

func decodeSample(at time.Time, raw []byte) (Sample, bool) {
	var s Sample
	if err := json.Unmarshal(raw, &s); err != nil {
		return Sample{}, false
	}
	s.At = at
	for _, w := range []*WindowSample{s.Short, s.Long} {
		if w != nil && w.Reset > 0 {
			w.ResetAt = time.Unix(w.Reset, 0).UTC()
		}
	}
	return s, true
}

// SampleRecord is one sample addressed to an account.
type SampleRecord struct {
	Provider string
	Account  string
	Sample   Sample
}

// AppendSamples writes samples in one transaction.
func (s *Store) AppendSamples(records []SampleRecord) error {
	if len(records) == 0 {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		root := tx.Bucket(bucketSamples)
		for _, rec := range records {
			series, err := root.CreateBucketIfNotExists(seriesName(rec.Provider, rec.Account))
			if err != nil {
				return err
			}
			raw, err := encodeSample(rec.Sample)
			if err != nil {
				return err
			}
			if err = series.Put(timeKey(rec.Sample.At), raw); err != nil {
				return err
			}
		}
		return nil
	})
}

// Samples returns one account's samples in [from, to], oldest first, across the
// hourly and full-detail series.
func (s *Store) Samples(provider, account string, from, to time.Time) ([]Sample, error) {
	out := []Sample{}
	name := seriesName(provider, account)
	err := s.db.View(func(tx *bolt.Tx) error {
		for _, parent := range [][]byte{bucketHourly, bucketSamples} {
			series := tx.Bucket(parent).Bucket(name)
			if series == nil {
				continue
			}
			c := series.Cursor()
			end := timeKey(to)
			for k, v := c.Seek(timeKey(from)); k != nil && string(k) <= string(end); k, v = c.Next() {
				at, ok := keyTime(k)
				if !ok {
					continue
				}
				if sample, okDecode := decodeSample(at, v); okDecode {
					out = append(out, sample)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

// FirstSampleAt returns when an account's oldest recorded sample was taken.
func (s *Store) FirstSampleAt(provider, account string) (time.Time, bool, error) {
	var first time.Time
	found := false
	name := seriesName(provider, account)
	err := s.db.View(func(tx *bolt.Tx) error {
		for _, parent := range [][]byte{bucketHourly, bucketSamples} {
			series := tx.Bucket(parent).Bucket(name)
			if series == nil {
				continue
			}
			if k, _ := series.Cursor().First(); k != nil {
				if at, ok := keyTime(k); ok && (!found || at.Before(first)) {
					first, found = at, true
				}
			}
		}
		return nil
	})
	return first, found, err
}
