package quota

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router/engine"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/store"
	log "github.com/sirupsen/logrus"
)

// Window names of the API.
const (
	WindowWeekly  = "weekly"
	WindowSession = "session"
)

const (
	// minSampleGap bounds how often one account is recorded, however busy it is.
	minSampleGap = time.Minute
	// heartbeatGap records an unchanged account now and then, so a flat line on
	// the chart is a measured one rather than a gap.
	heartbeatGap = 10 * time.Minute
	// changeEpsilon is a change in remaining percent worth a new sample.
	changeEpsilon = 0.5
	flushEvery    = 5 * time.Second
	// refreshEvery rate limits POST /quota/refresh.
	refreshEvery = time.Minute
	// burnLookback reaches past the burn window for the baseline sample.
	burnLookback = 24 * time.Hour
)

// Service records samples from the router's observations and answers queries.
// It does no polling of its own: the router's pools already poll idle accounts,
// and every reading reaches this collector, so an account is never polled twice.
type Service struct {
	store  *store.Store
	router *router.Router
	now    func() time.Time

	mu      sync.Mutex
	last    map[string]store.Sample
	pending []store.SampleRecord

	lastRefresh atomic.Int64
	cancel      context.CancelFunc
	done        chan struct{}
}

var current atomic.Pointer[Service]

// Current returns the running service, or nil before it starts.
func Current() *Service { return current.Load() }

// New builds a service. The clock is injected for tests.
func New(st *store.Store, r *router.Router, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: st, router: r, now: now, last: map[string]store.Sample{}}
}

// Start begins collecting from the router and makes the service current.
func Start(st *store.Store, r *router.Router) *Service {
	s := New(st, r, time.Now)
	s.restoreLast()
	r.AddObserver(s.Observe)
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	go s.loop(ctx)
	current.Store(s)
	return s
}

