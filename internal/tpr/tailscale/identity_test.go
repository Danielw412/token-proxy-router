package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	testLogin = "alice@example.com"
	testAddr  = "100.64.0.7"
	testHost  = "host.example.ts.net:8443"
	testDNS   = "host.example.ts.net"
)

func newChecker(whois func(context.Context, string) (string, error)) *Checker {
	return &Checker{
		Settings: func() Identity { return Identity{Enabled: true, AllowedLogins: []string{testLogin}} },
		Whois:    whois,
		DNSName:  func(context.Context) string { return testDNS },
	}
}

func okWhois(context.Context, string) (string, error) { return testLogin, nil }

// serveRequest is what tailscale serve forwards for a tailnet user.
func serveRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v8/management/config", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	r.Host = testHost
	r.Header.Set(headerLogin, testLogin)
	r.Header.Set(headerForward, testAddr)
	return r
}

func TestIdentityAcceptsTheRealPath(t *testing.T) {
	login, ok := newChecker(okWhois).Check(serveRequest(), "127.0.0.1")
	if !ok || login != testLogin {
		t.Fatalf("login=%q ok=%v", login, ok)
	}
}

func TestIdentityAcceptsSameOriginBrowser(t *testing.T) {
	r := serveRequest()
	r.Header.Set("Origin", "https://"+testHost)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if _, ok := newChecker(okWhois).Check(r, "127.0.0.1"); !ok {
		t.Fatal("the panel's own requests must pass")
	}
}

func TestIdentityRejections(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*http.Request)
		bindHost string
		checker  func() *Checker
	}{
		{name: "spoofed headers from a non-loopback peer", mutate: func(r *http.Request) { r.RemoteAddr = "100.64.0.7:50000" }},
		{name: "spoofed headers from a LAN peer", mutate: func(r *http.Request) { r.RemoteAddr = "192.0.2.10:50000" }},
		{name: "malformed peer", mutate: func(r *http.Request) { r.RemoteAddr = "not-an-address" }},
		{name: "login not allowed", mutate: func(r *http.Request) { r.Header.Set(headerLogin, "mallory@example.com") }},
		{name: "login header missing", mutate: func(r *http.Request) { r.Header.Del(headerLogin) }},
		{name: "login header empty", mutate: func(r *http.Request) { r.Header.Set(headerLogin, "  ") }},
		{name: "login header repeated", mutate: func(r *http.Request) { r.Header.Add(headerLogin, "mallory@example.com") }},
		{name: "forwarded address missing", mutate: func(r *http.Request) { r.Header.Del(headerForward) }},
		{name: "forwarded address is a list", mutate: func(r *http.Request) { r.Header.Set(headerForward, testAddr+", 192.0.2.1") }},
		{name: "forwarded header repeated", mutate: func(r *http.Request) { r.Header.Add(headerForward, "100.64.0.8") }},
		{name: "forwarded address not an ip", mutate: func(r *http.Request) { r.Header.Set(headerForward, "evil; rm -rf") }},
		{name: "forwarded address is a flag", mutate: func(r *http.Request) { r.Header.Set(headerForward, "--help") }},
		{name: "forwarded address outside tailnet", mutate: func(r *http.Request) { r.Header.Set(headerForward, "192.0.2.1") }},
		{name: "forwarded address loopback", mutate: func(r *http.Request) { r.Header.Set(headerForward, "127.0.0.1") }},
		{name: "funnel request", mutate: func(r *http.Request) { r.Header.Set(headerFunnel, "?1") }},
		{name: "cross-site browser", mutate: func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }},
		{name: "same-site browser", mutate: func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }},
		{name: "foreign origin", mutate: func(r *http.Request) { r.Header.Set("Origin", "https://evil.example.com") }},
		{name: "plain http origin", mutate: func(r *http.Request) { r.Header.Set("Origin", "http://"+testHost) }},
		{name: "null origin", mutate: func(r *http.Request) { r.Header.Set("Origin", "null") }},
		{name: "dns rebinding host", mutate: func(r *http.Request) {
			r.Host = "evil.example.com:8317"
			r.Header.Set("Origin", "https://evil.example.com:8317")
		}},
		{name: "direct loopback host", mutate: func(r *http.Request) { r.Host = "127.0.0.1:8317" }},
		{name: "non-loopback bind", bindHost: "0.0.0.0"},
		{name: "bind all interfaces", bindHost: "<empty>"},
		{name: "bind LAN address", bindHost: "192.0.2.5"},
		{name: "feature disabled", checker: func() *Checker {
			c := newChecker(okWhois)
			c.Settings = func() Identity { return Identity{Enabled: false, AllowedLogins: []string{testLogin}} }
			return c
		}},
		{name: "empty allow list", checker: func() *Checker {
			c := newChecker(okWhois)
			c.Settings = func() Identity { return Identity{Enabled: true} }
			return c
		}},
		{name: "whois mismatch", checker: func() *Checker {
			return newChecker(func(context.Context, string) (string, error) { return "mallory@example.com", nil })
		}},
		{name: "whois failure", checker: func() *Checker {
			return newChecker(func(context.Context, string) (string, error) { return "", errors.New("no match") })
		}},
		{name: "whois empty login", checker: func() *Checker {
			return newChecker(func(context.Context, string) (string, error) { return "", nil })
		}},
		{name: "unknown tailnet name", checker: func() *Checker {
			c := newChecker(okWhois)
			c.DNSName = func(context.Context) string { return "" }
			return c
		}},
		{name: "tagged device allowed by mistake", mutate: func(r *http.Request) { r.Header.Set(headerLogin, taggedLogin) }, checker: func() *Checker {
			c := newChecker(func(context.Context, string) (string, error) { return taggedLogin, nil })
			c.Settings = func() Identity { return Identity{Enabled: true, AllowedLogins: []string{taggedLogin}} }
			return c
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := serveRequest()
			if tc.mutate != nil {
				tc.mutate(r)
			}
			checker := newChecker(okWhois)
			if tc.checker != nil {
				checker = tc.checker()
			}
			bind := "127.0.0.1"
			switch tc.bindHost {
			case "":
			case "<empty>":
				bind = ""
			default:
				bind = tc.bindHost
			}
			if login, ok := checker.Check(r, bind); ok || login != "" {
				t.Fatalf("must fail closed: login=%q ok=%v", login, ok)
			}
		})
	}
}

