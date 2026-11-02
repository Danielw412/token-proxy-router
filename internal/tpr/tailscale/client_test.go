package tailscale

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeCLI struct {
	responses map[string]fakeResponse
	calls     []string
}

type fakeResponse struct {
	stdout, stderr string
	err            error
}

func (f *fakeCLI) client() *Client {
	return &Client{
		GOOS:   "linux",
		Binary: func() string { return "tailscale" },
		Run: func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
			key := strings.Join(args, " ")
			f.calls = append(f.calls, key)
			response := f.responses[key]
			return []byte(response.stdout), []byte(response.stderr), response.err
		},
	}
}

func (f *fakeCLI) called(key string) bool {
	for _, call := range f.calls {
		if call == key {
			return true
		}
	}
	return false
}

const (
	serveEmpty = `{}`
	serveOurs  = `{"TCP":{"8443":{"HTTPS":true}},"Web":{"host.example.ts.net:8443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:8317"}}}}}`
	serveOther = `{"TCP":{"8443":{"HTTPS":true},"443":{"HTTPS":true}},"Web":{"host.example.ts.net:8443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:3000"}}},"host.example.ts.net:443":{"Handlers":{"/":{"Path":"/var/www"}}}}}`
	serveBoth  = `{"Web":{"host.example.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:3000"}}},"host.example.ts.net:8443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:8317"}}}}}`
)

func TestStatusStates(t *testing.T) {
	cases := []struct {
		name      string
		response  fakeResponse
		loggedIn  bool
		dnsName   string
		hintWants string
	}{
		{"running", fakeResponse{stdout: `{"BackendState":"Running","Self":{"DNSName":"host.example.ts.net."}}`}, true, "host.example.ts.net", ""},
		{"logged out", fakeResponse{stdout: `{"BackendState":"NeedsLogin"}`}, false, "", "tailscale up"},
		{"no daemon", fakeResponse{stderr: "failed to connect to local tailscaled\n", err: errors.New("exit 1")}, false, "", "not reachable"},
		{"garbage", fakeResponse{stdout: `not json`}, false, "", "Could not read"},
		{"no magic dns", fakeResponse{stdout: `{"BackendState":"Running","Self":{"DNSName":""}}`}, true, "", "MagicDNS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeCLI{responses: map[string]fakeResponse{"status --json": tc.response}}
			status := fake.client().Status(context.Background())
			if !status.Installed || status.LoggedIn != tc.loggedIn || status.DNSName != tc.dnsName {
				t.Fatalf("status = %+v", status)
			}
			if tc.hintWants != "" && !strings.Contains(status.Hint, tc.hintWants) {
				t.Fatalf("hint = %q", status.Hint)
			}
		})
	}
}

func TestStatusNotInstalled(t *testing.T) {
	c := &Client{Binary: func() string { return "" }}
	status := c.Status(context.Background())
	if status.Installed || !strings.Contains(status.Hint, "not installed") {
		t.Fatalf("status = %+v", status)
	}
	if err := c.Expose(context.Background(), 8443, 8317); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v", err)
	}
}

func TestExposeRunsTheDocumentedCommand(t *testing.T) {
	fake := &fakeCLI{responses: map[string]fakeResponse{"serve status --json": {stdout: serveEmpty}}}
	if err := fake.client().Expose(context.Background(), 8443, 8317); err != nil {
		t.Fatal(err)
	}
	if !fake.called("serve --bg --https=8443 http://127.0.0.1:8317") {
		t.Fatalf("calls = %v", fake.calls)
	}
}

func TestExposeIsIdempotentForOurMapping(t *testing.T) {
	fake := &fakeCLI{responses: map[string]fakeResponse{"serve status --json": {stdout: serveOurs}}}
	if err := fake.client().Expose(context.Background(), 8443, 8317); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("should not change anything: %v", fake.calls)
	}
}

