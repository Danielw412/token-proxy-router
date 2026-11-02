// Package e2e boots a real server in-process against a mock upstream that speaks
// both Codex and Claude. No credential or account data leaves the machine: every
// account is a fake auth file in a temp dir, TPR_HOME is a temp dir, and every model
// and quota call goes to the mock. Upstream's own version checks still run.
//
// Run it with: go test -race ./internal/tpr/e2e/
// It takes several seconds, so -short skips it.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	tprbackend "github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/backend"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const (
	mgmtKey   = "e2e-management-key"
	clientKey = "sk-e2e-client-key-aaaa"
	modelC    = "gpt-6-luna"
	modelA    = "claude-sonnet-5-5"
)

// account is one fake credential and how the mock answers for it.
type account struct {
	file, token, email string
	used               float64 // weekly percent used, reported on every response
	exhausted          bool    // answer with a quota rejection
	idle               bool    // the usage endpoint reports no running 5-hour window
}

// mock is the upstream. It knows accounts by the bearer token the proxy sends.
type mock struct {
	t      *testing.T
	mu     sync.Mutex
	byTok  map[string]*account
	served []string // account file of each model request, in order
	models map[string]string
	polls  map[string]int
}

func (m *mock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	m.mu.Lock()
	acct := m.byTok[token]
	m.mu.Unlock()
	if acct == nil {
		http.Error(w, `{"error":"unknown token"}`, http.StatusUnauthorized)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var payload struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(raw, &payload)
	switch {
	case r.URL.Path == "/backend-api/wham/usage":
		m.countPoll(acct)
		used := m.usedOf(acct)
		w.Header().Set("Content-Type", "application/json")
		if acct.idle {
			// Codex's idle window: 0% used, with a reset that keeps sliding.
			fmt.Fprintf(w, `{"plan_type":"plus","rate_limit":{"allowed":true,"primary_window":{"used_percent":0,"limit_window_seconds":18000,"reset_after_seconds":18000},"secondary_window":{"used_percent":%v,"limit_window_seconds":604800,"reset_after_seconds":259200}}}`, used)
			return
		}
		fmt.Fprintf(w, `{"plan_type":"plus","rate_limit":{"allowed":true,"primary_window":{"used_percent":%v,"limit_window_seconds":18000,"reset_after_seconds":3600},"secondary_window":{"used_percent":%v,"limit_window_seconds":604800,"reset_after_seconds":259200}}}`, used/2, used)
	case r.URL.Path == "/api/oauth/usage":
		m.countPoll(acct)
		used := m.usedOf(acct)
		w.Header().Set("Content-Type", "application/json")
		weekly := time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339)
		if acct.idle {
			fmt.Fprintf(w, `{"five_hour":{"utilization":0,"resets_at":null},"seven_day":{"utilization":%v,"resets_at":%q}}`, used, weekly)
			return
		}
		fmt.Fprintf(w, `{"five_hour":{"utilization":%v,"resets_at":%q},"seven_day":{"utilization":%v,"resets_at":%q}}`,
			used/2, time.Now().Add(time.Hour).UTC().Format(time.RFC3339), used, weekly)
	case r.URL.Path == "/api/oauth/profile":
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"account":{"has_claude_max":true},"organization":{"rate_limit_tier":"default_claude_max_5x"}}`)
	case strings.HasSuffix(r.URL.Path, "/responses"):
		m.record(acct)
		m.mu.Lock()
		m.models[acct.file] = payload.Model
		m.mu.Unlock()
		m.codexResponse(w, acct)
	case strings.HasSuffix(r.URL.Path, "/messages"):
		m.record(acct)
		m.mu.Lock()
		m.models[acct.file] = payload.Model
		m.mu.Unlock()
		m.claudeResponse(w, acct)
	default:
		http.NotFound(w, r)
	}
}

func (m *mock) record(a *account) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.served = append(m.served, a.file)
}

func (m *mock) countPoll(a *account) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.polls[a.file]++
}

func (m *mock) usedOf(a *account) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return a.used
}

func (m *mock) lastServed() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.served) == 0 {
		return ""
	}
	return m.served[len(m.served)-1]
}

func (m *mock) set(a *account, used float64, exhausted bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a.used, a.exhausted = used, exhausted
}

func (m *mock) codexResponse(w http.ResponseWriter, a *account) {
	m.mu.Lock()
	used, exhausted := a.used, a.exhausted
	m.mu.Unlock()
	h := w.Header()
	h.Set("X-Codex-Primary-Used-Percent", strconv.FormatFloat(used/2, 'f', -1, 64))
	h.Set("X-Codex-Primary-Window-Minutes", "300")
	h.Set("X-Codex-Primary-Reset-After-Seconds", "3600")
	h.Set("X-Codex-Secondary-Used-Percent", strconv.FormatFloat(used, 'f', -1, 64))
	h.Set("X-Codex-Secondary-Window-Minutes", "10080")
	h.Set("X-Codex-Secondary-Reset-After-Seconds", "259200")
	if exhausted {
		h.Set("Content-Type", "application/json")
		h.Set("X-Codex-Secondary-Used-Percent", "100")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":259200}}`)
		return
	}
	h.Set("Content-Type", "text/event-stream")
	body := `{"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"gpt-6-luna","output":[{"type":"message","id":"m1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`
	fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", body)
}

