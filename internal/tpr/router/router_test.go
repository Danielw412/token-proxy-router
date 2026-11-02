package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router/engine"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// fakeHost stands in for the auth manager.
type fakeHost struct {
	mu       sync.Mutex
	accounts []engine.HostAccount
	quota    map[string]engine.Quota
	fetchErr error
	fetches  []string
	executed []string
}

func (f *fakeHost) Accounts() []engine.HostAccount {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]engine.HostAccount(nil), f.accounts...)
}

func (f *fakeHost) FetchQuota(_ context.Context, _ string, id string) (engine.Quota, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches = append(f.fetches, id)
	if f.fetchErr != nil {
		return engine.Quota{}, f.fetchErr
	}
	return f.quota[id], nil
}

func (f *fakeHost) ExecuteModel(_ context.Context, _ string, model string, _ []byte, _ http.Header, authID string) (ModelResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.executed = append(f.executed, authID+":"+model)
	return ModelResponse{StatusCode: 200, Body: []byte(`{"usage":{"input_tokens":3,"output_tokens":1,"cache_read_input_tokens":2}}`)}, nil
}

func newTestRouter(t *testing.T) (*Router, *fakeHost) {
	t.Helper()
	t.Setenv(home.EnvHome, t.TempDir())
	host := &fakeHost{accounts: []engine.HostAccount{
		{ID: "codex-a.json", AuthIndex: "ca", Provider: "codex", Email: "alice@example.com"},
		{ID: "codex-b.json", AuthIndex: "cb", Provider: "codex", Email: "bob@example.com"},
		{ID: "claude-a.json", AuthIndex: "la", Provider: "claude", Email: "carol@example.com"},
		{ID: "claude-b.json", AuthIndex: "lb", Provider: "claude", Email: "dave@example.com"},
		{ID: "gemini-a.json", AuthIndex: "ga", Provider: "gemini", Email: "erin@example.com"},
	}, quota: map[string]engine.Quota{}}
	r := New(host)
	for _, pool := range r.pools {
		pool.Engine.ObserveAccounts(host.Accounts())
	}
	return r, host
}

func candidates(provider string, ids ...string) []pluginapi.SchedulerAuthCandidate {
	out := make([]pluginapi.SchedulerAuthCandidate, 0, len(ids))
	for _, id := range ids {
		out = append(out, pluginapi.SchedulerAuthCandidate{ID: id, Provider: provider})
	}
	return out
}

func pickReq(provider string, headers map[string][]string, ids ...string) pluginapi.SchedulerPickRequest {
	return pluginapi.SchedulerPickRequest{
		Provider:   provider,
		Model:      "any",
		Options:    pluginapi.SchedulerOptions{Headers: headers},
		Candidates: candidates(provider, ids...),
	}
}

// recordingNext stands in for an upstream scheduler plugin.
type recordingNext struct {
	calls  int
	last   pluginapi.SchedulerPickRequest
	across bool
}

func (n *recordingNext) PickAuth(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	n.calls++
	n.last = req
	return pluginapi.SchedulerPickResponse{Handled: true, AuthID: req.Candidates[0].ID}, true, nil
}
func (n *recordingNext) SchedulerWantsAcrossPriorities() bool { return n.across }

func TestPoolsRouteTheirOwnProviderIndependently(t *testing.T) {
	r, _ := newTestRouter(t)
	codex, handled, err := r.PickAuth(context.Background(), pickReq("codex", map[string][]string{"Session_id": {"s1"}}, "codex-a.json", "codex-b.json"))
	if err != nil || !handled || !strings.HasPrefix(codex.AuthID, "codex-") {
		t.Fatalf("codex pick = %+v handled=%v err=%v", codex, handled, err)
	}
	claude, handled, err := r.PickAuth(context.Background(), pickReq("claude", map[string][]string{"X-Claude-Code-Session-Id": {"s2"}}, "claude-a.json", "claude-b.json"))
	if err != nil || !handled || !strings.HasPrefix(claude.AuthID, "claude-") {
		t.Fatalf("claude pick = %+v handled=%v err=%v", claude, handled, err)
	}
	if err := r.Pool("codex").Engine.Select("codex-b.json"); err != nil {
		t.Fatal(err)
	}
	if got := r.Pool("claude").state(5).Active; got == "codex-b.json" {
		t.Fatal("pools must keep independent current accounts")
	}
}

