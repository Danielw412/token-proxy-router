// Package tailscale publishes the server on the user's tailnet and implements
// identity sign-in for the management API. It only ever talks to the local
// tailscale CLI, and it only ever changes the one serve mapping it owns.
package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
)

const (
	// DefaultPort is the tailnet HTTPS port `tpr expose` uses.
	DefaultPort = 8443
	// statusTimeout bounds read-only CLI calls.
	statusTimeout = 5 * time.Second
	// serveTimeout bounds `tailscale serve`, which blocks on a prompt when serve
	// has not been enabled for the tailnet yet.
	serveTimeout = 20 * time.Second
	// whoisTimeout bounds the identity lookup made during a request.
	whoisTimeout = 3 * time.Second
	// taggedLogin is the login Tailscale reports for tagged devices. It is shared
	// by every tagged node, so it can never identify a person.
	taggedLogin = "tagged-devices"
)

// ErrNotInstalled means no tailscale CLI was found.
var ErrNotInstalled = errors.New("tailscale is not installed; get it from https://tailscale.com/download")

// Client wraps the tailscale CLI.
type Client struct {
	// Run executes the CLI. Tests inject a fake.
	Run setup.Runner
	// Binary returns the CLI path, or "" when tailscale is not installed.
	Binary func() string
	// GOOS picks OS-specific guidance. Empty means the current OS.
	GOOS string
}

// NewClient returns a client for the real CLI.
func NewClient() *Client {
	return &Client{Run: setup.ExecRunner, Binary: FindBinary}
}