func (m *mock) claudeResponse(w http.ResponseWriter, a *account) {
	m.mu.Lock()
	used, exhausted := a.used, a.exhausted
	m.mu.Unlock()
	h := w.Header()
	h.Set("Anthropic-Ratelimit-Unified-5h-Utilization", strconv.FormatFloat(used/200, 'f', -1, 64))
	h.Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
	h.Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
	h.Set("Anthropic-Ratelimit-Unified-7d-Utilization", strconv.FormatFloat(used/100, 'f', -1, 64))
	h.Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(time.Now().Add(72*time.Hour).Unix(), 10))
	h.Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
	if exhausted {
		h.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
		h.Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
		h.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "1")
		h.Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit."}}`)
		return
	}
	h.Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`)
}

// rewriter sends every upstream call to the mock, whatever host the executor chose.
type rewriter struct{ target *url.URL }

func (r rewriter) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme, clone.URL.Host = r.target.Scheme, r.target.Host
	clone.Host = r.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

type rewriteProvider struct{ rt http.RoundTripper }

func (p rewriteProvider) RoundTripperFor(*coreauth.Auth) http.RoundTripper { return p.rt }

type server struct {
	t         *testing.T
	base      string
	mock      *mock
	router    *router.Router
	alice     *account
	bob       *account
	carol     *account
	dave      *account
	erin      *account // Off for automatic routing; only window starts reach it
	frank     *account // the same, in the Codex pool
	mockURL   string
	client    *http.Client
	authDir   string
	configDir string
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func writeAuth(t *testing.T, dir string, a *account, provider, plan string) {
	t.Helper()
	body := map[string]any{"type": provider, "email": a.email, "access_token": a.token,
		"expired": time.Now().Add(240 * time.Hour).UTC().Format(time.RFC3339)}
	if plan != "" {
		body["plan_type"] = plan
	}
	raw, _ := json.Marshal(body)
	if err := os.WriteFile(filepath.Join(dir, a.file), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// boot starts everything and waits until both pools know their accounts.
func boot(t *testing.T) *server {
	t.Helper()
	if testing.Short() {
		t.Skip("boots a real server; run without -short")
	}
	root := t.TempDir()
	t.Setenv(home.EnvHome, filepath.Join(root, "home"))
	t.Setenv("WRITABLE_PATH", filepath.Join(root, "writable"))

	s := &server{t: t, authDir: filepath.Join(root, "auth"), client: &http.Client{Timeout: 30 * time.Second}}
	s.alice = &account{file: "codex-alice@example.com-plus.json", token: "fake-codex-alice", email: "alice@example.com", used: 10}
	s.bob = &account{file: "codex-bob@example.com-plus.json", token: "fake-codex-bob", email: "bob@example.com", used: 20}
	s.carol = &account{file: "claude-carol@example.com.json", token: "fake-claude-carol", email: "carol@example.com", used: 15}
	s.dave = &account{file: "claude-dave@example.com.json", token: "fake-claude-dave", email: "dave@example.com", used: 25}
	s.erin = &account{file: "claude-erin@example.com.json", token: "fake-claude-erin", email: "erin@example.com", used: 5, idle: true}
	s.frank = &account{file: "codex-frank@example.com-plus.json", token: "fake-codex-frank", email: "frank@example.com", used: 5, idle: true}
	s.mock = &mock{t: t, byTok: map[string]*account{}, polls: map[string]int{}, models: map[string]string{}}
	if err := os.MkdirAll(s.authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*account{s.alice, s.bob, s.frank} {
		s.mock.byTok[a.token] = a
		writeAuth(t, s.authDir, a, "codex", "plus")
	}
	for _, a := range []*account{s.carol, s.dave, s.erin} {
		s.mock.byTok[a.token] = a
		writeAuth(t, s.authDir, a, "claude", "")
	}
	upstream := httptest.NewServer(s.mock)
	t.Cleanup(upstream.Close)
	s.mockURL = upstream.URL

	oldDelay, oldTick, oldSettle, oldCodex, oldUsage, oldProfile := router.InitialLoopDelay, router.TickEvery, router.StarterSettleWait, router.CodexUsageURL, router.ClaudeUsageURL, router.ClaudeProfileURL
	router.InitialLoopDelay, router.TickEvery, router.StarterSettleWait = 50*time.Millisecond, 150*time.Millisecond, 10*time.Millisecond
	router.CodexUsageURL = upstream.URL + "/backend-api/wham/usage"
	router.ClaudeUsageURL = upstream.URL + "/api/oauth/usage"
	router.ClaudeProfileURL = upstream.URL + "/api/oauth/profile"
	t.Cleanup(func() {
		router.InitialLoopDelay, router.TickEvery, router.StarterSettleWait = oldDelay, oldTick, oldSettle
		router.CodexUsageURL, router.ClaudeUsageURL, router.ClaudeProfileURL = oldCodex, oldUsage, oldProfile
	})

	port := freePort(t)
	s.base = fmt.Sprintf("http://127.0.0.1:%d", port)
	yaml := fmt.Sprintf(`config-version: 8