func TestOtherProvidersAreLeftEntirelyToUpstream(t *testing.T) {
	r, _ := newTestRouter(t)
	resp, handled, err := r.PickAuth(context.Background(), pickReq("gemini", nil, "gemini-a.json"))
	if err != nil || handled || resp.Handled {
		t.Fatalf("gemini must not be handled: %+v handled=%v err=%v", resp, handled, err)
	}
	// A route offered by several providers is upstream's choice too.
	mixed := pickReq("", nil, "codex-a.json")
	mixed.Candidates = append(mixed.Candidates, pluginapi.SchedulerAuthCandidate{ID: "gemini-a.json", Provider: "gemini"})
	mixed.Candidates[0].Provider = "codex"
	if _, handled, _ = r.PickAuth(context.Background(), mixed); handled {
		t.Fatal("a mixed-provider route must be left to upstream")
	}
}

func TestMixedRouteOfOnePoolIsStillHandled(t *testing.T) {
	r, _ := newTestRouter(t)
	req := pickReq("", nil, "claude-a.json", "claude-b.json")
	req.Candidates[0].Provider, req.Candidates[1].Provider = "claude", "claude"
	if _, handled, _ := r.PickAuth(context.Background(), req); !handled {
		t.Fatal("candidates all in one pool should be routed by that pool")
	}
}

func TestPanicInDecisionFallsBackToUpstreamSelection(t *testing.T) {
	r, _ := newTestRouter(t)
	r.SetPickHookForTest(func() { panic("boom") })
	resp, handled, err := r.PickAuth(context.Background(), pickReq("codex", nil, "codex-a.json"))
	if err != nil || handled || resp.Handled || resp.Reject {
		t.Fatalf("panic must mean not-handled: %+v handled=%v err=%v", resp, handled, err)
	}
	r.SetPickHookForTest(nil)
	if _, handled, _ = r.PickAuth(context.Background(), pickReq("codex", nil, "codex-a.json")); !handled {
		t.Fatal("router must recover after a panic")
	}
}

func TestPanicInUsageIsContained(t *testing.T) {
	r, _ := newTestRouter(t)
	r.AddObserver(func(string, string, engine.Quota) { panic("observer") })
	headers := http.Header{"X-Codex-Primary-Used-Percent": {"10"}, "X-Codex-Primary-Reset-After-Seconds": {"60"}}
	r.HandleUsage(context.Background(), usage.Record{AuthID: "codex-a.json", Provider: "codex", ResponseHeaders: headers})
}

