package health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/store"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

var now = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func oauth(meta map[string]any) *coreauth.Auth {
	base := map[string]any{"email": "alice@example.com", "refresh_token": "fake-refresh"}
	for k, v := range meta {
		base[k] = v
	}
	return &coreauth.Auth{ID: "claude-a.json", Provider: "claude", Status: coreauth.StatusActive, Metadata: base,
		Attributes: map[string]string{coreauth.AttributePath: "/example/claude-a.json"}}
}

func TestStatusRules(t *testing.T) {
	soon := now.Add(time.Hour).Format(time.RFC3339)
	past := now.Add(-time.Hour).Format(time.RFC3339)
	cases := []struct {
		name     string
		auth     func() *coreauth.Auth
		activity Activity
		want     string
	}{
		{"healthy", func() *coreauth.Auth { return oauth(nil) }, Activity{LastSuccessAt: now}, StatusOK},
		{"turned off", func() *coreauth.Auth { a := oauth(nil); a.Disabled = true; return a }, Activity{}, StatusDisabled},
		{"status disabled", func() *coreauth.Auth { a := oauth(nil); a.Status = coreauth.StatusDisabled; return a }, Activity{}, StatusDisabled},
		{"sign-in rejected", func() *coreauth.Auth { a := oauth(nil); a.RequiresSignIn = true; return a }, Activity{}, StatusExpired},
		{"expired and cannot renew", func() *coreauth.Auth {
			return oauth(map[string]any{"refresh_token": "", "expired": past, "access_token": "x"})
		}, Activity{}, StatusExpired},
		{"expired but renews itself", func() *coreauth.Auth {
			return oauth(map[string]any{"expired": past, "access_token": "x"})
		}, Activity{}, StatusOK},
		{"expiring and cannot renew", func() *coreauth.Auth {
			return oauth(map[string]any{"refresh_token": "", "expired": soon, "access_token": "x"})
		}, Activity{}, StatusExpiring},
		{"renewal failed", func() *coreauth.Auth {
			a := oauth(nil)
			a.RefreshError = &coreauth.Error{Message: "nope"}
			return a
		}, Activity{}, StatusExpiring},
		{"recent failure", func() *coreauth.Auth { return oauth(nil) }, Activity{LastErrorAt: now.Add(-time.Minute), LastError: "HTTP 500"}, StatusFailing},
		{"failure since recovered", func() *coreauth.Auth { return oauth(nil) }, Activity{LastErrorAt: now.Add(-2 * time.Minute), LastSuccessAt: now.Add(-time.Minute), LastError: "x"}, StatusOK},
		{"old failure", func() *coreauth.Auth { return oauth(nil) }, Activity{LastErrorAt: now.Add(-time.Hour), LastError: "x"}, StatusOK},
		{"status error", func() *coreauth.Auth {
			a := oauth(nil)
			a.Status = coreauth.StatusError
			a.LastError = &coreauth.Error{Message: "401 unauthorized"}
			return a
		}, Activity{}, StatusFailing},
		{"cooling down", func() *coreauth.Auth {
			a := oauth(nil)
			a.Unavailable, a.NextRetryAfter = true, now.Add(time.Minute)
			return a
		}, Activity{}, StatusCooling},
		{"quota cooldown", func() *coreauth.Auth {
			a := oauth(nil)
			a.Quota.Exceeded, a.Quota.NextRecoverAt = true, now.Add(time.Hour)
			return a
		}, Activity{}, StatusCooling},
		{"cooldown over", func() *coreauth.Auth {
			a := oauth(nil)
			a.Unavailable, a.NextRetryAfter = true, now.Add(-time.Minute)
			return a
		}, Activity{}, StatusOK},
		{"disabled wins over everything", func() *coreauth.Auth {
			a := oauth(nil)
			a.Disabled, a.RequiresSignIn, a.Unavailable, a.NextRetryAfter = true, true, true, now.Add(time.Hour)
			return a
		}, Activity{LastErrorAt: now}, StatusDisabled},
	}
	for _, c := range cases {
		got := Derive(now, c.auth(), c.activity)
		if got.Status != c.want {
			t.Errorf("%s: status = %s (%s), want %s", c.name, got.Status, got.Reason, c.want)
		}
		if got.Status != StatusOK && got.Reason == "" {
			t.Errorf("%s: a problem needs a reason", c.name)
		}
	}
}

func TestAPIKeyCredentialsHaveNoLoginToRepeat(t *testing.T) {
	a := &coreauth.Auth{ID: "key", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "sk-fake"}}
	got := Derive(now, a, Activity{})
	if got.Reauth != nil || got.Status != StatusOK || got.TokenExpiresAt != nil {
		t.Fatalf("got %+v", got)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "sk-fake") {
		t.Fatalf("response has the key: %s", raw)
	}
}

