package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/quota"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/store"
)

var now = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

type fakeForecast struct {
	mu    sync.Mutex
	pools []quota.PoolView
	err   error
}

func (f *fakeForecast) set(pools ...quota.PoolView) {
	f.mu.Lock()
	f.pools = pools
	f.mu.Unlock()
}

func (f *fakeForecast) Pools(quota.Query) ([]quota.PoolView, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pools, now, f.err
}

type sentMessage struct{ channel, message string }

type recorder struct {
	mu   sync.Mutex
	sent []sentMessage
	err  error
}

func (r *recorder) send(_ context.Context, c Channel, _, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, sentMessage{c.Name, message})
	return r.err
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func setup(t *testing.T) (*Service, *fakeForecast, *recorder, *clock) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "tpr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f, r, clk := &fakeForecast{}, &recorder{}, &clock{t: now}
	s := New(st, f, r.send, clk.now)
	s.settings.Channels = []Channel{{Type: "webhook", URL: "https://hooks.example.com/secret-path", Name: "mine"}}
	return s, f, r, clk
}

func pct(v float64) *float64 { return &v }

func account(id string, remaining float64) quota.AccountView {
	return quota.AccountView{ID: id, Label: id + "@example.com", RemainingPercent: pct(remaining), Status: "ok"}
}

func poolEmptyingIn(d time.Duration, accounts ...quota.AccountView) quota.PoolView {
	at := now.Add(d)
	view := quota.PoolView{Provider: "claude", Accounts: accounts}
	view.PoolEmptyAt = &at
	return view
}