server:
  host: "127.0.0.1"
  port: %d
management:
  allow-remote: false
  secret-key: %q
  disable-control-panel: true
  disable-auto-update-panel: true
access:
  api-keys:
    - %q
oauth:
  auth-dir: %q
routing:
  strategy: "fill-first"
upstream:
  request-retry: 0
`, port, mgmtKey, clientKey, s.authDir)
	cfgPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	service, err := cliproxy.NewBuilder().WithConfig(cfg).WithConfigPath(cfgPath).Build()
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(upstream.URL)
	mgr, cfgFn := service.TPRAttachment()
	mgr.SetRoundTripperProvider(rewriteProvider{rt: rewriter{target: target}})
	stop := tprbackend.Start(mgr, cfgFn)
	s.router = router.Current()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = service.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("the service did not stop")
		}
		stop()
	})

	s.waitFor("both pools to see their accounts", func() bool {
		var state struct {
			Pools []struct {
				Provider string `json:"provider"`
				Accounts []struct {
					ID    string `json:"id"`
					State string `json:"state"`
				} `json:"accounts"`
			} `json:"pools"`
		}
		code, raw := s.mgmt(http.MethodGet, "/router/state", "")
		if code != 200 || json.Unmarshal(raw, &state) != nil || len(state.Pools) != 2 {
			return false
		}
		for _, pool := range state.Pools {
			known := 0
			for _, a := range pool.Accounts {
				if a.State == "ok" {
					known++
				}
			}
			if known < 2 {
				return false
			}
		}
		return true
	})
	s.mgmtJSON(http.MethodPatch, "/router/settings?provider=claude", fmt.Sprintf(`{"disabled_accounts":[%q]}`, s.erin.file), &map[string]any{})
	s.mgmtJSON(http.MethodPatch, "/router/settings?provider=codex", fmt.Sprintf(`{"disabled_accounts":[%q]}`, s.frank.file), &map[string]any{})
	return s
}

func (s *server) waitFor(what string, cond func() bool) {
	s.t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.t.Fatalf("timed out waiting for %s", what)
}

func (s *server) mgmt(method, path, body string) (int, []byte) {
	req, err := http.NewRequest(method, s.base+"/v8/management/tpr"+path, strings.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+mgmtKey)
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, nil
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func (s *server) mgmtJSON(method, path, body string, out any) {
	s.t.Helper()
	code, raw := s.mgmt(method, path, body)
	if code != 200 {
		s.t.Fatalf("%s %s: %d %s", method, path, code, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		s.t.Fatalf("%s %s: %v: %s", method, path, err, raw)
	}
}

func (s *server) post(path string, headers map[string]string, body any) (int, string) {
	s.t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, s.base+path, bytes.NewReader(raw))
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+clientKey)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

// codex sends one Codex request in a session and returns the account that served it.
func (s *server) codex(session string) (string, int) {
	s.t.Helper()
	code, body := s.post("/v1/responses", map[string]string{"Session_id": session}, map[string]any{
		"model": modelC, "stream": false, "input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hi"}}}},
	})
	if code != 200 {
		return "", code
	}
	_ = body
	return s.mock.lastServed(), code
}

// claude sends one Claude request and returns the account that served it.
func (s *server) claude(headers map[string]string, body map[string]any) (string, int) {
	s.t.Helper()
	payload := map[string]any{"model": modelA, "max_tokens": 16, "messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	for k, v := range body {
		payload[k] = v
	}
	code, _ := s.post("/v1/messages", headers, payload)
	if code != 200 {
		return "", code
	}
	return s.mock.lastServed(), code
}

func (s *server) waitForModels() {
	s.t.Helper()
	s.waitFor("models to be served", func() bool {
		_, code := s.codex("warmup")
		return code == 200
	})
	s.waitFor("claude models to be served", func() bool {
		_, code := s.claude(nil, nil)
		return code == 200
	})
}

func TestEndToEnd(t *testing.T) {
	s := boot(t)
	s.waitForModels()

	t.Run("both providers are routed by their own engine", func(t *testing.T) {
		codexAccount, code := s.codex("codex-session-1")
		if code != 200 || !strings.HasPrefix(codexAccount, "codex-") {
			t.Fatalf("codex: %q %d", codexAccount, code)
		}
		claudeAccount, code := s.claude(map[string]string{"X-Claude-Code-Session-Id": "claude-session-1"}, nil)
		if code != 200 || !strings.HasPrefix(claudeAccount, "claude-") {
			t.Fatalf("claude: %q %d", claudeAccount, code)
		}
		for _, provider := range router.Providers {
			var state struct {
				Picks     int64            `json:"picks"`
				PickStats map[string]int64 `json:"pick_stats"`
				Active    string           `json:"active"`
			}
			s.mgmtJSON(http.MethodGet, "/router/state?provider="+provider, "", &state)
			if state.Picks == 0 || state.Active == "" {
				t.Fatalf("%s pool did not route: %+v", provider, state)
			}
		}
	})

	t.Run("a session stays on its account", func(t *testing.T) {
		first, _ := s.codex("sticky")
		// New sessions move to the other account; the old session must not.
		other := s.bob.file
		if first == other {
			other = s.alice.file
		}
		s.mgmtJSON(http.MethodPost, "/router/select?provider=codex", fmt.Sprintf(`{"account":%q}`, other), &map[string]any{})
		for i := 0; i < 3; i++ {
			if got, code := s.codex("sticky"); got != first || code != 200 {
				t.Fatalf("session moved from %s to %s (%d)", first, got, code)
			}
		}
		if got, _ := s.codex("brand-new-session"); got != other {
			t.Fatalf("a new session went to %s, current account is %s", got, other)
		}
	})

	t.Run("a Claude subagent and a body-only session keep one account", func(t *testing.T) {
		parent, _ := s.claude(map[string]string{"X-Claude-Code-Session-Id": "family"}, nil)
		child, _ := s.claude(map[string]string{"X-Claude-Code-Session-Id": "family", "X-Claude-Code-Agent-Id": "helper"}, nil)
		if child != parent {
			t.Fatalf("subagent on %s, parent on %s", child, parent)
		}
		other := s.carol.file
		if parent == other {
			other = s.dave.file
		}
		body := map[string]any{"metadata": map[string]any{"user_id": `{"session_id":"body-only-session"}`}}
		s.mgmtJSON(http.MethodPost, "/router/select?provider=claude", fmt.Sprintf(`{"account":%q}`, parent), &map[string]any{})
		first, code := s.claude(nil, body)
		if code != 200 {
			t.Fatalf("body-only session: %d", code)
		}
		s.mgmtJSON(http.MethodPost, "/router/select?provider=claude", fmt.Sprintf(`{"account":%q}`, other), &map[string]any{})
		second, _ := s.claude(nil, body)
		if second != first {
			t.Fatalf("a session identified only by metadata.user_id moved from %s to %s", first, second)
		}
	})

	t.Run("quota exhaustion switches accounts", func(t *testing.T) {
		current, _ := s.codex("exhaust-a")
		victim, spare := s.alice, s.bob
		if current == s.bob.file {
			victim, spare = s.bob, s.alice
		}
		s.mock.set(victim, 100, true)
		// Upstream's own failover may answer this from the spare account; either
		// way the rejection reaches the router.
		_, _ = s.codex("exhaust-a")
		s.waitFor("the codex account to read as exhausted", func() bool {
			var state struct {
				Accounts []struct {
					ID    string `json:"id"`
					State string `json:"state"`
				} `json:"accounts"`
			}
			s.mgmtJSON(http.MethodGet, "/router/state?provider=codex", "", &state)
			for _, a := range state.Accounts {
				if a.ID == victim.file {
					return a.State == "exhausted"
				}
			}
			return false
		})
		if got, code := s.codex("exhaust-new"); got != spare.file || code != 200 {
			t.Fatalf("after exhaustion: %s %d, want %s", got, code, spare.file)
		}
		if got, code := s.codex("exhaust-a"); got != spare.file || code != 200 {
			t.Fatalf("the exhausted session did not move: %s %d", got, code)
		}
		s.mock.set(victim, 100, false)
	})

	t.Run("a Claude rejection header switches accounts", func(t *testing.T) {
		current, _ := s.claude(map[string]string{"X-Claude-Code-Session-Id": "claude-exhaust"}, nil)
		victim, spare := s.carol, s.dave
		if current == s.dave.file {
			victim, spare = s.dave, s.carol
		}
		s.mock.set(victim, 100, true)
		_, _ = s.claude(map[string]string{"X-Claude-Code-Session-Id": "claude-exhaust"}, nil)
		s.waitFor("the claude account to read as exhausted", func() bool {
			var state struct {
				Accounts []struct {
					ID    string `json:"id"`
					State string `json:"state"`
				} `json:"accounts"`
			}
			s.mgmtJSON(http.MethodGet, "/router/state?provider=claude", "", &state)
			for _, a := range state.Accounts {
				if a.ID == victim.file {
					return a.State == "exhausted"
				}
			}
			return false
		})
		if got, code := s.claude(map[string]string{"X-Claude-Code-Session-Id": "claude-new"}, nil); got != spare.file || code != 200 {
			t.Fatalf("after rejection: %s %d, want %s", got, code, spare.file)
		}
		s.mock.set(victim, 100, false)
	})

	t.Run("a panic in the engine falls back to upstream selection", func(t *testing.T) {
		var panics int
		var mu sync.Mutex
		s.router.SetPickHookForTest(func() {
			mu.Lock()
			panics++
			mu.Unlock()
			panic("forced by the end-to-end test")
		})
		defer s.router.SetPickHookForTest(nil)
		got, code := s.claude(map[string]string{"X-Claude-Code-Session-Id": "during-panic"}, nil)
		if code != 200 || got == "" {
			t.Fatalf("a request failed because of the router: %q %d", got, code)
		}
		mu.Lock()
		defer mu.Unlock()
		if panics == 0 {
			t.Fatal("the hook never ran, so this proved nothing")
		}
	})

	t.Run("pause holds requests until resume", func(t *testing.T) {
		s.mgmtJSON(http.MethodPost, "/router/pause?provider=codex", `{"minutes":0,"auto_resume_minutes":0}`, &map[string]any{})
		result := make(chan int, 1)
		go func() {
			_, code := s.codex("paused-session")
			result <- code
		}()
		s.waitFor("the request to be held", func() bool {
			var state struct {
				Waiting int `json:"waiting"`
			}
			s.mgmtJSON(http.MethodGet, "/router/state?provider=codex", "", &state)
			return state.Waiting == 1
		})
		select {
		case code := <-result:
			t.Fatalf("a paused request went through: %d", code)
		case <-time.After(300 * time.Millisecond):
		}
		// The other pool is not paused.
		if _, code := s.claude(nil, nil); code != 200 {
			t.Fatalf("claude was held by codex's pause: %d", code)
		}
		s.mgmtJSON(http.MethodPost, "/router/resume?provider=codex", "", &map[string]any{})
		select {
		case code := <-result:
			if code != 200 {
				t.Fatalf("after resume: %d", code)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the request stayed held after resume")
		}
	})

	t.Run("window start sends one request locked to the account", func(t *testing.T) {
		for _, c := range []struct {
			provider, model string
			account         *account
		}{
			{"claude", "claude-haiku-5-5", s.erin},
			{"codex", "gpt-6-luna", s.frank},
		} {
			s.waitFor("the idle account's window to read as idle", func() bool {
				var state struct {
					Windows []struct {
						Account string `json:"account"`
						State   string `json:"state"`
					} `json:"windows"`
				}
				s.mgmtJSON(http.MethodGet, "/router/state?provider="+c.provider, "", &state)
				for _, w := range state.Windows {
					if w.Account == c.account.file {
						return w.State == "idle"
					}
				}
				return false
			})
			s.mgmtJSON(http.MethodPost, "/router/windows/start?provider="+c.provider, fmt.Sprintf(`{"account":%q}`, c.account.file), &map[string]any{})
			var status, detail string
			s.waitFor("the start to be logged", func() bool {
				var state struct {
					Starter struct {
						Log []struct {
							Account string `json:"account"`
							Status  string `json:"status"`
							Detail  string `json:"detail"`
						} `json:"log"`
					} `json:"starter"`
				}
				s.mgmtJSON(http.MethodGet, "/router/state?provider="+c.provider, "", &state)
				for _, entry := range state.Starter.Log {
					if entry.Account == c.account.file {
						status, detail = entry.Status, entry.Detail
						return true
					}
				}
				return false
			})
			if status != "started" {
				t.Fatalf("%s start: %q %q", c.provider, status, detail)
			}
			s.mock.mu.Lock()
			got := s.mock.models[c.account.file]
			s.mock.mu.Unlock()
			if got != c.model {
				t.Fatalf("%s start reached the account with model %q", c.provider, got)
			}
		}
	})

	t.Run("history records samples and pools return them", func(t *testing.T) {
		type poolsBody struct {
			Now   time.Time `json:"now"`
			Pools []struct {
				Provider         string  `json:"provider"`
				RemainingPercent float64 `json:"remaining_percent"`
				CapacityPercent  float64 `json:"capacity_percent"`
				Recorded         []struct {
					At      time.Time `json:"at"`
					Percent float64   `json:"percent"`
				} `json:"recorded"`
				Projected []struct {
					At time.Time `json:"at"`
				} `json:"projected"`
				Accounts []struct {
					ID   string `json:"id"`
					Plan string `json:"plan"`
				} `json:"accounts"`
				Coverage struct {
					Samples int `json:"samples"`
				} `json:"coverage"`
			} `json:"pools"`
		}
		var body poolsBody
		s.waitFor("samples in both pools", func() bool {
			s.mgmtJSON(http.MethodGet, "/quota/pools?burn=1h&window=weekly&future=same", "", &body)
			if len(body.Pools) != 2 {
				return false
			}
			for _, pool := range body.Pools {
				if pool.Coverage.Samples == 0 || len(pool.Recorded) == 0 || pool.CapacityPercent == 0 {
					return false
				}
			}
			return true
		})
		for _, pool := range body.Pools {
			if !pool.Projected[0].At.Equal(body.Now) {
				t.Fatalf("%s: projected starts at %v, now is %v", pool.Provider, pool.Projected[0].At, body.Now)
			}
			if len(pool.Accounts) < 2 {
				t.Fatalf("%s accounts = %+v", pool.Provider, pool.Accounts)
			}
		}
		var history struct {
			Provider string `json:"provider"`
			Samples  []struct {
				Percent float64 `json:"percent"`
				Source  string  `json:"source"`
			} `json:"samples"`
		}
		s.mgmtJSON(http.MethodGet, "/quota/accounts/"+url.PathEscape(s.alice.file)+"/history?window=weekly", "", &history)
		if history.Provider != "codex" || len(history.Samples) == 0 {
			t.Fatalf("history = %+v", history)
		}
		// Live numbers come from both providers' response headers, not only polls.
		// The collector records one sample a minute per account, so this reads the
		// router's view: a test this short would see only the first sample otherwise.
		for _, provider := range router.Providers {
			var state struct {
				Accounts []struct {
					Source string `json:"source"`
				} `json:"accounts"`
			}
			s.mgmtJSON(http.MethodGet, "/router/state?provider="+provider, "", &state)
			fromHeaders := false
			for _, a := range state.Accounts {
				fromHeaders = fromHeaders || a.Source == "response"
			}
			if !fromHeaders {
				t.Fatalf("%s: no account's quota came from response headers: %+v", provider, state.Accounts)
			}
		}
	})

	t.Run("idle accounts are polled and the plan is learned", func(t *testing.T) {
		s.waitFor("a poll of every account", func() bool {
			s.mock.mu.Lock()
			defer s.mock.mu.Unlock()
			for _, a := range []*account{s.alice, s.bob, s.carol, s.dave, s.erin, s.frank} {
				if s.mock.polls[a.file] == 0 {
					return false
				}
			}
			return true
		})
		s.waitFor("the Claude plan from the profile endpoint", func() bool {
			var body struct {
				Pools []struct {
					Provider string `json:"provider"`
					Accounts []struct {
						Plan string `json:"plan"`
					} `json:"accounts"`
				} `json:"pools"`
			}
			s.mgmtJSON(http.MethodGet, "/quota/pools", "", &body)
			for _, pool := range body.Pools {
				if pool.Provider != "claude" {
					continue
				}
				for _, a := range pool.Accounts {
					if a.Plan != "max5" {
						return false
					}
				}
				return true
			}
			return false
		})
	})

	t.Run("usage per key hides the key", func(t *testing.T) {
		var body struct {
			Keys []struct {
				KeyID    string `json:"key_id"`
				Masked   string `json:"masked"`
				Requests int64  `json:"requests"`
			} `json:"keys"`
		}
		s.waitFor("usage to be aggregated", func() bool {
			s.mgmtJSON(http.MethodGet, "/usage/keys?bucket=hour", "", &body)
			return len(body.Keys) == 1 && body.Keys[0].Requests > 0
		})
		_, raw := s.mgmt(http.MethodGet, "/usage/keys", "")
		if strings.Contains(string(raw), clientKey) {
			t.Fatalf("the response has the raw key: %s", raw)
		}
		if body.Keys[0].Masked != "sk-...aaaa" {
			t.Fatalf("masked = %q", body.Keys[0].Masked)
		}
	})

	t.Run("health lists both providers without secrets", func(t *testing.T) {
		code, raw := s.mgmt(http.MethodGet, "/health/accounts", "")
		if code != 200 {
			t.Fatalf("%d %s", code, raw)
		}
		for _, secret := range []string{s.alice.token, s.carol.token} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("the response has a token: %s", raw)
			}
		}
		var body struct {
			Accounts []struct {
				Provider string `json:"provider"`
				Status   string `json:"status"`
			} `json:"accounts"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || len(body.Accounts) != 6 {
			t.Fatalf("accounts = %s err=%v", raw, err)
		}
	})

	t.Run("no response on any route holds a token", func(t *testing.T) {
		for _, path := range []string{"/router/state", "/quota/pools", "/alerts", "/alerts/events", "/usage/keys"} {
			code, raw := s.mgmt(http.MethodGet, path, "")
			if code != 200 {
				t.Fatalf("%s: %d %s", path, code, raw)
			}
			for _, a := range []*account{s.alice, s.bob, s.carol, s.dave} {
				if strings.Contains(string(raw), a.token) {
					t.Fatalf("%s holds a token: %s", path, raw)
				}
			}
		}
	})

	t.Run("management routes need the key", func(t *testing.T) {
		resp, err := s.client.Get(s.base + "/v8/management/tpr/router/state")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})

	t.Run("state survives on disk with no secrets", func(t *testing.T) {
		pool := s.router.Pool("codex")
		pool.Engine.MarkDirty()
		pool.Flush()
		matches, err := filepath.Glob(filepath.Join(home.DataDir(), "router", "*"))
		if err != nil || len(matches) == 0 {
			t.Fatalf("no state files: %v %v", matches, err)
		}
		for _, path := range matches {
			raw, errRead := os.ReadFile(path)
			if errRead != nil {
				t.Fatal(errRead)
			}
			for _, a := range []*account{s.alice, s.bob, s.carol, s.dave} {
				if strings.Contains(string(raw), a.token) {
					t.Fatalf("%s holds a token", path)
				}
			}
		}
	})
}