func TestDelegatesToSchedulerBehindAndNarrowsPriorities(t *testing.T) {
	r, _ := newTestRouter(t)
	next := &recordingNext{}
	r.SetNextScheduler(next)
	req := pickReq("gemini", nil, "gemini-a.json", "gemini-b.json")
	req.Candidates[0].Priority, req.Candidates[1].Priority = 1, 5
	resp, handled, err := r.PickAuth(context.Background(), req)
	if err != nil || !handled || next.calls != 1 {
		t.Fatalf("not delegated: %+v %v %v", resp, handled, err)
	}
	if len(next.last.Candidates) != 1 || next.last.Candidates[0].ID != "gemini-b.json" {
		t.Fatalf("a plugin that did not ask for every tier must see only the highest: %+v", next.last.Candidates)
	}
	next.across = true
	if _, _, err = r.PickAuth(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(next.last.Candidates) != 2 {
		t.Fatalf("a plugin that asked for every tier must see them all: %+v", next.last.Candidates)
	}
	// Codex stays with the router even with a plugin behind it.
	calls := next.calls
	if _, handled, _ = r.PickAuth(context.Background(), pickReq("codex", nil, "codex-a.json")); !handled || next.calls != calls {
		t.Fatal("pool requests must not reach the scheduler behind")
	}
}

func TestDisabledPoolAndStoppedRouterLeaveRequestsToUpstream(t *testing.T) {
	r, _ := newTestRouter(t)
	pool := r.Pool("codex")
	s, _ := pool.Engine.Settings()
	s.Enabled = false
	pool.Engine.Configure(s, nil)
	if _, handled, _ := r.PickAuth(context.Background(), pickReq("codex", nil, "codex-a.json")); handled {
		t.Fatal("a disabled pool must not decide")
	}
	if _, handled, _ := r.PickAuth(context.Background(), pickReq("claude", nil, "claude-a.json")); !handled {
		t.Fatal("the other pool stays on")
	}
	r.Stop()
	if _, handled, _ := r.PickAuth(context.Background(), pickReq("claude", nil, "claude-a.json")); handled {
		t.Fatal("a stopped router must not decide")
	}
}

func TestStrictPinRejectionPassesThrough(t *testing.T) {
	r, _ := newTestRouter(t)
	pool := r.Pool("codex")
	s, _ := pool.Engine.Settings()
	s.ManualFallback = engine.FallbackStrict
	pool.Engine.Configure(s, nil)
	if err := pool.Engine.Pin("codex-b.json"); err != nil {
		t.Fatal(err)
	}
	resp, handled, err := r.PickAuth(context.Background(), pickReq("codex", nil, "codex-a.json"))
	if err != nil || !handled || !resp.Reject || resp.RejectCode != "quota_router_pinned_unavailable" {
		t.Fatalf("resp = %+v handled=%v err=%v", resp, handled, err)
	}
}

func TestPausedRequestWaitsAndContextCancelEndsItsWait(t *testing.T) {
	r, _ := newTestRouter(t)
	pool := r.Pool("claude")
	if err := pool.Engine.Pause(0, 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_, _, _ = r.PickAuth(ctx, pickReq("claude", nil, "claude-a.json"))
		close(done)
	}()
	waitingCount(t, pool, 1)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled request stayed held")
	}
	waitingCount(t, pool, 0)
}

func TestUsageUpdatesQuotaAndTellsObservers(t *testing.T) {
	r, _ := newTestRouter(t)
	var mu sync.Mutex
	var seen []string
	r.AddObserver(func(provider, id string, q engine.Quota) {
		mu.Lock()
		defer mu.Unlock()
		if q.Short != nil {
			seen = append(seen, provider+"/"+id)
		}
	})
	reset := time.Now().Add(time.Hour).Unix()
	r.HandleUsage(context.Background(), usage.Record{AuthID: "claude-a.json", Provider: "claude", RequestedAt: time.Now(), ResponseHeaders: http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.5"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {time.Unix(reset, 0).UTC().Format(time.RFC3339)},
	}})
	r.HandleUsage(context.Background(), usage.Record{AuthID: "gemini-a.json", Provider: "gemini"})
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "claude/claude-a.json" {
		t.Fatalf("observers saw %v", seen)
	}
	q, ok := r.Pool("claude").Engine.QuotaOf("claude-a.json")
	if !ok || q.Short == nil || q.Short.UsedPercent != 50 {
		t.Fatalf("quota = %+v ok=%v", q, ok)
	}
}

func TestOverageOnlyRejectionDoesNotBlockAClaudeAccount(t *testing.T) {
	r, _ := newTestRouter(t)
	reset := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	r.HandleUsage(context.Background(), usage.Record{AuthID: "claude-a.json", Provider: "claude", RequestedAt: time.Now(), Failed: true, Fail: usage.Failure{StatusCode: 429}, ResponseHeaders: http.Header{
		"Anthropic-Ratelimit-Unified-Status":               {"rejected"},
		"Anthropic-Ratelimit-Unified-5h-Status":            {"allowed"},
		"Anthropic-Ratelimit-Unified-7d-Status":            {"allowed"},
		"Anthropic-Ratelimit-Unified-5h-Utilization":       {"0.1"},
		"Anthropic-Ratelimit-Unified-5h-Reset":             {reset},
		"Anthropic-Ratelimit-Unified-7d-Utilization":       {"0.1"},
		"Anthropic-Ratelimit-Unified-7d-Reset":             {reset},
		"Anthropic-Ratelimit-Unified-Overage-Status":       {"rejected"},
		"Anthropic-Ratelimit-Unified-Representative-Claim": {"overage"},
	}})
	for _, a := range r.Pool("claude").Engine.View(0).Accounts {
		if a.ID == "claude-a.json" && a.State == "exhausted" {
			t.Fatalf("overage-only rejection took the account down: %s", a.Detail)
		}
	}
}

