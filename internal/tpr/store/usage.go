package store

import (
	"encoding/json"
	"time"

	bolt "go.etcd.io/bbolt"
)

// KeyHour is one client key's usage in one hour. KeyID is a hash prefix: the raw
// key is never stored.
type KeyHour struct {
	Hour         time.Time        `json:"-"`
	Requests     int64            `json:"q,omitempty"`
	Failures     int64            `json:"f,omitempty"`
	InputTokens  int64            `json:"i,omitempty"`
	OutputTokens int64            `json:"o,omitempty"`
	CachedTokens int64            `json:"c,omitempty"`
	TotalTokens  int64            `json:"t,omitempty"`
	LastUsed     int64            `json:"l,omitempty"`
	Models       map[string]Model `json:"m,omitempty"`
}

// Model is one model's share of a KeyHour.
type Model struct {
	Requests    int64 `json:"q,omitempty"`
	TotalTokens int64 `json:"t,omitempty"`
}

// Add folds another hour's numbers into this one.
func (h *KeyHour) Add(o KeyHour) {
	h.Requests += o.Requests
	h.Failures += o.Failures
	h.InputTokens += o.InputTokens
	h.OutputTokens += o.OutputTokens
	h.CachedTokens += o.CachedTokens
	h.TotalTokens += o.TotalTokens
	if o.LastUsed > h.LastUsed {
		h.LastUsed = o.LastUsed
	}
	for name, m := range o.Models {
		if h.Models == nil {
			h.Models = map[string]Model{}
		}
		cur := h.Models[name]
		cur.Requests += m.Requests
		cur.TotalTokens += m.TotalTokens
		h.Models[name] = cur
	}
}

// AddKeyUsage folds pending per-key hours into the database in one transaction.
func (s *Store) AddKeyUsage(pending map[string]map[int64]KeyHour) error {
	if len(pending) == 0 {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		root := tx.Bucket(bucketUsage)
		for keyID, hours := range pending {
			series, err := root.CreateBucketIfNotExists([]byte(keyID))
			if err != nil {
				return err
			}
			for hour, delta := range hours {
				key := timeKey(time.Unix(hour, 0))
				var row KeyHour
				if raw := series.Get(key); raw != nil {
					if errDecode := json.Unmarshal(raw, &row); errDecode != nil {
						row = KeyHour{}
					}
				}
				row.Add(delta)
				raw, errEncode := json.Marshal(row)
				if errEncode != nil {
					return errEncode
				}
				if err = series.Put(key, raw); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// KeyUsage returns each key's hours in [from, to], keyed by key ID, oldest first.
func (s *Store) KeyUsage(from, to time.Time) (map[string][]KeyHour, error) {
	out := map[string][]KeyHour{}
	err := s.db.View(func(tx *bolt.Tx) error {
		root := tx.Bucket(bucketUsage)
		end := timeKey(to)
		for _, name := range nestedNames(root) {
			c := root.Bucket(name).Cursor()
			for k, v := c.Seek(timeKey(from.Truncate(time.Hour))); k != nil && string(k) <= string(end); k, v = c.Next() {
				at, ok := keyTime(k)
				if !ok {
					continue
				}
				var row KeyHour
				if json.Unmarshal(v, &row) != nil {
					continue
				}
				row.Hour = at
				out[string(name)] = append(out[string(name)], row)
			}
		}
		return nil
	})
	return out, err
}
