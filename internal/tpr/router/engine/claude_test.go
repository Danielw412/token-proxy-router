package engine

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func claudeHeaders(shortUtil, longUtil string, shortReset, longReset time.Time) map[string][]string {
	return map[string][]string{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {shortUtil},
		"anthropic-ratelimit-unified-5h-reset":       {strconv.FormatInt(shortReset.Unix(), 10)},
		"Anthropic-Ratelimit-Unified-5h-Status":      {"allowed"},
		"Anthropic-Ratelimit-Unified-7d-Utilization": {longUtil},
		"Anthropic-Ratelimit-Unified-7d-Reset":       {longReset.Format(time.RFC3339)},
		"Anthropic-Ratelimit-Unified-7d-Status":      {"allowed"},
	}
}

func TestClaudeHeadersReadBothWindowsAsPercent(t *testing.T) {
	short, long := t0.Add(2*time.Hour), t0.Add(72*time.Hour)
	q, ok := ParseClaudeQuotaHeaders(claudeHeaders("0.25", "0.5", short, long), t0)
	if !ok {
		t.Fatal("headers not recognized")
	}
	if q.Short == nil || q.Short.UsedPercent != 25 || !q.Short.ResetAt.Equal(short) || q.Short.WindowMinutes != 300 {
		t.Fatalf("short window = %+v", q.Short)
	}
	if q.Long == nil || q.Long.UsedPercent != 50 || !q.Long.ResetAt.Equal(long) || q.Long.WindowMinutes != 10080 {
		t.Fatalf("long window = %+v", q.Long)
	}
	if q.LimitReached {
		t.Fatal("allowed windows must not count as limit reached")
	}
}

func TestClaudeRejectedWindowMeansSpentWhateverUtilizationSays(t *testing.T) {
	h := claudeHeaders("0.97", "0.2", t0.Add(time.Hour), t0.Add(time.Hour*48))
	h["Anthropic-Ratelimit-Unified-5h-Status"] = []string{"rejected"}
	q, ok := ParseClaudeQuotaHeaders(h, t0)
	if !ok || !q.LimitReached || q.Short.UsedPercent != 100 {
		t.Fatalf("q = %+v ok=%v", q, ok)
	}
}

func TestClaudeHeadersIgnoreCodexAndUnrelatedHeaders(t *testing.T) {
	if _, ok := ParseClaudeQuotaHeaders(map[string][]string{"x-codex-primary-used-percent": {"10"}, "Retry-After": {"5"}}, t0); ok {
		t.Fatal("unrelated headers must not produce a reading")
	}
	if _, ok := ParseClaudeQuotaHeaders(nil, t0); ok {
		t.Fatal("no headers must not produce a reading")
	}
}

func TestClaudeUsageResponseUsesPercentAndNullResetMeansNotStarted(t *testing.T) {
	body := `{"five_hour":{"utilization":0,"resets_at":null},"seven_day":{"utilization":42.5,"resets_at":"2026-11-05T10:00:00+00:00"},
		"seven_day_opus":{"utilization":9,"resets_at":null},"extra_usage":{"is_enabled":false}}`
	q, ok := ParseClaudeUsageResponse([]byte(body), t0)
	if !ok {
		t.Fatal("usage body not recognized")
	}
	if q.Short == nil || !q.Short.NotStarted || q.Short.UsedPercent != 0 {
		t.Fatalf("short = %+v", q.Short)
	}
	if q.Long == nil || q.Long.UsedPercent != 42.5 || q.Long.ResetAt.IsZero() || q.Long.NotStarted {
		t.Fatalf("long = %+v", q.Long)
	}
	for _, bad := range []string{`{}`, `not json`, `{"seven_day_opus":{"utilization":1}}`} {
		if _, ok := ParseClaudeUsageResponse([]byte(bad), t0); ok {
			t.Fatalf("%q must not produce a reading", bad)
		}
	}
}

func TestClaudeQuotaErrorSeparatesExhaustionFromPerMinuteLimits(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"account limit", 429, `{"error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit. Please try again later."}}`, true},
		{"usage limit", 429, `{"error":{"message":"You've hit your usage limit"}}`, true},
		{"per minute", 429, `{"error":{"type":"rate_limit_error","message":"This request would exceed the requests per minute rate limit. Please try again later."}}`, false},
		{"server error", 500, `{"error":{"message":"usage limit"}}`, false},
		{"plain 429", 429, `{"error":{"message":"slow down"}}`, false},
	}
	for _, c := range cases {
		if _, ok := ParseClaudeQuotaError(c.status, c.body, t0); ok != c.want {
			t.Errorf("%s: ok=%v want %v", c.name, ok, c.want)
		}
	}
}