func TestUnifiedRejectionBlocksAClaudeAccount(t *testing.T) {
	r, _ := newTestRouter(t)
	reset := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	r.HandleUsage(context.Background(), usage.Record{AuthID: "claude-a.json", Provider: "claude", RequestedAt: time.Now(), Failed: true, Fail: usage.Failure{StatusCode: 429}, ResponseHeaders: http.Header{
		"Anthropic-Ratelimit-Unified-Status":         {"rejected"},
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.2"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {reset},
	}})
	var state string
	for _, a := range r.Pool("claude").Engine.View(0).Accounts {
		if a.ID == "claude-a.json" {
			state = a.State
		}
	}
	if state != "exhausted" {
		t.Fatalf("state = %q", state)
	}
}

func TestStateAndSettingsSurviveARestart(t *testing.T) {
	r, _ := newTestRouter(t)
	pool := r.Pool("claude")
	if err := pool.Engine.Select("claude-b.json"); err != nil {
		t.Fatal(err)
	}
	s, _ := pool.Engine.Settings()
	s.SwitchThresholdPercent = 12
	if err := saveSettings("claude", s); err != nil {
		t.Fatal(err)
	}
	pool.Flush()

	restarted := New(&fakeHost{})
	again := restarted.Pool("claude")
	if got := again.state(5).Active; got != "claude-b.json" {
		t.Fatalf("active after restart = %q", got)
	}
	if got, _ := again.Engine.Settings(); got.SwitchThresholdPercent != 12 {
		t.Fatalf("threshold after restart = %v", got.SwitchThresholdPercent)
	}
	if got := restarted.Pool("codex").state(5).Active; got == "claude-b.json" {
		t.Fatal("codex pool must not read claude's state")
	}
}