func TestIdentityNeverCallsWhoisBeforeCheapChecksPass(t *testing.T) {
	called := false
	checker := newChecker(func(context.Context, string) (string, error) {
		called = true
		return testLogin, nil
	})
	r := serveRequest()
	r.RemoteAddr = "192.0.2.10:50000"
	checker.Check(r, "127.0.0.1")
	r = serveRequest()
	r.Header.Set(headerLogin, "mallory@example.com")
	checker.Check(r, "127.0.0.1")
	r = serveRequest()
	r.Header.Set(headerForward, "--help")
	checker.Check(r, "127.0.0.1")
	if called {
		t.Fatal("whois must only run for plausible requests")
	}
}

func TestIdentityLoginComparisonIsCaseInsensitive(t *testing.T) {
	checker := newChecker(func(context.Context, string) (string, error) { return "Alice@Example.com", nil })
	if _, ok := checker.Check(serveRequest(), "127.0.0.1"); !ok {
		t.Fatal("logins differ only in case")
	}
}

func TestWhoisResultIsCachedThenExpires(t *testing.T) {
	calls := 0
	now := time.Unix(1000, 0)
	checker := newChecker(func(context.Context, string) (string, error) {
		calls++
		return testLogin, nil
	})
	checker.Now = func() time.Time { return now }
	for range 3 {
		if _, ok := checker.Check(serveRequest(), "127.0.0.1"); !ok {
			t.Fatal("expected success")
		}
	}
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
	now = now.Add(whoisCacheTTL + time.Second)
	if _, ok := checker.Check(serveRequest(), "127.0.0.1"); !ok {
		t.Fatal("expected success")
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestWhoisFailureIsNotCached(t *testing.T) {
	fail := true
	checker := newChecker(func(context.Context, string) (string, error) {
		if fail {
			return "", errors.New("daemon down")
		}
		return testLogin, nil
	})
	if _, ok := checker.Check(serveRequest(), "127.0.0.1"); ok {
		t.Fatal("expected failure")
	}
	fail = false
	if _, ok := checker.Check(serveRequest(), "127.0.0.1"); !ok {
		t.Fatal("a later success must not be blocked")
	}
}

func TestServeSessionHidesSpoofedLogin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	saved := defaultChecker
	defer func() { defaultChecker = saved }()
	defaultChecker = newChecker(okWhois)

	engine := gin.New()
	engine.GET("/session", func(c *gin.Context) { ServeSession(c, "127.0.0.1") })

	r := serveRequest()
	r.URL.Path = "/session"
	r.RequestURI = "/session"
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, r)
	var body struct {
		IdentityLogin bool   `json:"identity_login"`
		Login         string `json:"login"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.IdentityLogin || body.Login != testLogin {
		t.Fatalf("body = %+v", body)
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("session must not be cached")
	}

	spoofed := serveRequest()
	spoofed.URL.Path = "/session"
	spoofed.RequestURI = "/session"
	spoofed.RemoteAddr = "192.0.2.10:50000"
	rr = httptest.NewRecorder()
	engine.ServeHTTP(rr, spoofed)
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.IdentityLogin || body.Login != "" {
		t.Fatalf("spoofed request got identity: %+v", body)
	}
}

func TestNormalizeLogins(t *testing.T) {
	got, err := NormalizeLogins([]string{" alice@example.com ", "ALICE@example.com", "", "bob@github"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	for _, bad := range []string{"not a login", taggedLogin} {
		if _, err := NormalizeLogins([]string{bad}); err == nil {
			t.Fatalf("%q should be rejected", bad)
		}
	}
}
