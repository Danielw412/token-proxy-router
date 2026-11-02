package tailscale

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
)

func routeEnv(t *testing.T, host string, fake *fakeCLI) *gin.Engine {
	t.Helper()
	// These cover the route logic on every OS. Whether an OS supports the feature has
	// its own test below.
	savedSupported := identitySupported
	identitySupported = true
	t.Cleanup(func() { identitySupported = savedSupported })
	dir := t.TempDir()
	t.Setenv(home.EnvHome, dir)
	if err := home.Ensure(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("host: \""+host+"\"\nport: 8317\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setup.SetActiveConfigPath(path)
	saved := defaultClient
	defaultClient = fake.client()
	t.Cleanup(func() {
		defaultClient = saved
		setup.SetActiveConfigPath("")
	})
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/tailscale", handleGet)
	engine.POST("/tailscale/expose", handleExpose)
	engine.DELETE("/tailscale/expose", handleUnexpose)
	engine.PUT("/tailscale/identity", handleIdentity)
	return engine
}

func call(engine *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, r)
	return rr
}

func running() fakeResponse {
	return fakeResponse{stdout: `{"BackendState":"Running","Self":{"DNSName":"host.example.ts.net."}}`}
}

func TestGetReportsExposure(t *testing.T) {
	fake := &fakeCLI{responses: map[string]fakeResponse{"status --json": running(), "serve status --json": {stdout: serveOurs}}}
	engine := routeEnv(t, "127.0.0.1", fake)
	rr := call(engine, http.MethodGet, "/tailscale", "")
	var report Report
	if err := json.Unmarshal(rr.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Exposed || report.URL != "https://host.example.ts.net:8443" || report.Port != 8443 || !report.CanExpose {
		t.Fatalf("report = %+v", report)
	}
	if report.Identity.Enabled || !report.Identity.Available {
		t.Fatalf("identity = %+v", report.Identity)
	}
}

func TestIdentityEnableValidates(t *testing.T) {
	fake := &fakeCLI{responses: map[string]fakeResponse{"status --json": running(), "serve status --json": {stdout: serveEmpty}}}
	engine := routeEnv(t, "127.0.0.1", fake)
	if rr := call(engine, http.MethodPut, "/tailscale/identity", `{"enabled":true,"allowed_logins":[]}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rr.Code)
	}
	if rr := call(engine, http.MethodPut, "/tailscale/identity", `{"enabled":true,"allowed_logins":["alice@example.com"]}`); rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body)
	}
	if state := LoadState(); !state.Identity.Enabled || state.Identity.AllowedLogins[0] != "alice@example.com" {
		t.Fatalf("state = %+v", state)
	}
}

func TestIdentityEnableRefusedOnNonLoopbackBind(t *testing.T) {
	fake := &fakeCLI{responses: map[string]fakeResponse{"status --json": running(), "serve status --json": {stdout: serveEmpty}}}
	engine := routeEnv(t, "0.0.0.0", fake)
	rr := call(engine, http.MethodPut, "/tailscale/identity", `{"enabled":true,"allowed_logins":["alice@example.com"]}`)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "loopback") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
	}
	if LoadState().Identity.Enabled {
		t.Fatal("identity must stay off")
	}
}

func TestIdentityIsRefusedWhereAConnectionsOwnerCantBeChecked(t *testing.T) {
	fake := &fakeCLI{responses: map[string]fakeResponse{"status --json": running(), "serve status --json": {stdout: "{}"}}}
	engine := routeEnv(t, "127.0.0.1", fake)
	identitySupported = false

	rr := call(engine, http.MethodPut, "/tailscale/identity", `{"enabled":true,"allowed_logins":["alice@example.com"]}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	rr = call(engine, http.MethodGet, "/tailscale", "")
	var report Report
	if err := json.Unmarshal(rr.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Identity.Available {
		t.Fatalf("identity = %+v", report.Identity)
	}
}

func TestExposeRoutePersistsPort(t *testing.T) {
	fake := &fakeCLI{responses: map[string]fakeResponse{"status --json": running(), "serve status --json": {stdout: serveEmpty}}}
	engine := routeEnv(t, "127.0.0.1", fake)
	if rr := call(engine, http.MethodPost, "/tailscale/expose", `{"port":9443}`); rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
	}
	if !fake.called("serve --bg --https=9443 http://127.0.0.1:8317") {
		t.Fatalf("calls = %v", fake.calls)
	}
	if LoadState().Port != 9443 {
		t.Fatal("port should be remembered")
	}
}

func TestExposeRouteReportsPermissionFix(t *testing.T) {
	fake := &fakeCLI{responses: map[string]fakeResponse{
		"serve status --json":                           {stdout: serveEmpty},
		"serve --bg --https=8443 http://127.0.0.1:8317": {stderr: "Access denied: serve config denied", err: context.Canceled},
	}}
	engine := routeEnv(t, "127.0.0.1", fake)
	rr := call(engine, http.MethodPost, "/tailscale/expose", ``)
	if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), "tailscale set --operator") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
	}
}

func TestUnexposeRouteOnlyTouchesOurs(t *testing.T) {
	fake := &fakeCLI{responses: map[string]fakeResponse{"status --json": running(), "serve status --json": {stdout: serveBoth}}}
	engine := routeEnv(t, "127.0.0.1", fake)
	if rr := call(engine, http.MethodDelete, "/tailscale/expose", ""); rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
	}
	if !fake.called("serve --https=8443 off") || fake.called("serve --https=443 off") {
		t.Fatalf("calls = %v", fake.calls)
	}
}