func TestStateFilesAreOwnerOnlyAndHoldNoSecrets(t *testing.T) {
	r, _ := newTestRouter(t)
	pool := r.Pool("codex")
	_, _, _ = r.PickAuth(context.Background(), pickReq("codex", map[string][]string{"Session_id": {"s"}}, "codex-a.json"))
	pool.Engine.MarkDirty()
	pool.Flush()
	info, err := os.Stat(engineStatePath("codex"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("state file mode = %o", perm)
	}
}

func TestPollErrorsAreRecordedAndDoNotEcho(t *testing.T) {
	r, host := newTestRouter(t)
	host.fetchErr = errors.New("usage endpoint returned HTTP 401")
	pool := r.Pool("codex")
	pool.refreshQuota(context.Background(), "codex-a.json", "ca")
	found := false
	for _, a := range pool.Engine.View(0).Accounts {
		if a.ID == "codex-a.json" && a.PollError == "usage endpoint returned HTTP 401" {
			found = true
		}
	}
	if !found {
		t.Fatal("poll error not recorded")
	}
}

func TestStartWindowSendsOneRequestLockedToTheAccountAndHonoursRunningWindows(t *testing.T) {
	StarterSettleWait = time.Millisecond
	r, host := newTestRouter(t)
	pool := r.Pool("claude")
	now := time.Now()
	host.quota["claude-a.json"] = engine.Quota{ObservedAt: now, Source: "poll",
		Short: &engine.Window{WindowMinutes: 300, ObservedAt: now, NotStarted: true},
		Long:  &engine.Window{UsedPercent: 3, WindowMinutes: 10080, ResetAt: now.Add(72 * time.Hour), ObservedAt: now}}
	res := pool.startWindow(context.Background(), engine.StartPlan{Account: "claude-a.json", Source: engine.SourceNow})
	if res.Status != engine.StartStarted || res.InputTokens != 3 || res.CachedTokens != 2 {
		t.Fatalf("result = %+v", res)
	}
	if len(host.executed) != 1 || host.executed[0] != "claude-a.json:"+engine.DefaultClaudeStarterModel {
		t.Fatalf("executed = %v", host.executed)
	}

	host.quota["claude-b.json"] = engine.Quota{ObservedAt: now, Source: "poll",
		Short: &engine.Window{UsedPercent: 10, WindowMinutes: 300, ResetAt: now.Add(time.Hour), ObservedAt: now}}
	res = pool.startWindow(context.Background(), engine.StartPlan{Account: "claude-b.json", Source: engine.SourceNow})
	if res.Status != engine.StartRunning {
		t.Fatalf("result = %+v", res)
	}
	if len(host.executed) != 1 {
		t.Fatalf("a running window must send nothing: %v", host.executed)
	}
}

func TestStarterBodyPerProvider(t *testing.T) {
	body, headers := starterBody("claude", "m", "none", "one-off")
	var claude map[string]any
	if err := json.Unmarshal(body, &claude); err != nil {
		t.Fatal(err)
	}
	if claude["model"] != "m" || claude["messages"] == nil || claude["input"] != nil {
		t.Fatalf("claude body = %s", body)
	}
	if headers.Get("X-Claude-Code-Session-Id") != "one-off" {
		t.Fatalf("headers = %v", headers)
	}
	body, headers = starterBody("codex", "m", "low", "one-off")
	var codex map[string]any
	if err := json.Unmarshal(body, &codex); err != nil {
		t.Fatal(err)
	}
	if codex["store"] != false || codex["input"] == nil || codex["prompt_cache_key"] != "one-off" {
		t.Fatalf("codex body = %s", body)
	}
	if headers.Get("Session_id") != "one-off" {
		t.Fatalf("headers = %v", headers)
	}
}

func TestHostAccountFromAuthMatchesPluginHostRules(t *testing.T) {
	file := t.TempDir() + "/codex-a.json"
	if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		auth *coreauth.Auth
		want bool
	}{
		{"file account", &coreauth.Auth{ID: "a", Provider: "codex", Attributes: map[string]string{coreauth.AttributePath: file}, Metadata: map[string]any{"email": "alice@example.com", "access_token": "x"}}, true},
		{"config api key", &coreauth.Auth{ID: "b", Provider: "codex", Attributes: map[string]string{"api_key": "x"}}, false},
		{"runtime login", &coreauth.Auth{ID: "c", Provider: "claude", Attributes: map[string]string{"runtime_only": "true"}}, true},
		{"disabled runtime login", &coreauth.Auth{ID: "d", Provider: "claude", Disabled: true, Attributes: map[string]string{"runtime_only": "true"}}, false},
		{"deleted file", &coreauth.Auth{ID: "e", Provider: "codex", Disabled: true, Attributes: map[string]string{coreauth.AttributePath: file + ".gone"}}, false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		entry, ok := hostAccountFromAuth(c.auth)
		if ok != c.want {
			t.Errorf("%s: ok=%v", c.name, ok)
		}
		raw, _ := json.Marshal(entry)
		if strings.Contains(string(raw), "access_token") {
			t.Errorf("%s: roster entry carries a token field: %s", c.name, raw)
		}
	}
}

func TestSanitizeErrorDropsCredentialsAndBodies(t *testing.T) {
	long := strings.Repeat("x y ", 200)
	cases := []struct{ in, mustNot, must string }{
		{`{"error":{"message":"Your token sk-ant-abcdefghijklmnop expired","type":"auth"},"context":{"request":"SECRET-BODY"}}`, "sk-ant-abcdefgh", "expired"},
		{`{"unrelated":"SECRET-BODY-TEXT"}`, "SECRET-BODY-TEXT", "upstream returned an error"},
		{"Authorization: Bearer abc.def.ghi failed", "abc.def.ghi", "failed"},
		{`access_token=abcd1234 rejected`, "abcd1234", "rejected"},
		{"token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abcdef rejected", "eyJhbGci", "rejected"},
		{"line one\nline two", "\n", "line one line two"},
		{strings.Repeat("a", 500), strings.Repeat("a", 40), "[redacted]"},
		{long, long, "x y"},
	}
	for _, c := range cases {
		got := SanitizeError(c.in)
		if strings.Contains(got, c.mustNot) {
			t.Errorf("SanitizeError(%q) = %q still contains %q", c.in, got, c.mustNot)
		}
		if !strings.Contains(got, c.must) {
			t.Errorf("SanitizeError(%q) = %q lost %q", c.in, got, c.must)
		}
		if len(got) > maxSanitized+len("…") {
			t.Errorf("SanitizeError output too long: %d", len(got))
		}
	}
	if SanitizeError("  ") != "" {
		t.Error("empty input must stay empty")
	}
}

// apiServer serves the routes on a router installed as current.
func apiServer(t *testing.T, r *Router) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	if r != nil {
		current.Store(r)
		t.Cleanup(func() { current.CompareAndSwap(r, nil) })
	}
	e := gin.New()
	registerRoutes(e.Group("/tpr"))
	return e
}