func TestClaudeProfilePlan(t *testing.T) {
	cases := map[string]string{
		`{"organization":{"rate_limit_tier":"default_claude_max_20x"},"account":{"has_claude_max":true}}`: "max20",
		`{"organization":{"rate_limit_tier":"default_claude_max_5x"}}`:                                    "max5",
		`{"account":{"has_claude_max":true}}`:                                                             "max",
		`{"account":{"has_claude_max":false,"has_claude_pro":true}}`:                                      "pro",
		`{"account":{"has_claude_max":false,"has_claude_pro":false}}`:                                     "free",
		`{"organization":{"organization_type":"claude_team","subscription_status":"active"}}`:             "team",
		`{}`:   "",
		`nope`: "",
	}
	for body, want := range cases {
		if got := ParseClaudeProfilePlan([]byte(body)); got != want {
			t.Errorf("%s: got %q want %q", body, got, want)
		}
	}
}

// newClaudeEngine builds a Claude pool with two accounts.
func newClaudeEngine(t *testing.T) (*Engine, *fakeClock) {
	t.Helper()
	s, warn := DefaultSettingsFor(ProviderClaude).NormalizeFor(ProviderClaude, nil)
	if len(warn) > 0 {
		t.Fatalf("warnings: %v", warn)
	}
	clk := &fakeClock{t: t0}
	e := NewForProvider(ProviderClaude, s, nil, clk.now)
	e.ObserveAccounts([]HostAccount{
		{ID: "claude-alice.json", AuthIndex: "1", Provider: "claude", Email: "alice@example.com"},
		{ID: "claude-bob.json", AuthIndex: "2", Provider: "claude", Email: "bob@example.com"},
		{ID: "codex-carol.json", AuthIndex: "3", Provider: "codex", Email: "carol@example.com"},
	})
	return e, clk
}

func claudePick(e *Engine, headers map[string][]string, meta map[string]any) PickResult {
	return e.Pick(PickRequest{
		Candidates: []Candidate{{ID: "claude-alice.json", Provider: "claude"}, {ID: "claude-bob.json", Provider: "claude"}},
		Headers:    headers,
		Metadata:   meta,
		Model:      "claude-sonnet-5-5",
	})
}

func TestClaudePoolKeepsASessionOnItsAccountAndIgnoresOtherProviders(t *testing.T) {
	e, _ := newClaudeEngine(t)
	if got := e.View(0).Accounts; len(got) != 2 {
		t.Fatalf("pool must hold only its own provider's accounts: %+v", got)
	}
	session := map[string][]string{"X-Claude-Code-Session-Id": {"s1"}}
	first := claudePick(e, session, nil)
	if !first.Handled || first.AuthID == "" {
		t.Fatalf("first pick = %+v", first)
	}
	sub := map[string][]string{"X-Claude-Code-Session-Id": {"s1"}, "X-Claude-Code-Agent-Id": {"helper"}}
	if got := claudePick(e, sub, nil); got.AuthID != first.AuthID {
		t.Fatalf("subagent went to %s, parent is on %s", got.AuthID, first.AuthID)
	}
	if got := e.Pick(PickRequest{Candidates: []Candidate{{ID: "codex-carol.json", Provider: "codex"}}}); got.Handled {
		t.Fatalf("codex candidates must be left to upstream: %+v", got)
	}
}