// Stop writes what is pending and stops the background loop.
func (s *Service) Stop() {
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

// restoreLast seeds the de-duplication state so a restart does not write a burst
// of samples for accounts that did not change.
func (s *Service) restoreLast() {
	if s.router == nil {
		return
	}
	now := s.now()
	for _, provider := range router.Providers {
		pool := s.router.Pool(provider)
		if pool == nil {
			continue
		}
		for _, id := range pool.Engine.AccountIDs() {
			samples, err := s.store.Samples(provider, id, now.Add(-heartbeatGap), now)
			if err == nil && len(samples) > 0 {
				s.last[provider+"\x00"+id] = samples[len(samples)-1]
			}
		}
	}
}

// Observe takes one account's latest quota. It runs on the usage and polling
// goroutines, so it only buffers: Flush does the writing.
func (s *Service) Observe(provider, id string, q engine.Quota) {
	at := q.ObservedAt
	if at.IsZero() {
		at = s.now()
	}
	sample := store.Sample{At: at, Source: q.Source, Plan: q.Plan, Short: windowSample(q.Short, at), Long: windowSample(q.Long, at)}
	if sample.Short == nil && sample.Long == nil {
		return
	}
	key := provider + "\x00" + id
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.last[key]; ok && !worthRecording(prev, sample) {
		return
	}
	s.last[key] = sample
	s.pending = append(s.pending, store.SampleRecord{Provider: provider, Account: id, Sample: sample})
}

func windowSample(w *engine.Window, at time.Time) *store.WindowSample {
	if w == nil {
		return nil
	}
	remaining, _ := w.Remaining(at)
	return &store.WindowSample{Remaining: remaining, ResetAt: w.ResetAt}
}

// worthRecording keeps busy accounts at one sample a minute and quiet ones at a
// heartbeat. A reading older than the last one recorded adds nothing.
func worthRecording(prev, next store.Sample) bool {
	gap := next.At.Sub(prev.At)
	if gap < minSampleGap {
		return false
	}
	if gap >= heartbeatGap {
		return true
	}
	return windowChanged(prev.Short, next.Short) || windowChanged(prev.Long, next.Long)
}

func windowChanged(a, b *store.WindowSample) bool {
	if (a == nil) != (b == nil) {
		return true
	}
	if a == nil {
		return false
	}
	return math.Abs(a.Remaining-b.Remaining) >= changeEpsilon || a.ResetAt.Sub(b.ResetAt).Abs() > time.Minute
}

// Flush writes buffered samples. A failed write keeps them for the next try.
func (s *Service) Flush() {
	s.mu.Lock()
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	if err := s.store.AppendSamples(pending); err != nil {
		log.Warnf("quota: could not record %d samples: %v", len(pending), err)
		s.mu.Lock()
		if len(s.pending) < 10000 {
			s.pending = append(pending, s.pending...)
		}
		s.mu.Unlock()
	}
}

// Query is what GET /quota/pools asks for.
type Query struct {
	From, To time.Time
	Burn     time.Duration
	Window   string
	Future   string
}

// AccountView is one account in a pool's response.
type AccountView struct {
	ID               string     `json:"id"`
	Label            string     `json:"label"`
	Email            string     `json:"email"`
	Plan             string     `json:"plan"`
	Weight           float64    `json:"weight"`
	RemainingPercent *float64   `json:"remaining_percent"`
	ResetAt          *time.Time `json:"reset_at"`
	Status           string     `json:"status"`
}

// PoolView is one pool's response.
type PoolView struct {
	Provider   string        `json:"provider"`
	Accounts   []AccountView `json:"accounts"`
	WeightNote string        `json:"weight_note"`
	Result
}

// Pools computes every pool's numbers at the service's clock.
func (s *Service) Pools(q Query) ([]PoolView, time.Time, error) {
	s.Flush()
	now := s.now()
	out := make([]PoolView, 0, len(router.Providers))
	for _, provider := range router.Providers {
		pool := s.router.Pool(provider)
		if pool == nil {
			continue
		}
		view, err := s.poolView(provider, pool, now, q)
		if err != nil {
			return nil, now, err
		}
		out = append(out, view)
	}
	return out, now, nil
}

func (s *Service) poolView(provider string, pool *router.Pool, now time.Time, q Query) (PoolView, error) {
	state := pool.Engine.View(1)
	accounts := append([]engine.AccountView(nil), state.Accounts...)
	// The order the router would drain them in, from the router's own ranking.
	// Accounts it would not choose right now sort after those it would.
	drain := map[string]int{}
	for i, id := range pool.Engine.DrainOrder() {
		drain[id] = i + 1
	}
	sort.SliceStable(accounts, func(i, j int) bool {
		a, b := accounts[i], accounts[j]
		ra, rb := drain[a.ID], drain[b.ID]
		if (ra != 0) != (rb != 0) {
			return ra != 0
		}
		if ra != rb {
			return ra < rb
		}
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		return a.Label < b.Label
	})

	plans := make([]string, 0, len(accounts))
	for _, a := range accounts {
		plans = append(plans, a.Plan)
	}
	weights, note := planWeights(provider, plans)

	seriesFrom := q.From
	if lookback := now.Add(-q.Burn - burnLookback); lookback.Before(seriesFrom) {
		seriesFrom = lookback
	}
	views := make([]AccountView, 0, len(accounts))
	inputs := make([]AccountInput, 0, len(accounts))
	var firstSample *time.Time
	for _, a := range accounts {
		w := a.Long
		period := 7 * 24 * time.Hour
		if q.Window == WindowSession {
			w, period = a.Short, 5*time.Hour
		}
		view := AccountView{ID: a.ID, Label: a.Label, Email: a.Email, Plan: a.Plan, Weight: round2(weights[a.Plan]), Status: a.State}
		input := AccountInput{
			ID:      a.ID,
			Weight:  weights[a.Plan],
			Period:  period,
			Counted: a.Role != engine.RoleOff && !a.Disabled,
			// Claude's weekly window keeps a fixed weekly schedule; a Codex window
			// and a 5-hour window start with the first request after a reset.
			Anchored: provider == engine.ProviderClaude && q.Window != WindowSession,
		}
		if w != nil {
			if w.WindowMinutes > 0 {
				input.Period = time.Duration(w.WindowMinutes) * time.Minute
			}
			remaining := w.RemainingPercent
			view.RemainingPercent = &remaining
			input.Known, input.Remaining, input.ResetAt = true, remaining, w.ResetAt
			if !w.ResetAt.IsZero() {
				at := w.ResetAt
				view.ResetAt = &at
			}
		}
		samples, err := s.store.Samples(provider, a.ID, seriesFrom, now)
		if err != nil {
			return PoolView{}, fmt.Errorf("read history: %w", err)
		}
		input.Series = seriesFor(samples, q.Window)
		if first, ok, errFirst := s.store.FirstSampleAt(provider, a.ID); errFirst == nil && ok && (firstSample == nil || first.Before(*firstSample)) {
			firstValue := first
			firstSample = &firstValue
		}
		views = append(views, view)
		inputs = append(inputs, input)
	}
	result := Forecast(Params{Now: now, From: q.From, To: q.To, Burn: q.Burn, Future: q.Future}, inputs)
	result.Coverage.FirstSampleAt = firstSample
	sort.SliceStable(views, func(i, j int) bool { return views[i].Label < views[j].Label })
	return PoolView{Provider: provider, Accounts: views, WeightNote: note, Result: result}, nil
}

func seriesFor(samples []store.Sample, window string) []SeriesPoint {
	out := make([]SeriesPoint, 0, len(samples))
	for _, sample := range samples {
		w := pickWindow(sample, window)
		if w == nil {
			continue
		}
		out = append(out, SeriesPoint{At: sample.At, Remaining: w.Remaining, ResetAt: w.ResetAt})
	}
	return out
}

func pickWindow(sample store.Sample, window string) *store.WindowSample {
	if window == WindowSession {
		return sample.Short
	}
	return sample.Long
}

// HistoryPoint is one sample of one account's window.
type HistoryPoint struct {
	At      time.Time  `json:"at"`
	Percent float64    `json:"percent"`
	ResetAt *time.Time `json:"reset_at"`
	Source  string     `json:"source"`
}

// AccountHistory is the response of GET /quota/accounts/:id/history.
type AccountHistory struct {
	ID       string         `json:"id"`
	Provider string         `json:"provider"`
	Window   string         `json:"window"`
	Samples  []HistoryPoint `json:"samples"`
}

// ErrUnknownAccount means no pool knows the account and it has no history.
var ErrUnknownAccount = fmt.Errorf("unknown account")

// History returns one account's recorded samples in a window, in remaining percent.
func (s *Service) History(id, window string, from, to time.Time) (AccountHistory, error) {
	s.Flush()
	provider := s.providerOf(id)
	if provider == "" {
		return AccountHistory{}, ErrUnknownAccount
	}
	samples, err := s.store.Samples(provider, id, from, to)
	if err != nil {
		return AccountHistory{}, fmt.Errorf("read history: %w", err)
	}
	out := AccountHistory{ID: id, Provider: provider, Window: window, Samples: make([]HistoryPoint, 0, len(samples))}
	for _, sample := range samples {
		w := pickWindow(sample, window)
		if w == nil {
			continue
		}
		point := HistoryPoint{At: sample.At, Percent: round2(w.Remaining), Source: sample.Source}
		if !w.ResetAt.IsZero() {
			at := w.ResetAt
			point.ResetAt = &at
		}
		out.Samples = append(out.Samples, point)
	}
	return out, nil
}

func (s *Service) providerOf(id string) string {
	for _, provider := range router.Providers {
		pool := s.router.Pool(provider)
		if pool == nil {
			continue
		}
		for _, known := range pool.Engine.AccountIDs() {
			if known == id {
				return provider
			}
		}
	}
	for _, provider := range router.Providers {
		if _, ok, err := s.store.FirstSampleAt(provider, id); err == nil && ok {
			return provider
		}
	}
	return ""
}

// ErrRefreshTooSoon is returned when POST /quota/refresh arrives inside the limit.
type ErrRefreshTooSoon struct{ RetryAfter time.Duration }

func (e ErrRefreshTooSoon) Error() string {
	return fmt.Sprintf("quota was refreshed moments ago; try again in %d seconds", int(math.Ceil(e.RetryAfter.Seconds())))
}

// Refresh polls every account now, at most once a minute, whoever asks.
func (s *Service) Refresh() error {
	now := s.now()
	last := s.lastRefresh.Load()
	if last != 0 {
		if wait := time.Unix(last, 0).Add(refreshEvery).Sub(now); wait > 0 {
			return ErrRefreshTooSoon{RetryAfter: wait}
		}
	}
	if !s.lastRefresh.CompareAndSwap(last, now.Unix()) {
		return ErrRefreshTooSoon{RetryAfter: refreshEvery}
	}
	s.router.RequestPollAll()
	return nil
}

func normalizeWindow(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", WindowWeekly:
		return WindowWeekly, true
	case WindowSession:
		return WindowSession, true
	}
	return "", false
}
