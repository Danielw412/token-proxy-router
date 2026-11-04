package quota

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router/engine"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/store"
)

// clock is a settable test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

type stubHost struct{ router.Host }

func (stubHost) Accounts() []engine.HostAccount { return nil }

func newService(t *testing.T) (*Service, *clock, *router.Router) {
	t.Helper()
	t.Setenv(home.EnvHome, t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "tpr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := &clock{t: now}
	r := router.NewWithClock(stubHost{}, clk.now)
	r.Pool("claude").Engine.ObserveAccounts([]engine.HostAccount{
		{ID: "claude-a", Provider: "claude", Email: "alice@example.com", Plan: "max5"},
		{ID: "claude-b", Provider: "claude", Email: "bob@example.com", Plan: "max20"},
		{ID: "claude-off", Provider: "claude", Email: "carol@example.com"},
	})
	r.Pool("codex").Engine.ObserveAccounts([]engine.HostAccount{
		{ID: "codex-a", Provider: "codex", Email: "dave@example.com", Plan: "plus"},
	})
	settings, _ := r.Pool("claude").Engine.Settings()
	settings.DisabledAccounts = []string{"claude-off"}
	settings, _ = settings.NormalizeFor("claude", nil)
	r.Pool("claude").Engine.Configure(settings, nil)
	return New(st, r, clk.now), clk, r
}

func window(used float64, reset time.Time, minutes int, at time.Time) *engine.Window {
	return &engine.Window{UsedPercent: used, WindowMinutes: minutes, ResetAt: reset, ObservedAt: at}
}

func reading(at time.Time, shortUsed, longUsed float64, longReset time.Time) engine.Quota {
	return engine.Quota{
		ObservedAt: at, Source: "response",
		Short: window(shortUsed, now.Add(5*time.Hour), 300, at),
		Long:  window(longUsed, longReset, 10080, at),
	}
}

func TestCollectorBoundsSamplesForBusyAndQuietAccounts(t *testing.T) {
	s, _, _ := newService(t)
	reset := now.Add(week)
	// A busy account reporting every 10 seconds is recorded once a minute.
	for i := 0; i < 30; i++ {
		s.Observe("claude", "claude-a", reading(now.Add(time.Duration(i)*10*time.Second), float64(i), float64(i), reset))
	}
	s.Flush()
	busy, err := s.store.Samples("claude", "claude-a", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(busy) < 4 || len(busy) > 6 {
		t.Fatalf("a five-minute burst recorded %d samples", len(busy))
	}
	// A quiet account with unchanged numbers gets only a heartbeat.
	for i := 0; i < 60; i++ {
		s.Observe("claude", "claude-b", reading(now.Add(time.Duration(i)*time.Minute), 1, 1, reset))
	}
	s.Flush()
	quiet, err := s.store.Samples("claude", "claude-b", now.Add(-time.Hour), now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(quiet) != 6 {
		t.Fatalf("an hour unchanged recorded %d samples, want one per 10 minutes", len(quiet))
	}
	// An out-of-order reading is dropped.
	s.Observe("claude", "claude-b", reading(now.Add(-2*time.Hour), 50, 50, reset))
	s.Flush()
	again, _ := s.store.Samples("claude", "claude-b", now.Add(-2*time.Hour), now.Add(2*time.Hour))
	if len(again) != len(quiet) {
		t.Fatalf("an older reading was recorded: %d vs %d", len(again), len(quiet))
	}
	// A reading with no windows is nothing to record.
	s.Observe("codex", "codex-a", engine.Quota{ObservedAt: now})
	s.Flush()
	if none, _ := s.store.Samples("codex", "codex-a", now.Add(-time.Hour), now.Add(time.Hour)); len(none) != 0 {
		t.Fatalf("recorded %d samples from an empty reading", len(none))
	}
}

func TestPoolsCombineHistoryWeightsAndTheRouterCurrentView(t *testing.T) {
	s, _, r := newService(t)
	reset := now.Add(2 * time.Hour)
	engineA := r.Pool("claude").Engine
	// Both accounts report a window; A burned 10 points in the last hour.
	s.Observe("claude", "claude-a", reading(now.Add(-time.Hour), 5, 40, reset))
	s.Observe("claude", "claude-a", reading(now, 5, 50, reset))
	s.Observe("claude", "claude-b", reading(now.Add(-time.Hour), 5, 90, reset))
	engineA.ApplyPolledQuota("claude-a", reading(now, 5, 50, reset), nil)
	engineA.ApplyPolledQuota("claude-b", reading(now, 5, 90, reset), nil)

	pools, at, err := s.Pools(Query{From: now.Add(-day), To: now.Add(day), Burn: 3 * time.Hour, Window: WindowWeekly, Future: FutureSame})
	if err != nil {
		t.Fatal(err)
	}
	if !at.Equal(now) {
		t.Fatalf("now = %v", at)
	}
	if len(pools) != 2 || pools[0].Provider != "claude" || pools[1].Provider != "codex" {
		t.Fatalf("pools = %+v", pools)
	}
	claude := pools[0]
	if claude.WeightNote != "Max 20x = 4x" {
		t.Fatalf("weight note = %q", claude.WeightNote)
	}
	near(t, "capacity", claude.CapacityPercent, 100+400)
	near(t, "remaining", claude.RemainingPercent, 50+4*10)
	near(t, "burn", claude.BurnPerHour, 10)
	if len(claude.Accounts) != 3 {
		t.Fatalf("every account is listed, counted or not: %+v", claude.Accounts)
	}
	var off AccountView
	for _, a := range claude.Accounts {
		if a.ID == "claude-off" {
			off = a
		}
	}
	if off.Status != "off" || off.RemainingPercent != nil {
		t.Fatalf("off account = %+v", off)
	}
	if len(claude.Recorded) != 2 {
		t.Fatalf("recorded = %+v", claude.Recorded)
	}
	if claude.Coverage.Samples != 3 || claude.Coverage.FirstSampleAt == nil {
		t.Fatalf("coverage = %+v", claude.Coverage)
	}
	if !claude.Projected[0].At.Equal(now) {
		t.Fatalf("projected must start at now: %+v", claude.Projected[0])
	}

	if pools[1].CapacityPercent != 0 || pools[1].Coverage.Samples != 0 {
		t.Fatalf("a pool with no data must be empty: %+v", pools[1])
	}
}

func TestSessionWindowUsesTheShortWindow(t *testing.T) {
	s, _, r := newService(t)
	engineA := r.Pool("claude").Engine
	s.Observe("claude", "claude-a", reading(now.Add(-time.Hour), 40, 10, now.Add(week)))
	s.Observe("claude", "claude-a", reading(now, 60, 10, now.Add(week)))
	engineA.ApplyPolledQuota("claude-a", reading(now, 60, 10, now.Add(week)), nil)
	pools, _, err := s.Pools(Query{From: now.Add(-day), To: now.Add(day), Burn: 3 * time.Hour, Window: WindowSession, Future: FutureIdle})
	if err != nil {
		t.Fatal(err)
	}
	near(t, "session remaining", pools[0].RemainingPercent, 40)
	near(t, "session burn", pools[0].BurnPerHour, 20)
}

func TestWeeklyPoolsAlsoCarryTheFiveHourWindow(t *testing.T) {
	s, _, r := newService(t)
	engineA := r.Pool("claude").Engine
	// A (weight 1) has 70% of its 5-hour window left, B (weight 4) has 20%.
	engineA.ApplyPolledQuota("claude-a", reading(now, 30, 10, now.Add(week)), nil)
	engineA.ApplyPolledQuota("claude-b", reading(now, 80, 10, now.Add(week)), nil)
	pools, _, err := s.Pools(Query{From: now.Add(-day), To: now.Add(day), Burn: 3 * time.Hour, Window: WindowWeekly, Future: FutureIdle})
	if err != nil {
		t.Fatal(err)
	}
	claude := pools[0]
	near(t, "weekly remaining", claude.RemainingPercent, 90+4*90)
	near(t, "5-hour remaining", claude.FiveHourRemainingPercent, 70+4*20)
	near(t, "5-hour capacity", claude.FiveHourCapacityPercent, 100+400)
	for _, a := range claude.Accounts {
		switch a.ID {
		case "claude-a":
			if a.FiveHourRemainingPercent == nil || *a.FiveHourRemainingPercent != 70 || a.FiveHourResetAt == nil {
				t.Fatalf("account A 5-hour = %+v", a)
			}
			if a.RemainingPercent == nil || *a.RemainingPercent != 90 {
				t.Fatalf("account A weekly = %+v", a)
			}
		case "claude-off":
			if a.FiveHourRemainingPercent != nil {
				t.Fatalf("an account with no reading has no 5-hour figure: %+v", a)
			}
		}
	}
	if pools[1].FiveHourCapacityPercent != 0 {
		t.Fatalf("a pool with no data has no 5-hour capacity: %+v", pools[1])
	}
}

func TestHistoryAndUnknownAccount(t *testing.T) {
	s, _, _ := newService(t)
	s.Observe("claude", "claude-a", reading(now.Add(-time.Hour), 5, 40, now.Add(week)))
	got, err := s.History("claude-a", WindowWeekly, now.Add(-day), now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "claude" || len(got.Samples) != 1 || got.Samples[0].Percent != 60 || got.Samples[0].Source != "response" {
		t.Fatalf("history = %+v", got)
	}
	session, err := s.History("claude-a", WindowSession, now.Add(-day), now)
	if err != nil || len(session.Samples) != 1 || session.Samples[0].Percent != 95 {
		t.Fatalf("session history = %+v err=%v", session, err)
	}
	if _, err = s.History("nobody", WindowWeekly, now.Add(-day), now); err != ErrUnknownAccount {
		t.Fatalf("err = %v", err)
	}
	// An account that left the roster keeps its history.
	if err = s.store.AppendSamples([]store.SampleRecord{{Provider: "codex", Account: "gone", Sample: store.Sample{At: now.Add(-time.Hour), Long: &store.WindowSample{Remaining: 5}}}}); err != nil {
		t.Fatal(err)
	}
	if gone, errGone := s.History("gone", WindowWeekly, now.Add(-day), now); errGone != nil || gone.Provider != "codex" || len(gone.Samples) != 1 {
		t.Fatalf("gone = %+v err=%v", gone, errGone)
	}
}

func TestRefreshIsRateLimitedByTheServer(t *testing.T) {
	s, clk, _ := newService(t)
	if err := s.Refresh(); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(); err == nil {
		t.Fatal("a second refresh within the minute must be refused")
	}
	clk.set(now.Add(2 * time.Minute))
	if err := s.Refresh(); err != nil {
		t.Fatal(err)
	}
}

func TestPlanWeights(t *testing.T) {
	cases := []struct {
		provider string
		plans    []string
		want     map[string]float64
		note     string
	}{
		{"claude", []string{"max5", "max5"}, map[string]float64{"max5": 1}, ""},
		{"claude", []string{"pro", "max20"}, map[string]float64{"pro": 1, "max20": 20}, "Max 20x = 20x"},
		{"claude", []string{"max5", "max20", ""}, map[string]float64{"max5": 1, "max20": 4, "": 1}, "Max 20x = 4x"},
		{"claude", []string{"", ""}, map[string]float64{"": 1}, ""},
		{"codex", []string{"plus", "pro"}, map[string]float64{"plus": 1, "pro": 20}, "Pro = 20x"},
		{"codex", []string{"plus", "prolite", "free"}, map[string]float64{"free": 1, "plus": 5, "prolite": 25}, "Plus = 5x, Pro Lite = 25x"},
		{"codex", []string{"mystery"}, map[string]float64{"mystery": 1}, ""},
	}
	for _, c := range cases {
		got, note := planWeights(c.provider, c.plans)
		for plan, want := range c.want {
			if got[plan] != want {
				t.Errorf("%v: weight[%q] = %v, want %v", c.plans, plan, got[plan], want)
			}
		}
		if note != c.note {
			t.Errorf("%v: note = %q, want %q", c.plans, note, c.note)
		}
	}
}

func apiCall(t *testing.T, e *gin.Engine, method, path string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: %q", path, w.Body.String())
	}
	return w.Code, out
}

func TestAPIContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	registerRoutes(e.Group("/tpr"))
	current.Store(nil)
	if code, out := apiCall(t, e, http.MethodGet, "/tpr/quota/pools"); code != 503 || out["error"] == nil {
		t.Fatalf("not started: %d %v", code, out)
	}

	s, _, r := newService(t)
	current.Store(s)
	t.Cleanup(func() { current.Store(nil) })
	s.Observe("claude", "claude-a", reading(now.Add(-time.Hour), 5, 40, now.Add(week)))
	r.Pool("claude").Engine.ApplyPolledQuota("claude-a", reading(now, 5, 50, now.Add(week)), nil)

	code, out := apiCall(t, e, http.MethodGet, "/tpr/quota/pools?burn=1h&window=session&future=double")
	if code != 200 {
		t.Fatalf("pools: %d %v", code, out)
	}
	for _, key := range []string{"now", "from", "to", "pools"} {
		if _, ok := out[key]; !ok {
			t.Errorf("response is missing %q", key)
		}
	}
	pool := out["pools"].([]any)[0].(map[string]any)
	for _, key := range []string{"provider", "accounts", "remaining_percent", "capacity_percent", "weight_note", "pool_empty_at", "first_empty", "next_reset", "burn_per_hour", "recorded", "projected", "resets", "empty_marks", "coverage"} {
		if _, ok := pool[key]; !ok {
			t.Errorf("pool is missing %q", key)
		}
	}
	account := pool["accounts"].([]any)[0].(map[string]any)
	for _, key := range []string{"id", "label", "email", "plan", "weight", "remaining_percent", "reset_at", "status"} {
		if _, ok := account[key]; !ok {
			t.Errorf("account is missing %q", key)
		}
	}
	raw, _ := json.Marshal(out)
	for _, word := range []string{"token", "secret"} {
		if strings.Contains(strings.ToLower(string(raw)), word) {
			t.Errorf("response mentions %q: %s", word, raw)
		}
	}

	for _, query := range []string{"burn=2h", "window=monthly", "future=maybe", "from=yesterday", "to=soon",
		"from=2026-10-26T12:00:00Z&to=2026-10-26T11:00:00Z", "from=2020-01-01T00:00:00Z&to=2026-01-01T00:00:00Z"} {
		if code, _ = apiCall(t, e, http.MethodGet, "/tpr/quota/pools?"+query); code != 400 {
			t.Errorf("%s: %d", query, code)
		}
	}

	code, out = apiCall(t, e, http.MethodGet, "/tpr/quota/accounts/claude-a/history?window=weekly&from=2026-10-25T00:00:00Z")
	if code != 200 || len(out["samples"].([]any)) != 1 {
		t.Fatalf("history: %d %v", code, out)
	}
	if code, _ = apiCall(t, e, http.MethodGet, "/tpr/quota/accounts/nobody/history"); code != 404 {
		t.Fatalf("unknown: %d", code)
	}
	if code, _ = apiCall(t, e, http.MethodGet, "/tpr/quota/accounts/claude-a"); code != 404 {
		t.Fatalf("no suffix: %d", code)
	}
	if code, _ = apiCall(t, e, http.MethodGet, "/tpr/quota/accounts/claude-a/history?window=bad"); code != 400 {
		t.Fatalf("bad window: %d", code)
	}
	if err := s.store.AppendSamples([]store.SampleRecord{{Provider: "codex", Account: "sub/dir.json", Sample: store.Sample{At: now.Add(-time.Hour), Long: &store.WindowSample{Remaining: 5}}}}); err != nil {
		t.Fatal(err)
	}
	if code, out = apiCall(t, e, http.MethodGet, "/tpr/quota/accounts/sub/dir.json/history"); code != 200 || out["id"] != "sub/dir.json" {
		t.Fatalf("slash in ID: %d %v", code, out)
	}

	if code, out = apiCall(t, e, http.MethodPost, "/tpr/quota/refresh"); code != 202 || out["requested"] != true {
		t.Fatalf("refresh: %d %v", code, out)
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/tpr/quota/refresh", nil))
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("second refresh: %d %v", w.Code, w.Header())
	}
}

// Filtering uses the same forecast as a whole pool, so a reset adds back only what the
// chosen account had used, not a full account's worth.
func TestPoolsFilterToChosenAccounts(t *testing.T) {
	s, _, r := newService(t)
	reset := now.Add(2 * time.Hour)
	engineA := r.Pool("claude").Engine
	s.Observe("claude", "claude-a", reading(now, 5, 10, reset))
	s.Observe("claude", "claude-b", reading(now, 5, 90, reset))
	engineA.ApplyPolledQuota("claude-a", reading(now, 5, 10, reset), nil)
	engineA.ApplyPolledQuota("claude-b", reading(now, 5, 90, reset), nil)

	query := Query{From: now.Add(-day), To: now.Add(day), Burn: 3 * time.Hour, Window: WindowWeekly, Future: FutureIdle, Accounts: map[string]bool{"claude-a": true}}
	pools, _, err := s.Pools(query)
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 1 || pools[0].Provider != "claude" {
		t.Fatalf("a pool with none of the chosen accounts must be left out: %+v", pools)
	}
	pool := pools[0]
	if len(pool.Accounts) != 1 || pool.Accounts[0].ID != "claude-a" {
		t.Fatalf("accounts = %+v", pool.Accounts)
	}
	if pool.RemainingPercent != 90 || pool.CapacityPercent != 100 {
		t.Fatalf("remaining = %v of %v", pool.RemainingPercent, pool.CapacityPercent)
	}
	for _, point := range pool.Projected {
		if point.Percent > pool.CapacityPercent {
			t.Fatalf("forecast went above capacity: %+v", point)
		}
	}

	if _, _, err := s.Pools(Query{From: now.Add(-day), To: now.Add(day), Burn: 3 * time.Hour, Window: WindowWeekly, Future: FutureIdle, Accounts: map[string]bool{"nobody": true}}); err != nil {
		t.Fatalf("an unknown account is not an error: %v", err)
	}
}
