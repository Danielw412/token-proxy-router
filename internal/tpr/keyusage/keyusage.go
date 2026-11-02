// Package keyusage aggregates requests by client API key. A key is only ever
// handled as a hash prefix: the raw key is never stored or returned.
package keyusage

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/connect"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/store"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	usagePluginName = "tpr-keyusage"
	flushEvery      = 30 * time.Second
	defaultSpan     = 7 * 24 * time.Hour
	maxSpan         = 366 * 24 * time.Hour
	// maxPending bounds usage held in memory if the database cannot be written.
	maxPending = 5000
)

func init() {
	tpr.AddRoutes(func(group *gin.RouterGroup) {
		group.GET("/usage/keys", handleKeys)
	})
}

// KeyID and Mask come from the connect feature, so one key has one identity across
// the API: the panel joins GET /connect with GET /usage/keys on key_id. A key ID is
// a hash prefix, from which the key cannot be recovered.
func KeyID(key string) string { return connect.KeyID(strings.TrimSpace(key)) }

// Mask shows just enough of a key to recognise it: sk-...abcd.
func Mask(key string) string { return connect.MaskKey(strings.TrimSpace(key)) }

// Service implements usage.Plugin.
type Service struct {
	store *store.Store
	cfg   func() *config.Config
	now   func() time.Time

	mu      sync.Mutex
	pending map[string]map[int64]store.KeyHour
	rows    int

	stopped atomic.Bool
	cancel  context.CancelFunc
	done    chan struct{}
}

var current atomic.Pointer[Service]

// Current returns the running service, or nil before it starts.
func Current() *Service { return current.Load() }

// New builds a service. The clock is injected for tests.
func New(st *store.Store, cfg func() *config.Config, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: st, cfg: cfg, now: now, pending: map[string]map[int64]store.KeyHour{}}
}

// Start registers the usage plugin, begins flushing and makes the service current.
func Start(st *store.Store, cfg func() *config.Config) *Service {
	s := New(st, cfg, time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	go s.loop(ctx)
	usage.RegisterNamedPlugin(usagePluginName, s)
	current.Store(s)
	return s
}

// Stop writes what is pending and stops flushing.
func (s *Service) Stop() {
	s.stopped.Store(true)
	current.CompareAndSwap(s, nil)
	if s.cancel != nil {
		s.cancel()
		<-s.done
	}
	s.Flush()
}

func (s *Service) loop(ctx context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(flushEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Flush()
		}
	}
}

// HandleUsage implements usage.Plugin. It runs on the usage queue and only buffers.
func (s *Service) HandleUsage(_ context.Context, rec usage.Record) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("keyusage: recovered from panic: %v", r)
		}
	}()
	key := strings.TrimSpace(rec.APIKey)
	if key == "" || s.stopped.Load() {
		return
	}
	at := rec.RequestedAt
	if at.IsZero() {
		at = s.now()
	}
	total := rec.Detail.TotalTokens
	if total == 0 {
		total = rec.Detail.InputTokens + rec.Detail.OutputTokens
	}
	cached := rec.Detail.CachedTokens
	if cached == 0 {
		cached = rec.Detail.CacheReadTokens
	}
	row := store.KeyHour{
		Requests:     1,
		InputTokens:  rec.Detail.InputTokens,
		OutputTokens: rec.Detail.OutputTokens,
		CachedTokens: cached,
		TotalTokens:  total,
		LastUsed:     at.Unix(),
	}
	if rec.Failed {
		row.Failures = 1
	}
	model := strings.TrimSpace(rec.Alias)
	if model == "" {
		model = strings.TrimSpace(rec.Model)
	}
	if model != "" {
		row.Models = map[string]store.Model{model: {Requests: 1, TotalTokens: total}}
	}
	id := KeyID(key)
	hour := at.Truncate(time.Hour).Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	hours := s.pending[id]
	if hours == nil {
		hours = map[int64]store.KeyHour{}
		s.pending[id] = hours
	}
	current, existed := hours[hour]
	if !existed {
		if s.rows >= maxPending {
			return
		}
		s.rows++
	}
	current.Add(row)
	hours[hour] = current
}

// Flush writes buffered usage. A failed write keeps it for the next try.
func (s *Service) Flush() {
	s.mu.Lock()
	pending := s.pending
	s.pending = map[string]map[int64]store.KeyHour{}
	s.rows = 0
	s.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	if err := s.store.AddKeyUsage(pending); err != nil {
		log.Warnf("keyusage: could not record usage: %v", err)
		s.mu.Lock()
		defer s.mu.Unlock()
		for id, hours := range pending {
			for hour, row := range hours {
				if s.pending[id] == nil {
					s.pending[id] = map[int64]store.KeyHour{}
				}
				merged, existed := s.pending[id][hour]
				if !existed {
					s.rows++
				}
				merged.Add(row)
				s.pending[id][hour] = merged
			}
		}
	}
}

// ModelUsage is one model's share of a key's usage.
type ModelUsage struct {
	Model       string `json:"model"`
	Requests    int64  `json:"requests"`
	TotalTokens int64  `json:"total_tokens"`
}

