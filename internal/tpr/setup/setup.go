// Package setup owns first run: it creates the data folder, writes a safe
// config.yaml with random keys, and records whether the setup wizard finished.
// Other Token Proxy Router packages also use its small helpers for reading the
// active configuration and running external commands.
package setup

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"golang.org/x/crypto/bcrypt"
)

const (
	// DefaultPort is the port a new install listens on.
	DefaultPort = 8317
	// EnvPort picks the port written to a newly generated config.
	EnvPort = "TPR_PORT"
	// EnvBindHost picks the listen address written to a newly generated config. A
	// container needs it, since loopback inside one cannot be reached from outside.
	EnvBindHost = "TPR_BIND_HOST"
	// EnvManagementKey gives CLI commands the management key after the plaintext
	// copy in credentials.txt has been deleted.
	EnvManagementKey = "TPR_MANAGEMENT_KEY"
	// portSearchRange is how far above the wanted port a first run looks when the
	// wanted port is taken.
	portSearchRange = 100
)

var activeConfig atomic.Value

// SetActiveConfigPath records the config file the running process uses.
func SetActiveConfigPath(path string) { activeConfig.Store(path) }

// ActiveConfigPath returns the config file in use, defaulting to the data folder.
func ActiveConfigPath() string {
	if path, ok := activeConfig.Load().(string); ok && path != "" {
		return path
	}
	return home.ConfigPath()
}

// Keys holds the plaintext first-run secrets. It is only ever written to
// credentials.txt, never to logs.
type Keys struct {
	API        string
	Management string
}

// FirstRun describes the config that Bootstrap generated.
type FirstRun struct {
	ConfigPath      string
	CredentialsPath string
	Port            int
	WantedPort      int
}

// PanelURL returns the local management panel address.
func (f FirstRun) PanelURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/management.html", f.Port)
}

// Print writes the first-run message: where the panel is and where the keys went.
func (f FirstRun) Print(w io.Writer) {
	fmt.Fprintln(w, "Token Proxy Router: first run")
	if f.Port != f.WantedPort {
		fmt.Fprintf(w, "  Port %d is already in use (is another proxy running? try `tpr doctor`), so this install uses port %d.\n", f.WantedPort, f.Port)
	}
	fmt.Fprintf(w, "  Config: %s\n", f.ConfigPath)
	fmt.Fprintf(w, "  Keys:   %s (owner-only; show with `tpr creds`, delete with `tpr creds --forget`)\n", f.CredentialsPath)
	fmt.Fprintf(w, "  Panel:  %s\n", f.PanelURL())
}

// Bootstrap prepares the data folder and creates configPath when it is missing
// or empty. It returns nil when an existing config was left alone.
func Bootstrap(configPath string) (*FirstRun, error) {
	if err := home.Ensure(); err != nil {
		return nil, fmt.Errorf("create data folder %s: %w", home.Dir(), err)
	}
	SetActiveConfigPath(configPath)

	info, errStat := os.Stat(configPath)
	switch {
	case errStat == nil && info.IsDir():
		return nil, fmt.Errorf("config path %s is a folder; pass a file with -config", configPath)
	case errStat == nil && info.Size() > 0:
		return nil, nil
	case errStat == nil:
		// A zero-byte file is what an interrupted editor leaves; treat it as missing.
		if errRemove := os.Remove(configPath); errRemove != nil {
			return nil, fmt.Errorf("replace empty config %s: %w", configPath, errRemove)
		}
	case !errors.Is(errStat, os.ErrNotExist):
		return nil, fmt.Errorf("check config %s: %w", configPath, errStat)
	}

	keys, errKeys := GenerateKeys()
	if errKeys != nil {
		return nil, errKeys
	}
	wanted, errPort := wantedPort()
	if errPort != nil {
		return nil, errPort
	}
	port := findFreePort(wanted)
	content, errRender := RenderConfig(bindHost(), port, home.AuthDir(), keys)
	if errRender != nil {
		return nil, errRender
	}
	if err := writeExclusive(configPath, content); err != nil {
		if errors.Is(err, os.ErrExist) {
			// Another process won the race and owns first run.
			return nil, nil
		}
		return nil, fmt.Errorf("write config %s: %w", configPath, err)
	}
	if err := WriteCredentials(keys); err != nil {
		// Without the plaintext copy the generated keys are unusable.
		_ = os.Remove(configPath)
		return nil, err
	}
	return &FirstRun{ConfigPath: configPath, CredentialsPath: home.CredentialsPath(), Port: port, WantedPort: wanted}, nil
}