func TestOneConditionNotifiesOnceUntilItClears(t *testing.T) {
	s, f, r, _ := setup(t)
	ctx := context.Background()
	f.set(poolEmptyingIn(2 * time.Hour))
	for i := 0; i < 3; i++ {
		if err := s.Evaluate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if r.count() != 1 {
		t.Fatalf("sent %d notifications, want 1: %+v", r.count(), r.sent)
	}
	if !strings.Contains(r.sent[0].message, "2 h") || !strings.Contains(r.sent[0].message, "Claude") {
		t.Fatalf("message = %q", r.sent[0].message)
	}
	if len(s.View().Active) != 1 {
		t.Fatalf("active = %+v", s.View().Active)
	}

	f.set(quota.PoolView{Provider: "claude"})
	if err := s.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	if len(s.View().Active) != 0 {
		t.Fatalf("still active: %+v", s.View().Active)
	}
	if r.count() != 1 {
		t.Fatal("clearing must not notify")
	}

	f.set(poolEmptyingIn(2 * time.Hour))
	if err := s.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	if r.count() != 2 {
		t.Fatalf("a new occurrence must notify again: %d", r.count())
	}
}

func TestDeDuplicationSurvivesARestart(t *testing.T) {
	s, f, r, clk := setup(t)
	ctx := context.Background()
	f.set(poolEmptyingIn(time.Hour))
	if err := s.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	again := New(s.store, f, r.send, clk.now)
	again.settings.Channels = s.settings.Channels
	if err := again.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	if r.count() != 1 {
		t.Fatalf("a restart repeated the alert: %d", r.count())
	}
}

func TestHoveringAtAThresholdDoesNotFlap(t *testing.T) {
	s, f, r, _ := setup(t)
	ctx := context.Background()
	f.set(quota.PoolView{Provider: "codex", Accounts: []quota.AccountView{account("a", 9.5)}})
	if err := s.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	f.set(quota.PoolView{Provider: "codex", Accounts: []quota.AccountView{account("a", 10.5)}})
	if err := s.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	if len(s.View().Active) != 1 {
		t.Fatal("just above the threshold must not clear yet")
	}
	f.set(quota.PoolView{Provider: "codex", Accounts: []quota.AccountView{account("a", 9.5)}})
	if err := s.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	if r.count() != 1 {
		t.Fatalf("flapping notified %d times", r.count())
	}
	f.set(quota.PoolView{Provider: "codex", Accounts: []quota.AccountView{account("a", 13)}})
	if err := s.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	if len(s.View().Active) != 0 {
		t.Fatal("well above the threshold must clear")
	}
}

func TestConditionsRespectSettings(t *testing.T) {
	soon := poolEmptyingIn(2*time.Hour, account("a", 5))
	off := account("off", 1)
	off.Status = "off"
	unknown := quota.AccountView{ID: "unknown", Status: "unknown"}
	soon.Accounts = append(soon.Accounts, off, unknown)

	base := Settings{Enabled: true, PoolEmptyWithinHours: 6, AccountBelowPercent: 10}
	got := Conditions(now, []quota.PoolView{soon}, base, nil)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	noPool := base
	noPool.PoolEmptyWithinHours = 0
	if got = Conditions(now, []quota.PoolView{soon}, noPool, nil); len(got) != 1 || got[0].Kind != KindAccountLow {
		t.Fatalf("pool alert off: %+v", got)
	}
	noAccount := base
	noAccount.AccountBelowPercent = 0
	if got = Conditions(now, []quota.PoolView{soon}, noAccount, nil); len(got) != 1 || got[0].Kind != KindPoolEmptySoon {
		t.Fatalf("account alert off: %+v", got)
	}
	far := poolEmptyingIn(7 * time.Hour)
	if got = Conditions(now, []quota.PoolView{far}, base, nil); len(got) != 0 {
		t.Fatalf("beyond the horizon: %+v", got)
	}
}

func TestDisabledAlertsClearAndSendNothing(t *testing.T) {
	s, f, r, _ := setup(t)
	ctx := context.Background()
	f.set(poolEmptyingIn(time.Hour))
	if err := s.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	s.settings.Enabled = false
	if err := s.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	if len(s.View().Active) != 0 {
		t.Fatal("disabling must clear active alerts")
	}
	if r.count() != 1 {
		t.Fatalf("sent %d", r.count())
	}
}

func TestResetNoticeIsOneShotAndOptional(t *testing.T) {
	s, f, r, clk := setup(t)
	ctx := context.Background()
	s.resetMark = now
	pool := quota.PoolView{Provider: "codex"}
	clk.add(time.Hour)
	pool.Resets = []quota.Reset{{At: now.Add(30 * time.Minute), DeltaPercent: 80}, {At: now.Add(48 * time.Hour), DeltaPercent: 50, Projected: true}}
	f.set(pool)

	if err := s.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	if r.count() != 0 {
		t.Fatal("reset notices are off by default")
	}

	s.settings.NotifyOnReset = true
	s.resetMark = now
	for i := 0; i < 3; i++ {
		if err := s.Evaluate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if r.count() != 1 || !strings.Contains(r.sent[0].message, "80%") {
		t.Fatalf("sent = %+v", r.sent)
	}
}

func TestFailedDeliveryIsLoggedNotRetried(t *testing.T) {
	s, f, r, _ := setup(t)
	r.err = errors.New("could not reach the channel")
	f.set(poolEmptyingIn(time.Hour))
	for i := 0; i < 2; i++ {
		if err := s.Evaluate(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if r.count() != 1 {
		t.Fatalf("delivery tried %d times", r.count())
	}
	events, err := s.Events(10)
	if err != nil {
		t.Fatal(err)
	}
	var sawFailure, sawAlert bool
	for _, e := range events {
		sawFailure = sawFailure || (e.Kind == KindDeliveryError && strings.Contains(e.Message, "could not reach"))
		sawAlert = sawAlert || e.Kind == KindPoolEmptySoon
	}
	if !sawFailure || !sawAlert {
		t.Fatalf("events = %+v", events)
	}
}

func TestOutgoingMessagesMaskEmailsButTheLogKeepsThem(t *testing.T) {
	s, f, r, _ := setup(t)
	f.set(quota.PoolView{Provider: "claude", Accounts: []quota.AccountView{account("alice", 3)}})
	if err := s.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.sent[0].message, "alice@example.com") || !strings.Contains(r.sent[0].message, "a…@example.com") {
		t.Fatalf("outgoing = %q", r.sent[0].message)
	}
	events, _ := s.Events(1)
	if !strings.Contains(events[0].Message, "alice@example.com") {
		t.Fatalf("event = %+v", events[0])
	}
}

func TestSettingsMaskURLsAndKeepTheSecretOnSave(t *testing.T) {
	s, _, _, _ := setup(t)
	view := s.View()
	if view.Channels[0].URL != "https://hooks.example.com/…" {
		t.Fatalf("masked = %q", view.Channels[0].URL)
	}
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), "secret-path") {
		t.Fatalf("GET leaks the URL: %s", raw)
	}
	next := view.Settings
	next.AccountBelowPercent = 15
	if _, err := s.Update(next); err != nil {
		t.Fatal(err)
	}
	if s.settings.Channels[0].URL != "https://hooks.example.com/secret-path" {
		t.Fatalf("stored URL = %q", s.settings.Channels[0].URL)
	}
	if s.settings.AccountBelowPercent != 15 {
		t.Fatalf("settings = %+v", s.settings)
	}

	// Two channels on one host keep their own URLs.
	if _, err := s.Update(Settings{Enabled: true, Channels: []Channel{
		{Type: "webhook", URL: "https://hooks.example.com/one", Name: "one"},
		{Type: "webhook", URL: "https://hooks.example.com/two", Name: "two"},
	}}); err != nil {
		t.Fatal(err)
	}
	masked := s.View().Settings
	masked.Channels = []Channel{masked.Channels[0], masked.Channels[1]}
	if _, err := s.Update(masked); err != nil {
		t.Fatal(err)
	}
	if s.settings.Channels[0].URL != "https://hooks.example.com/one" || s.settings.Channels[1].URL != "https://hooks.example.com/two" {
		t.Fatalf("channels = %+v", s.settings.Channels)
	}

	// Settings persist.
	again := New(s.store, &fakeForecast{}, nil, nil)
	if len(again.settings.Channels) != 2 || again.settings.Channels[1].URL != "https://hooks.example.com/two" {
		t.Fatalf("after restart = %+v", again.settings)
	}
}

func TestInvalidSettingsAreRejected(t *testing.T) {
	s, _, _, _ := setup(t)
	bad := []Settings{
		{PoolEmptyWithinHours: -1},
		{PoolEmptyWithinHours: 1000},
		{AccountBelowPercent: 101},
		{Channels: []Channel{{Type: "pager", URL: "https://example.com/x"}}},
		{Channels: []Channel{{Type: "ntfy", URL: "not a url"}}},
		{Channels: []Channel{{Type: "ntfy", URL: "ftp://example.com/x"}}},
		{Channels: []Channel{{Type: "ntfy", URL: "https://gone.example.com/…"}}},
		{Channels: make([]Channel, maxChannels+1)},
	}
	for i, settings := range bad {
		if _, err := s.Update(settings); !errors.Is(err, ErrInvalidSettings) {
			t.Errorf("case %d: err = %v", i, err)
		}
	}
}

func TestSendTestReportsEachChannel(t *testing.T) {
	s, _, r, _ := setup(t)
	if results := s.SendTest(context.Background()); len(results) != 1 || !results[0].OK || results[0].Name != "mine" {
		t.Fatalf("results = %+v", results)
	}
	r.err = errors.New("the channel answered HTTP 500")
	results := s.SendTest(context.Background())
	if results[0].OK || !strings.Contains(results[0].Error, "500") {
		t.Fatalf("results = %+v", results)
	}
}

func TestDeliverShapesEachServicesPayloadAndHidesTheURLInErrors(t *testing.T) {
	var mu sync.Mutex
	got := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		got[strings.TrimPrefix(r.URL.Path, "/")] = r.Header.Get("Content-Type") + "|" + r.Header.Get("Title") + "|" + string(raw)
		mu.Unlock()
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer server.Close()
	ctx := context.Background()
	for _, channelType := range []string{"ntfy", "discord", "slack", "webhook"} {
		if err := deliver(ctx, Channel{Type: channelType, URL: server.URL + "/" + channelType}, "T", "hello"); err != nil {
			t.Fatalf("%s: %v", channelType, err)
		}
	}
	if !strings.Contains(got["ntfy"], "text/plain") || !strings.Contains(got["ntfy"], "|T|hello") {
		t.Fatalf("ntfy = %q", got["ntfy"])
	}
	if !strings.Contains(got["discord"], `"content":"T: hello"`) {
		t.Fatalf("discord = %q", got["discord"])
	}
	if !strings.Contains(got["slack"], `"text":"T: hello"`) {
		t.Fatalf("slack = %q", got["slack"])
	}
	if !strings.Contains(got["webhook"], `"message":"hello"`) {
		t.Fatalf("webhook = %q", got["webhook"])
	}
	err := deliver(ctx, Channel{Type: "webhook", URL: server.URL + "/fail"}, "T", "m")
	if err == nil || !strings.Contains(err.Error(), "418") {
		t.Fatalf("err = %v", err)
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL + "/secret-topic"
	dead.Close()
	err = deliver(ctx, Channel{Type: "ntfy", URL: deadURL}, "T", "m")
	if err == nil || strings.Contains(err.Error(), "secret-topic") || strings.Contains(err.Error(), "http") {
		t.Fatalf("err = %v", err)
	}
	if err = deliver(ctx, Channel{Type: "pager", URL: server.URL}, "T", "m"); err == nil {
		t.Fatal("an unknown type must fail")
	}
}

func TestMaskEmails(t *testing.T) {
	cases := map[string]string{
		"account alice@example.com left":        "account a…@example.com left",
		"no address here":                       "no address here",
		"two b.c+d@example.org e@example.co.uk": "two b…@example.org e…@example.co.uk",
	}
	for in, want := range cases {
		if got := MaskEmails(in); got != want {
			t.Errorf("MaskEmails(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAPIContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	registerRoutes(e.Group("/tpr"))
	do := func(method, path, body string) (int, map[string]any) {
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: %q", path, w.Body.String())
		}
		return w.Code, out
	}
	current.Store(nil)
	if code, _ := do(http.MethodGet, "/tpr/alerts", ""); code != 503 {
		t.Fatalf("not started: %d", code)
	}

	s, f, _, _ := setup(t)
	current.Store(s)
	t.Cleanup(func() { current.Store(nil) })
	f.set(poolEmptyingIn(time.Hour))
	if err := s.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	code, out := do(http.MethodGet, "/tpr/alerts", "")
	if code != 200 || len(out["active"].([]any)) != 1 {
		t.Fatalf("get: %d %v", code, out)
	}
	for _, key := range []string{"enabled", "pool_empty_within_hours", "account_below_percent", "notify_on_reset", "channels"} {
		if _, ok := out[key]; !ok {
			t.Errorf("settings are missing %q", key)
		}
	}
	if strings.Contains(out["channels"].([]any)[0].(map[string]any)["url"].(string), "secret") {
		t.Fatalf("GET leaks a URL: %v", out)
	}
	code, out = do(http.MethodPut, "/tpr/alerts", `{"enabled":true,"pool_empty_within_hours":3,"account_below_percent":20,"channels":[{"type":"slack","url":"https://hooks.example.com/abc","name":"s"}]}`)
	if code != 200 || out["pool_empty_within_hours"].(float64) != 3 {
		t.Fatalf("put: %d %v", code, out)
	}
	if code, _ = do(http.MethodPut, "/tpr/alerts", `{"account_below_percent":500}`); code != 400 {
		t.Fatalf("bad put: %d", code)
	}
	if code, _ = do(http.MethodPut, "/tpr/alerts", `{nope`); code != 400 {
		t.Fatalf("bad JSON: %d", code)
	}
	code, out = do(http.MethodPost, "/tpr/alerts/test", "")
	if code != 200 || len(out["results"].([]any)) != 1 {
		t.Fatalf("test: %d %v", code, out)
	}
	result := out["results"].([]any)[0].(map[string]any)
	for _, key := range []string{"name", "ok", "error"} {
		if _, ok := result[key]; !ok {
			t.Errorf("result is missing %q", key)
		}
	}
	code, out = do(http.MethodGet, "/tpr/alerts/events?limit=1", "")
	if code != 200 || len(out["events"].([]any)) != 1 {
		t.Fatalf("events: %d %v", code, out)
	}
	event := out["events"].([]any)[0].(map[string]any)
	for _, key := range []string{"at", "kind", "provider", "account", "message"} {
		if _, ok := event[key]; !ok {
			t.Errorf("event is missing %q", key)
		}
	}
}