// FindBinary locates the CLI on PATH or in its usual install location.
func FindBinary() string {
	name := "tailscale"
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		candidates = []string{`C:\Program Files\Tailscale\tailscale.exe`}
	case "darwin":
		candidates = []string{"/Applications/Tailscale.app/Contents/MacOS/Tailscale"}
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func (c *Client) goos() string {
	if c.GOOS != "" {
		return c.GOOS
	}
	return runtime.GOOS
}

func (c *Client) run(ctx context.Context, timeout time.Duration, args ...string) ([]byte, []byte, error) {
	binary := c.Binary()
	if binary == "" {
		return nil, nil, ErrNotInstalled
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.Run(ctx, binary, args...)
}

// Status is the state of the local tailscale install.
type Status struct {
	Installed bool
	LoggedIn  bool
	DNSName   string
	Hint      string
}

type statusJSON struct {
	BackendState string `json:"BackendState"`
	Self         *struct {
		DNSName string `json:"DNSName"`
	} `json:"Self"`
}

// Status reads `tailscale status --json`.
func (c *Client) Status(ctx context.Context) Status {
	if c.Binary() == "" {
		return Status{Hint: "Tailscale is not installed. Get it from https://tailscale.com/download, then sign in."}
	}
	stdout, stderr, err := c.run(ctx, statusTimeout, "status", "--json")
	if err != nil && len(stdout) == 0 {
		return Status{Installed: true, Hint: "Tailscale is installed but not reachable (" + firstLine(stderr, err) + "). Start it, then run `tailscale up`."}
	}
	var parsed statusJSON
	if errParse := json.Unmarshal(stdout, &parsed); errParse != nil {
		return Status{Installed: true, Hint: "Could not read `tailscale status --json`. Is your Tailscale version current?"}
	}
	status := Status{Installed: true}
	if parsed.BackendState != "Running" {
		status.Hint = "Tailscale is not signed in (state " + parsed.BackendState + "). Run `tailscale up`."
		return status
	}
	status.LoggedIn = true
	if parsed.Self != nil {
		status.DNSName = strings.TrimSuffix(parsed.Self.DNSName, ".")
	}
	if status.DNSName == "" {
		status.Hint = "This tailnet has no DNS name for the machine. Turn on MagicDNS in the Tailscale admin console."
	}
	return status
}

// Mapping is one published HTTPS port.
type Mapping struct {
	Port int
	// Target is the proxy target of the "/" path. It is empty for a port that
	// serves something other than a plain proxy.
	Target string
}

// pointsAt reports whether the mapping proxies "/" to this server's local port.
func (m Mapping) pointsAt(serverPort int) bool {
	parsed, err := url.Parse(m.Target)
	if err != nil || parsed.Port() != strconv.Itoa(serverPort) {
		return false
	}
	host := parsed.Hostname()
	return host == "localhost" || setup.IsLoopbackHost(host)
}

type serveJSON struct {
	TCP map[string]json.RawMessage `json:"TCP"`
	Web map[string]struct {
		Handlers map[string]struct {
			Proxy string `json:"Proxy"`
		} `json:"Handlers"`
	} `json:"Web"`
}

// Mappings lists published ports from `tailscale serve status --json`, including
// the ones that have nothing to do with this server.
func (c *Client) Mappings(ctx context.Context) ([]Mapping, error) {
	stdout, stderr, err := c.run(ctx, statusTimeout, "serve", "status", "--json")
	if err != nil {
		if errors.Is(err, ErrNotInstalled) {
			return nil, err
		}
		return nil, fmt.Errorf("read serve status: %s", firstLine(stderr, err))
	}
	var parsed serveJSON
	if errParse := json.Unmarshal(stdout, &parsed); errParse != nil {
		return nil, fmt.Errorf("read serve status: %w", errParse)
	}
	byPort := map[int]Mapping{}
	for rawPort := range parsed.TCP {
		if port, errPort := strconv.Atoi(rawPort); errPort == nil {
			byPort[port] = Mapping{Port: port}
		}
	}
	for hostPort, web := range parsed.Web {
		_, rawPort, errSplit := net.SplitHostPort(hostPort)
		if errSplit != nil {
			continue
		}
		port, errPort := strconv.Atoi(rawPort)
		if errPort != nil {
			continue
		}
		byPort[port] = Mapping{Port: port, Target: web.Handlers["/"].Proxy}
	}
	mappings := make([]Mapping, 0, len(byPort))
	for _, mapping := range byPort {
		mappings = append(mappings, mapping)
	}
	sort.Slice(mappings, func(i, j int) bool { return mappings[i].Port < mappings[j].Port })
	return mappings, nil
}

// FindExposure returns the published port that reaches this server. A preferred
// port wins when several do.
func (c *Client) FindExposure(ctx context.Context, serverPort, preferred int) (int, bool, error) {
	mappings, err := c.Mappings(ctx)
	if err != nil {
		return 0, false, err
	}
	found := 0
	for _, mapping := range mappings {
		if !mapping.pointsAt(serverPort) {
			continue
		}
		if mapping.Port == preferred {
			return mapping.Port, true, nil
		}
		if found == 0 {
			found = mapping.Port
		}
	}
	return found, found != 0, nil
}

// Expose publishes the server. It refuses a port already used for anything else.
func (c *Client) Expose(ctx context.Context, port, serverPort int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port %d is not valid", port)
	}
	mappings, err := c.Mappings(ctx)
	if err != nil {
		return err
	}
	for _, mapping := range mappings {
		if mapping.Port != port {
			continue
		}
		if mapping.pointsAt(serverPort) {
			return nil
		}
		return fmt.Errorf("tailnet port %d is already published for something else. Choose another with `tpr expose --port <port>`", port)
	}
	stdout, stderr, err := c.run(ctx, serveTimeout, "serve", "--bg",
		fmt.Sprintf("--https=%d", port), fmt.Sprintf("http://127.0.0.1:%d", serverPort))
	if err != nil {
		return c.explainServeError(stdout, stderr, err)
	}
	return nil
}

// Unexpose removes the mapping on port, and only when it points at this server.
func (c *Client) Unexpose(ctx context.Context, port, serverPort int) error {
	mappings, err := c.Mappings(ctx)
	if err != nil {
		return err
	}
	for _, mapping := range mappings {
		if mapping.Port != port {
			continue
		}
		if !mapping.pointsAt(serverPort) {
			return fmt.Errorf("tailnet port %d is published for something else, so it was left alone", port)
		}
		stdout, stderr, errRun := c.run(ctx, serveTimeout, "serve", fmt.Sprintf("--https=%d", port), "off")
		if errRun != nil {
			return c.explainServeError(stdout, stderr, errRun)
		}
		return nil
	}
	return nil
}

// OperatorFix returns the exact command that lets this user change serve settings.
func (c *Client) OperatorFix() string {
	if c.goos() == "windows" {
		return `tailscale set --operator=%USERNAME%  (in a terminal opened with "Run as administrator")`
	}
	return "sudo tailscale set --operator=$USER"
}

func (c *Client) explainServeError(stdout, stderr []byte, err error) error {
	if errors.Is(err, ErrNotInstalled) {
		return err
	}
	combined := string(stdout) + "\n" + string(stderr)
	lower := strings.ToLower(combined)
	switch {
	case strings.Contains(lower, "access denied"), strings.Contains(lower, "--operator"), strings.Contains(lower, "permission denied"):
		return fmt.Errorf("Tailscale does not let this user change serve settings. Run this once, then try again:\n  %s", c.OperatorFix()) //nolint:staticcheck // shown to the user as a sentence
	case strings.Contains(lower, "not enabled") || strings.Contains(lower, "login.tailscale.com"):
		if link := findURL(combined); link != "" {
			return fmt.Errorf("Tailscale Serve is not enabled for this tailnet. Enable it here, then try again: %s", link) //nolint:staticcheck // shown to the user as a sentence
		}
		return errors.New("Tailscale Serve is not enabled for this tailnet. Enable HTTPS and Serve in the Tailscale admin console, then try again") //nolint:staticcheck // shown to the user as a sentence
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("`tailscale serve` did not finish. Run it by hand to see what it is waiting for")
	}
	return fmt.Errorf("tailscale serve failed: %s", firstLine(stderr, err))
}

type whoisJSON struct {
	UserProfile *struct {
		LoginName string `json:"LoginName"`
	} `json:"UserProfile"`
}

// Whois asks the local Tailscale daemon who owns a tailnet address.
func (c *Client) Whois(ctx context.Context, addr string) (string, error) {
	stdout, stderr, err := c.run(ctx, whoisTimeout, "whois", "--json", addr)
	if err != nil {
		return "", fmt.Errorf("whois: %s", firstLine(stderr, err))
	}
	var parsed whoisJSON
	if errParse := json.Unmarshal(stdout, &parsed); errParse != nil {
		return "", fmt.Errorf("whois: %w", errParse)
	}
	if parsed.UserProfile == nil || parsed.UserProfile.LoginName == "" {
		return "", errors.New("whois: no user for that address")
	}
	return parsed.UserProfile.LoginName, nil
}

func firstLine(stderr []byte, err error) string {
	for _, line := range strings.Split(string(stderr), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	if err != nil {
		return err.Error()
	}
	return "unknown error"
}

func findURL(text string) string {
	for _, field := range strings.Fields(text) {
		if strings.HasPrefix(field, "https://") {
			return strings.TrimRight(field, ".,)")
		}
	}
	return ""
}