func TestRowCarriesReauthAndNoSecrets(t *testing.T) {
	a := oauth(map[string]any{"access_token": "secret-access-value", "expired": now.Add(24 * time.Hour).Format(time.RFC3339)})
	got := Derive(now, a, Activity{LastSuccessAt: now})
	if got.Reauth == nil || got.Reauth.Provider != "claude" || got.Email != "alice@example.com" {
		t.Fatalf("got %+v", got)
	}
	if got.TokenExpiresAt == nil {
		t.Fatalf("expiry missing: %+v", got)
	}
	raw, _ := json.Marshal(got)
	for _, secret := range []string{"secret-access-value", "fake-refresh"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("response has a secret: %s", raw)
		}
	}
}

type staticLister []*coreauth.Auth

func (l staticLister) List() []*coreauth.Auth { return l }

func TestUsageUpdatesActivityAndSanitizes(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tpr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	s := New(staticLister{oauth(nil), {ID: "gemini-x", Provider: "gemini"}}, st, func() time.Time { return now })
	body := `{"error":{"message":"bad token sk-ant-abcdefghijklmnop for Authorization: Bearer abc.def.ghi"},"request":{"prompt":"PRIVATE TEXT"}}`
	s.HandleUsage(context.Background(), usage.Record{AuthID: "claude-a.json", Provider: "claude", RequestedAt: now.Add(-time.Minute), Failed: true, Fail: usage.Failure{StatusCode: 401, Body: body}})
	s.HandleUsage(context.Background(), usage.Record{AuthID: "gemini-x", Provider: "gemini", Failed: true, Fail: usage.Failure{StatusCode: 500}})

	accounts := s.Accounts()
	if len(accounts) != 1 {
		t.Fatalf("other providers stay out of it: %+v", accounts)
	}
	got := accounts[0]
	if got.Status != StatusFailing || got.LastErrorAt == nil {
		t.Fatalf("got %+v", got)
	}
	for _, secret := range []string{"sk-ant-abcdefgh", "abc.def.ghi", "PRIVATE TEXT"} {
		if strings.Contains(got.LastError, secret) || strings.Contains(got.Reason, secret) {
			t.Fatalf("last_error kept %q: %q / %q", secret, got.LastError, got.Reason)
		}
	}
	if len(got.LastError) > 210 {
		t.Fatalf("last_error too long: %d", len(got.LastError))
	}

	s.HandleUsage(context.Background(), usage.Record{AuthID: "claude-a.json", Provider: "claude", RequestedAt: now})
	if got = s.Accounts()[0]; got.Status != StatusOK || got.LastSuccessAt == nil {
		t.Fatalf("after a success: %+v", got)
	}

	// Activity survives a restart.
	s.save()
	again := New(staticLister{oauth(nil)}, st, func() time.Time { return now })
	if row := again.Accounts()[0]; row.LastSuccessAt == nil || row.LastErrorAt == nil {
		t.Fatalf("after restart: %+v", row)
	}
}

func TestFailureWithoutABodyStillSaysSomething(t *testing.T) {
	if got := failureText(usage.Failure{StatusCode: 502}); got != "upstream returned HTTP Bad Gateway" {
		t.Fatalf("got %q", got)
	}
	if got := failureText(usage.Failure{}); got != "request failed" {
		t.Fatalf("got %q", got)
	}
}

func TestWorksWithoutAStore(t *testing.T) {
	s := New(staticLister{oauth(nil)}, nil, func() time.Time { return now })
	s.HandleUsage(context.Background(), usage.Record{AuthID: "claude-a.json", Provider: "claude", RequestedAt: now})
	s.save()
	if got := s.Accounts()[0]; got.LastSuccessAt == nil {
		t.Fatalf("got %+v", got)
	}
}

func TestAPIContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.GET("/health/accounts", handleAccounts)
	do := func() (int, map[string]any) {
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health/accounts", nil))
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return w.Code, out
	}
	current.Store(nil)
	if code, _ := do(); code != 503 {
		t.Fatalf("not started: %d", code)
	}
	current.Store(New(staticLister{oauth(nil)}, nil, func() time.Time { return now }))
	t.Cleanup(func() { current.Store(nil) })
	code, out := do()
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	row := out["accounts"].([]any)[0].(map[string]any)
	for _, key := range []string{"id", "provider", "label", "email", "status", "reason", "last_success_at", "last_error_at", "last_error", "token_expires_at", "reauth"} {
		if _, ok := row[key]; !ok {
			t.Errorf("row is missing %q", key)
		}
	}
}