func call(t *testing.T, e *gin.Engine, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	var out map[string]any
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s: bad JSON %q", method, path, w.Body.String())
		}
	}
	return w.Code, out
}

func TestRoutesAnswer503UntilStarted(t *testing.T) {
	current.Store(nil)
	e := apiServer(t, nil)
	for _, path := range []string{"/tpr/router/state", "/tpr/router/settings?provider=codex"} {
		code, out := call(t, e, http.MethodGet, path, "")
		if code != http.StatusServiceUnavailable || out["error"] == "" {
			t.Fatalf("%s: %d %v", path, code, out)
		}
	}
}

func TestRouterAPIContract(t *testing.T) {
	r, _ := newTestRouter(t)
	e := apiServer(t, r)

	code, out := call(t, e, http.MethodGet, "/tpr/router/state", "")
	if code != 200 || len(out["pools"].([]any)) != 2 {
		t.Fatalf("all pools: %d %v", code, out)
	}
	code, out = call(t, e, http.MethodGet, "/tpr/router/state?provider=claude&history=3", "")
	if code != 200 || out["provider"] != "claude" {
		t.Fatalf("one pool: %d %v", code, out)
	}
	for _, key := range []string{"mode", "accounts", "sessions", "settings", "history", "pause", "slowdown", "waiting", "windows", "last_error"} {
		if _, ok := out[key]; !ok {
			t.Errorf("state is missing %q", key)
		}
	}
	if code, _ = call(t, e, http.MethodGet, "/tpr/router/state?provider=gemini", ""); code != 400 {
		t.Fatalf("unknown provider: %d", code)
	}
	if code, _ = call(t, e, http.MethodPost, "/tpr/router/mode", `{"mode":"auto"}`); code != 400 {
		t.Fatalf("missing provider: %d", code)
	}

	code, out = call(t, e, http.MethodPost, "/tpr/router/mode?provider=codex", `{"mode":"manual","account":"codex-b.json"}`)
	if code != 200 || out["mode"] != "manual" || out["active"] != "codex-b.json" {
		t.Fatalf("pin: %d %v", code, out)
	}
	if code, _ = call(t, e, http.MethodPost, "/tpr/router/mode?provider=codex", `{"mode":"manual","account":"nobody"}`); code != 404 {
		t.Fatalf("pin unknown: %d", code)
	}
	if code, _ = call(t, e, http.MethodPost, "/tpr/router/mode?provider=codex", `{"mode":"sideways"}`); code != 400 {
		t.Fatalf("bad mode: %d", code)
	}
	code, out = call(t, e, http.MethodPost, "/tpr/router/select?provider=codex", `{"account":"codex-a.json"}`)
	if code != 200 || out["mode"] != "auto" || out["active"] != "codex-a.json" {
		t.Fatalf("select: %d %v", code, out)
	}
	if code, _ = call(t, e, http.MethodPost, "/tpr/router/refresh?provider=codex", ""); code != 200 {
		t.Fatalf("refresh: %d", code)
	}

	code, out = call(t, e, http.MethodPatch, "/tpr/router/settings?provider=claude",
		`{"switch_threshold_percent":999,"rank_by":["most_remaining"],"disabled_accounts":["claude-b.json"]}`)
	if code != 200 {
		t.Fatalf("patch: %d %v", code, out)
	}
	settings := out["settings"].(map[string]any)
	if settings["switch_threshold_percent"].(float64) != 90 || settings["rank_by"].([]any)[0] != "most_remaining" {
		t.Fatalf("settings = %v", settings)
	}
	if len(out["warnings"].([]any)) == 0 {
		t.Fatal("a clamped value must produce a warning")
	}
	if code, _ = call(t, e, http.MethodPatch, "/tpr/router/settings?provider=claude", `{"nope":1}`); code != 400 {
		t.Fatalf("unknown key: %d", code)
	}
	code, out = call(t, e, http.MethodGet, "/tpr/router/settings?provider=claude", "")
	if code != 200 || out["settings"].(map[string]any)["switch_threshold_percent"].(float64) != 90 {
		t.Fatalf("get settings: %d %v", code, out)
	}
	if got, _ := loadSettings("claude"); got.SwitchThresholdPercent != 90 {
		t.Fatalf("settings not saved: %v", got.SwitchThresholdPercent)
	}
	if got, _ := loadSettings("codex"); got.SwitchThresholdPercent == 90 {
		t.Fatal("settings leaked into the other pool")
	}

	code, out = call(t, e, http.MethodPost, "/tpr/router/windows/schedule?provider=codex",
		`{"account":"codex-a.json","at":"`+time.Now().Add(time.Hour).UTC().Format(time.RFC3339)+`","daily":true}`)
	if code != 200 {
		t.Fatalf("schedule: %d %v", code, out)
	}
	starter := out["starter"].(map[string]any)
	plans := starter["plans"].([]any)
	if len(plans) != 1 || plans[0].(map[string]any)["daily"] != true {
		t.Fatalf("plans = %v", plans)
	}
	id := plans[0].(map[string]any)["id"].(string)
	if code, _ = call(t, e, http.MethodPost, "/tpr/router/windows/schedule?provider=codex", `{"account":"codex-a.json","at":"2001-01-01T00:00:00Z"}`); code != 400 {
		t.Fatalf("schedule in the past: %d", code)
	}
	if code, _ = call(t, e, http.MethodPost, "/tpr/router/windows/cancel?provider=codex", `{"id":"`+id+`"}`); code != 200 {
		t.Fatalf("cancel: %d", code)
	}
	if code, _ = call(t, e, http.MethodPost, "/tpr/router/windows/cancel?provider=codex", `{"id":"`+id+`"}`); code != 404 {
		t.Fatalf("cancel twice: %d", code)
	}
	if code, _ = call(t, e, http.MethodPost, "/tpr/router/windows/start?provider=codex", `{"delay_minutes":5000}`); code != 400 {
		t.Fatalf("bad delay: %d", code)
	}
	code, out = call(t, e, http.MethodPost, "/tpr/router/windows/start?provider=codex", `{"delay_minutes":30,"stagger_minutes":10}`)
	if code != 200 || len(out["starter"].(map[string]any)["plans"].([]any)) != 2 {
		t.Fatalf("start all: %d %v", code, out)
	}
	if code, out = call(t, e, http.MethodPost, "/tpr/router/windows/cancel?provider=codex", `{}`); code != 200 || len(out["starter"].(map[string]any)["plans"].([]any)) != 0 {
		t.Fatalf("cancel all: %d %v", code, out)
	}

	code, out = call(t, e, http.MethodPost, "/tpr/router/pause?provider=claude", `{"minutes":30,"auto_resume_minutes":5}`)
	if code != 200 || out["pause"] == nil {
		t.Fatalf("pause: %d %v", code, out)
	}
	if code, _ = call(t, e, http.MethodPost, "/tpr/router/pause?provider=claude", `{"minutes":-1}`); code != 400 {
		t.Fatalf("bad pause: %d", code)
	}
	if st := r.Pool("codex").state(1); st.Pause != nil {
		t.Fatal("a pause in one pool must not pause the other")
	}
	code, out = call(t, e, http.MethodPost, "/tpr/router/resume?provider=claude", "")
	if code != 200 || out["pause"] != nil {
		t.Fatalf("resume: %d %v", code, out)
	}
	code, out = call(t, e, http.MethodPost, "/tpr/router/slowdown?provider=claude", `{"seconds":30}`)
	if code != 200 || out["slowdown"].(map[string]any)["seconds"].(float64) != 30 {
		t.Fatalf("slowdown: %d %v", code, out)
	}
	if code, _ = call(t, e, http.MethodPost, "/tpr/router/slowdown?provider=claude", `{"seconds":999}`); code != 400 {
		t.Fatalf("bad slowdown: %d", code)
	}
	if code, _ = call(t, e, http.MethodPost, "/tpr/router/select?provider=codex", `{bad`); code != 400 {
		t.Fatalf("bad JSON: %d", code)
	}
}