func bindHost() string {
	if host := strings.TrimSpace(os.Getenv(EnvBindHost)); host != "" {
		return host
	}
	return "127.0.0.1"
}

func wantedPort() (int, error) {
	raw := strings.TrimSpace(os.Getenv(EnvPort))
	if raw == "" {
		return DefaultPort, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s must be a port number, got %q", EnvPort, raw)
	}
	return port, nil
}

func findFreePort(wanted int) int {
	for port := wanted; port < wanted+portSearchRange && port <= 65535; port++ {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			continue
		}
		_ = listener.Close()
		return port
	}
	return wanted
}

// GenerateKeys creates a client API key and a management key.
func GenerateKeys() (Keys, error) {
	api, err := randomToken(32)
	if err != nil {
		return Keys{}, err
	}
	management, err := randomToken(32)
	if err != nil {
		return Keys{}, err
	}
	return Keys{API: "sk-tpr-" + api, Management: "tpr-mgmt-" + management}, nil
}

func randomToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// RenderConfig returns a compact config.yaml. The management key is stored as a
// bcrypt hash, the same form upstream rewrites a plaintext key into on load, so
// the plaintext never reaches this file.
func RenderConfig(host string, port int, authDir string, keys Keys) ([]byte, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(keys.Management), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash management key: %w", err)
	}
	var b strings.Builder
	b.WriteString("# Token Proxy Router configuration. Generated on first run.\n")
	b.WriteString("# Settings can also be changed from the panel. See config.example.yaml in the\n")
	b.WriteString("# repository for every option.\n")
	b.WriteString("config-version: 8\n\n")
	b.WriteString("server:\n")
	if IsLoopbackHost(host) {
		b.WriteString("  # Loopback only. Use `tpr expose` to publish on your tailnet.\n")
	}
	fmt.Fprintf(&b, "  host: %s\n", strconv.Quote(host))
	fmt.Fprintf(&b, "  port: %d\n\n", port)
	b.WriteString("access:\n  # Client API keys. Tools send one of these as their API key.\n  api-keys:\n")
	fmt.Fprintf(&b, "    - %s\n\n", strconv.Quote(keys.API))
	b.WriteString("management:\n  allow-remote: false\n")
	fmt.Fprintf(&b, "  secret-key: %s\n\n", strconv.Quote(string(hashed)))
	b.WriteString("oauth:\n")
	fmt.Fprintf(&b, "  auth-dir: %s\n\n", strconv.Quote(authDir))
	b.WriteString("routing:\n  strategy: \"round-robin\"\n  retry:\n    request-retry: 3\n    max-retry-interval: 30\n\n")
	b.WriteString("observability:\n  usage:\n    usage-statistics-enabled: true\n")
	return []byte(b.String()), nil
}

// WriteCredentials saves the plaintext keys, readable only by the owner.
func WriteCredentials(keys Keys) error {
	var b strings.Builder
	b.WriteString("# Token Proxy Router first-run keys. Keep this private.\n")
	b.WriteString("# The management key is stored hashed in config.yaml, so this is the only plaintext copy.\n")
	b.WriteString("# Delete this file with: tpr creds --forget\n")
	fmt.Fprintf(&b, "client-api-key: %s\n", keys.API)
	fmt.Fprintf(&b, "management-key: %s\n", keys.Management)
	if err := AtomicWrite(home.CredentialsPath(), []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("save first-run keys: %w", err)
	}
	return nil
}

// ReadCredentials loads credentials.txt. A missing file returns os.ErrNotExist.
func ReadCredentials() (Keys, error) {
	raw, err := os.ReadFile(home.CredentialsPath())
	if err != nil {
		return Keys{}, err
	}
	var keys Keys
	for _, line := range strings.Split(string(raw), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		switch strings.TrimSpace(name) {
		case "client-api-key":
			keys.API = strings.TrimSpace(value)
		case "management-key":
			keys.Management = strings.TrimSpace(value)
		}
	}
	return keys, nil
}

// ManagementKey finds a management key for local CLI calls: the environment
// first, then credentials.txt. It returns "" when neither has one.
func ManagementKey() string {
	if key := strings.TrimSpace(os.Getenv(EnvManagementKey)); key != "" {
		return key
	}
	keys, err := ReadCredentials()
	if err != nil {
		return ""
	}
	return keys.Management
}

// Runner runs an external command. Packages take one so that tests never touch
// the real systemctl, tailscale, schtasks or reg.
type Runner func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