func TestClaudeBodySessionIdentityArrivesThroughManagerMetadata(t *testing.T) {
	e, _ := newClaudeEngine(t)
	meta := map[string]any{"canonical_session_id": "claude:from-user-id"}
	first := claudePick(e, nil, meta)
	second := claudePick(e, nil, map[string]any{"canonical_session_id": "claude:from-user-id"})
	if !first.Handled || second.AuthID != first.AuthID {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if got := len(e.View(0).Accounts); got != 2 {
		t.Fatalf("accounts = %d", got)
	}
	if v := e.View(0); v.Sessions != 1 {
		t.Fatalf("sessions = %d, want one family", v.Sessions)
	}
	// A subagent the manager linked through parent_session_id joins its family.
	sub := claudePick(e, nil, map[string]any{"canonical_session_id": "claude:child", "parent_session_id": "claude:from-user-id"})
	if sub.AuthID != first.AuthID {
		t.Fatalf("sub=%+v first=%+v", sub, first)
	}
	if v := e.View(0); v.Sessions != 1 {
		t.Fatalf("sessions = %d, subagent must join the family", v.Sessions)
	}
}

func TestClaudeExhaustionHeaderSwitchesCurrentAccount(t *testing.T) {
	e, clk := newClaudeEngine(t)
	alice := "claude-alice.json"
	bob := "claude-bob.json"
	first := claudePick(e, nil, nil)
	if first.AuthID != alice {
		t.Fatalf("first pick = %s", first.AuthID)
	}
	h := claudeHeaders("1", "0.2", clk.now().Add(time.Hour), clk.now().Add(48*time.Hour))
	h["Anthropic-Ratelimit-Unified-5h-Status"] = []string{"rejected"}
	e.ObserveUsage(UsageObservation{AuthID: alice, Provider: "claude", Headers: h, At: clk.now(), Failed: true, Status: 429})
	if got := claudePick(e, nil, nil); got.AuthID != bob {
		t.Fatalf("after exhaustion pick = %+v", got)
	}
	var found bool
	for _, entry := range e.View(0).History {
		if entry.Kind == "switch" && entry.To == bob {
			found = true
		}
	}
	if !found {
		t.Fatalf("no switch in history: %+v", e.View(0).History)
	}
	// Past the reset, the window counts as full again and the block lifts.
	clk.add(2 * time.Hour)
	e.Tick()
	if q, ok := e.QuotaOf(alice); !ok {
		t.Fatal("quota missing")
	} else if r, _ := q.MinRemaining(clk.now()); r < 50 {
		t.Fatalf("remaining after reset = %v", r)
	}
}

func TestClaudeLimitRejectedFlagBlocksEvenWithoutWindowStatus(t *testing.T) {
	e, clk := newClaudeEngine(t)
	h := claudeHeaders("0.4", "0.4", clk.now().Add(time.Hour), clk.now().Add(48*time.Hour))
	e.ObserveUsage(UsageObservation{AuthID: "claude-alice.json", Provider: "claude", Headers: h, At: clk.now(), LimitRejected: true})
	v := e.View(0)
	for _, a := range v.Accounts {
		if a.ID == "claude-alice.json" && a.State != "exhausted" {
			t.Fatalf("state = %s (%s)", a.State, a.Detail)
		}
	}
}

func TestClaudeWindowStateIdleUntilAResetIsReported(t *testing.T) {
	e, clk := newClaudeEngine(t)
	id := "claude-alice.json"
	if state, _ := e.WindowState(id); state != WindowUnknown {
		t.Fatalf("no data: %s", state)
	}
	body := `{"five_hour":{"utilization":0,"resets_at":null},"seven_day":{"utilization":10,"resets_at":"2026-11-05T10:00:00Z"}}`
	q, ok := ParseClaudeUsageResponse([]byte(body), clk.now())
	if !ok {
		t.Fatal("parse")
	}
	e.ApplyPolledQuota(id, q, nil)
	if state, _ := e.WindowState(id); state != WindowIdle {
		t.Fatalf("null reset: %s", state)
	}
	reset := clk.now().Add(4 * time.Hour)
	e.ObserveUsage(UsageObservation{AuthID: id, Provider: "claude", At: clk.now(),
		Headers: claudeHeaders("0.01", "0.1", reset, clk.now().Add(48*time.Hour))})
	state, at := e.WindowState(id)
	if state != WindowRunning || !at.Equal(reset.Truncate(time.Second)) {
		t.Fatalf("after traffic: %s %s", state, at)
	}
}

func TestClaudePoolRejectionNamesTheProvider(t *testing.T) {
	s := DefaultSettingsFor(ProviderClaude)
	s.DisabledAccounts = []string{"claude-alice.json", "claude-bob.json"}
	s, _ = s.NormalizeFor(ProviderClaude, nil)
	e := NewForProvider(ProviderClaude, s, nil, nil)
	got := claudePick(e, nil, nil)
	if !got.Reject || !strings.Contains(got.RejectReason, "Claude") {
		t.Fatalf("got %+v", got)
	}
}

func TestSettingsDefaultPerPool(t *testing.T) {
	c := DefaultSettingsFor(ProviderClaude)
	if c.WindowStartModel != DefaultClaudeStarterModel || !c.Enabled {
		t.Fatalf("claude defaults %+v", c)
	}
	var empty Settings
	n, _ := empty.NormalizeFor(ProviderClaude, nil)
	if n.WindowStartModel != DefaultClaudeStarterModel || n.WindowStartEffort != DefaultClaudeStarterEffort {
		t.Fatalf("empty claude settings fell back to %q / %q", n.WindowStartModel, n.WindowStartEffort)
	}
	n, _ = empty.NormalizeFor(ProviderCodex, nil)
	if n.WindowStartModel != DefaultStarterModel {
		t.Fatalf("empty codex settings fell back to %q", n.WindowStartModel)
	}
}