// SeriesPoint is one bucket of a key's usage.
type SeriesPoint struct {
	At          time.Time `json:"at"`
	Requests    int64     `json:"requests"`
	TotalTokens int64     `json:"total_tokens"`
}

// KeyUsage is one key's row in GET /usage/keys.
type KeyUsage struct {
	KeyID        string        `json:"key_id"`
	Name         string        `json:"name"`
	Masked       string        `json:"masked"`
	Requests     int64         `json:"requests"`
	Failures     int64         `json:"failures"`
	InputTokens  int64         `json:"input_tokens"`
	OutputTokens int64         `json:"output_tokens"`
	CachedTokens int64         `json:"cached_tokens"`
	LastUsedAt   *time.Time    `json:"last_used_at"`
	Models       []ModelUsage  `json:"models"`
	Series       []SeriesPoint `json:"series"`
}

// Report builds the usage table: every configured key, used or not, plus any key
// that has since been removed from the config but still has usage in the range.
// Client keys carry no names in the config, so a key is named by its position; the
// panel's own names, kept in the browser, apply on top of these.
func (s *Service) Report(from, to time.Time, bucket string) ([]KeyUsage, error) {
	s.Flush()
	usageByKey, err := s.store.KeyUsage(from, to)
	if err != nil {
		return nil, err
	}
	var loc *time.Location
	if bucket == "day" {
		loc = time.Local
	}
	out := []KeyUsage{}
	seen := map[string]bool{}
	if s.cfg != nil {
		if cfg := s.cfg(); cfg != nil {
			for i, key := range cfg.APIKeys {
				if strings.TrimSpace(key) == "" {
					continue
				}
				id := KeyID(key)
				if seen[id] {
					continue
				}
				seen[id] = true
				out = append(out, buildRow(id, fmt.Sprintf("Key %d", i+1), Mask(key), usageByKey[id], from, loc))
			}
		}
	}
	removed := make([]string, 0)
	for id := range usageByKey {
		if !seen[id] {
			removed = append(removed, id)
		}
	}
	sort.Strings(removed)
	for _, id := range removed {
		out = append(out, buildRow(id, "Removed key", "", usageByKey[id], from, loc))
	}
	return out, nil
}

// buildRow folds a key's hours into its totals and series. A nil loc buckets by
// hour; otherwise buckets are calendar days in loc, which stays right across DST.
func buildRow(id, name, masked string, hours []store.KeyHour, from time.Time, loc *time.Location) KeyUsage {
	row := KeyUsage{KeyID: id, Name: name, Masked: masked, Models: []ModelUsage{}, Series: []SeriesPoint{}}
	var total store.KeyHour
	buckets := map[int64]*SeriesPoint{}
	for _, hour := range hours {
		if hour.Hour.Before(from.Truncate(time.Hour)) {
			continue
		}
		total.Add(hour)
		at := hour.Hour
		if loc != nil {
			local := at.In(loc)
			at = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).UTC()
		}
		point := buckets[at.Unix()]
		if point == nil {
			point = &SeriesPoint{At: at}
			buckets[at.Unix()] = point
		}
		point.Requests += hour.Requests
		point.TotalTokens += hour.TotalTokens
	}
	for _, point := range buckets {
		row.Series = append(row.Series, *point)
	}
	sort.Slice(row.Series, func(i, j int) bool { return row.Series[i].At.Before(row.Series[j].At) })
	row.Requests = total.Requests
	row.Failures = total.Failures
	row.InputTokens = total.InputTokens
	row.OutputTokens = total.OutputTokens
	row.CachedTokens = total.CachedTokens
	if total.LastUsed > 0 {
		last := time.Unix(total.LastUsed, 0).UTC()
		row.LastUsedAt = &last
	}
	for name, m := range total.Models {
		row.Models = append(row.Models, ModelUsage{Model: name, Requests: m.Requests, TotalTokens: m.TotalTokens})
	}
	sort.Slice(row.Models, func(i, j int) bool {
		if row.Models[i].TotalTokens != row.Models[j].TotalTokens {
			return row.Models[i].TotalTokens > row.Models[j].TotalTokens
		}
		return row.Models[i].Model < row.Models[j].Model
	})
	return row
}

func handleKeys(c *gin.Context) {
	s := Current()
	if s == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage per key is not running"})
		return
	}
	now := s.now()
	from, to := now.Add(-defaultSpan), now
	if raw := strings.TrimSpace(c.Query("from")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "from must be an RFC 3339 time"})
			return
		}
		from = parsed
	}
	if raw := strings.TrimSpace(c.Query("to")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "to must be an RFC 3339 time"})
			return
		}
		to = parsed
	}
	if !to.After(from) || to.Sub(from) > maxSpan {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the range must run forward and be at most 366 days"})
		return
	}
	bucket := strings.ToLower(strings.TrimSpace(c.Query("bucket")))
	switch bucket {
	case "":
		bucket = "hour"
	case "hour", "day":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "bucket must be hour or day"})
		return
	}
	keys, err := s.Report(from, to, bucket)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read usage"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"keys": keys})
}