func TestExposeRefusesSomeoneElsesPort(t *testing.T) {
	fake := &fakeCLI{responses: map[string]fakeResponse{"serve status --json": {stdout: serveOther}}}
	err := fake.client().Expose(context.Background(), 8443, 8317)
	if err == nil || !strings.Contains(err.Error(), "already published") {
		t.Fatalf("err = %v", err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("must not touch other mappings: %v", fake.calls)
	}
}

func TestExposeOperatorErrorGivesExactFix(t *testing.T) {
	const denied = "Access denied: serve config denied\nUse 'sudo tailscale serve' or set operator with 'sudo tailscale set --operator=$USER'\n"
	fake := &fakeCLI{responses: map[string]fakeResponse{
		"serve status --json":                           {stdout: serveEmpty},
		"serve --bg --https=8443 http://127.0.0.1:8317": {stderr: denied, err: errors.New("exit 1")},
	}}
	err := fake.client().Expose(context.Background(), 8443, 8317)
	if err == nil || !strings.Contains(err.Error(), "sudo tailscale set --operator=$USER") {
		t.Fatalf("err = %v", err)
	}
	winClient := fake.client()
	winClient.GOOS = "windows"
	if err := winClient.Expose(context.Background(), 8443, 8317); err == nil || !strings.Contains(err.Error(), "Run as administrator") {
		t.Fatalf("windows err = %v", err)
	}
}

func TestExposeServeNotEnabledShowsLink(t *testing.T) {
	const link = "https://login.tailscale.com/f/serve?node=abc"
	fake := &fakeCLI{responses: map[string]fakeResponse{
		"serve status --json":                           {stdout: serveEmpty},
		"serve --bg --https=8443 http://127.0.0.1:8317": {stdout: "Serve is not enabled on your tailnet.\nTo enable, visit:\n\n  " + link + "\n", err: context.DeadlineExceeded},
	}}
	err := fake.client().Expose(context.Background(), 8443, 8317)
	if err == nil || !strings.Contains(err.Error(), link) {
		t.Fatalf("err = %v", err)
	}
}

func TestUnexposeOnlyRemovesOurMapping(t *testing.T) {
	fake := &fakeCLI{responses: map[string]fakeResponse{"serve status --json": {stdout: serveOurs}}}
	if err := fake.client().Unexpose(context.Background(), 8443, 8317); err != nil {
		t.Fatal(err)
	}
	if !fake.called("serve --https=8443 off") {
		t.Fatalf("calls = %v", fake.calls)
	}
}

func TestUnexposeLeavesOtherMappingAlone(t *testing.T) {
	fake := &fakeCLI{responses: map[string]fakeResponse{"serve status --json": {stdout: serveOther}}}
	err := fake.client().Unexpose(context.Background(), 8443, 8317)
	if err == nil || !strings.Contains(err.Error(), "left alone") {
		t.Fatalf("err = %v", err)
	}
	for _, call := range fake.calls {
		if strings.Contains(call, "off") || strings.Contains(call, "reset") {
			t.Fatalf("destructive call: %v", fake.calls)
		}
	}
}

func TestUnexposeMissingMappingIsANoop(t *testing.T) {
	fake := &fakeCLI{responses: map[string]fakeResponse{"serve status --json": {stdout: serveEmpty}}}
	if err := fake.client().Unexpose(context.Background(), 8443, 8317); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("calls = %v", fake.calls)
	}
}

func TestFindExposureIgnoresOthers(t *testing.T) {
	fake := &fakeCLI{responses: map[string]fakeResponse{"serve status --json": {stdout: serveBoth}}}
	port, exposed, err := fake.client().FindExposure(context.Background(), 8317, 0)
	if err != nil || !exposed || port != 8443 {
		t.Fatalf("port=%d exposed=%v err=%v", port, exposed, err)
	}
	if _, exposed, _ := fake.client().FindExposure(context.Background(), 9999, 0); exposed {
		t.Fatal("different server port must not match")
	}
}

func TestURLFor(t *testing.T) {
	c := &Client{}
	if got := c.URLFor("host.example.ts.net", 443); got != "https://host.example.ts.net" {
		t.Fatal(got)
	}
	if got := c.URLFor("host.example.ts.net", 8443); got != "https://host.example.ts.net:8443" {
		t.Fatal(got)
	}
}

func TestWhois(t *testing.T) {
	fake := &fakeCLI{responses: map[string]fakeResponse{
		"whois --json 100.64.0.1": {stdout: `{"Node":{},"UserProfile":{"LoginName":"alice@example.com"}}`},
		"whois --json 100.64.0.2": {stderr: "no match", err: errors.New("exit 1")},
		"whois --json 100.64.0.3": {stdout: `{"Node":{}}`},
	}}
	c := fake.client()
	if login, err := c.Whois(context.Background(), "100.64.0.1"); err != nil || login != "alice@example.com" {
		t.Fatalf("login=%q err=%v", login, err)
	}
	for _, addr := range []string{"100.64.0.2", "100.64.0.3"} {
		if _, err := c.Whois(context.Background(), addr); err == nil {
			t.Fatalf("%s should fail", addr)
		}
	}
}
