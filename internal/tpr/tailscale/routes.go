package tailscale

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
)

const dnsCacheTTL = 30 * time.Second

var (
	defaultClient = NewClient()

	dnsMu      sync.Mutex
	dnsName    string
	dnsExpires time.Time

	defaultChecker = &Checker{
		Settings: func() Identity { return LoadState().Identity },
		Whois:    func(ctx context.Context, addr string) (string, error) { return defaultClient.Whois(ctx, addr) },
		DNSName:  cachedDNSName,
	}
)

func cachedDNSName(ctx context.Context) string {
	dnsMu.Lock()
	defer dnsMu.Unlock()
	if dnsName != "" && time.Now().Before(dnsExpires) {
		return dnsName
	}
	status := defaultClient.Status(context.WithoutCancel(ctx))
	dnsName = status.DNSName
	dnsExpires = time.Now().Add(dnsCacheTTL)
	return dnsName
}

// CheckIdentity reports whether a management request may skip the key. It is the
// hook called from upstream's management Middleware.
func CheckIdentity(r *http.Request, bindHost string) bool {
	_, ok := defaultChecker.Check(r, bindHost)
	return ok
}

// ServeSession answers GET /v8/management/tpr-public/session. It needs no key and
// reveals a login only when identity sign-in actually applies to this request, so
// a spoofed header gets back nothing it did not already send.
func ServeSession(c *gin.Context, bindHost string) {
	login, ok := defaultChecker.Check(c.Request, bindHost)
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"identity_login": ok, "login": login})
}

// Report is the GET /tailscale response.
type Report struct {
	Installed bool           `json:"installed"`
	LoggedIn  bool           `json:"logged_in"`
	DNSName   string         `json:"dns_name"`
	Exposed   bool           `json:"exposed"`
	URL       string         `json:"url"`
	Port      int            `json:"port"`
	CanExpose bool           `json:"can_expose"`
	Hint      string         `json:"hint"`
	Identity  IdentityReport `json:"identity"`
}

// IdentityReport is the identity part of Report.
type IdentityReport struct {
	Enabled       bool     `json:"enabled"`
	AllowedLogins []string `json:"allowed_logins"`
	Available     bool     `json:"available"`
	// Reason says why identity sign-in is unavailable on this OS.
	Reason string `json:"reason,omitempty"`
}

// BuildReport gathers the state shown by `tpr status` and the panel.
func (c *Client) BuildReport(ctx context.Context, info setup.ServerInfo, state State) Report {
	status := c.Status(ctx)
	report := Report{
		Installed: status.Installed,
		LoggedIn:  status.LoggedIn,
		DNSName:   status.DNSName,
		Port:      state.Port,
		Hint:      status.Hint,
		CanExpose: status.LoggedIn && status.DNSName != "",
	}
	if report.Port == 0 {
		report.Port = DefaultPort
	}
	report.Identity = IdentityReport{
		Enabled:       state.Identity.Enabled,
		AllowedLogins: append([]string{}, state.Identity.AllowedLogins...),
		Available:     identitySupported && status.LoggedIn && status.DNSName != "" && setup.IsLoopbackHost(info.Host),
		Reason:        identityUnsupportedReason,
	}
	if !status.LoggedIn {
		return report
	}
	port, exposed, err := c.FindExposure(ctx, info.Port, state.Port)
	switch {
	case err != nil:
		report.Hint = "Could not read the current serve settings: " + err.Error()
	case exposed:
		report.Exposed = true
		report.Port = port
		report.URL = c.URLFor(status.DNSName, port)
	}
	return report
}

// URLFor returns the tailnet URL for a published port.
func (c *Client) URLFor(dnsName string, port int) string {
	if dnsName == "" {
		return ""
	}
	if port == 443 {
		return "https://" + dnsName
	}
	return fmt.Sprintf("https://%s:%d", dnsName, port)
}

// HubURL returns the tailnet URL of this server, or "" when it is not published.
func HubURL(ctx context.Context, info setup.ServerInfo) string {
	return defaultClient.BuildReport(ctx, info, LoadState()).URL
}

func currentInfo() (setup.ServerInfo, error) {
	return setup.LoadServerInfo(setup.ActiveConfigPath())
}

func init() {
	tpr.AddRoutes(func(group *gin.RouterGroup) {
		group.GET("/tailscale", handleGet)
		group.POST("/tailscale/expose", handleExpose)
		group.DELETE("/tailscale/expose", handleUnexpose)
		group.PUT("/tailscale/identity", handleIdentity)
	})
}

func handleGet(c *gin.Context) {
	info, err := currentInfo()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the config: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, defaultClient.BuildReport(c.Request.Context(), info, LoadState()))
}

// Publish exposes the server and remembers the port.
func (c *Client) Publish(ctx context.Context, port int, info setup.ServerInfo) (string, error) {
	if port == 0 {
		port = DefaultPort
	}
	if err := c.Expose(ctx, port, info.Port); err != nil {
		return "", err
	}
	state := LoadState()
	state.Port = port
	if err := SaveState(state); err != nil {
		return "", fmt.Errorf("save tailnet state: %w", err)
	}
	return c.URLFor(c.Status(ctx).DNSName, port), nil
}

// Unpublish removes the published mapping for this server, if there is one.
func (c *Client) Unpublish(ctx context.Context, info setup.ServerInfo) error {
	state := LoadState()
	port, exposed, err := c.FindExposure(ctx, info.Port, state.Port)
	if err != nil {
		return err
	}
	if !exposed {
		return nil
	}
	return c.Unexpose(ctx, port, info.Port)
}

func handleExpose(c *gin.Context) {
	var body struct {
		Port int `json:"port"`
	}
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body must be JSON like {\"port\":8443}"})
		return
	}
	info, err := currentInfo()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the config: " + err.Error()})
		return
	}
	if _, err := defaultClient.Publish(c.Request.Context(), body.Port, info); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, defaultClient.BuildReport(c.Request.Context(), info, LoadState()))
}

func handleUnexpose(c *gin.Context) {
	info, err := currentInfo()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the config: " + err.Error()})
		return
	}
	if err := defaultClient.Unpublish(c.Request.Context(), info); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, defaultClient.BuildReport(c.Request.Context(), info, LoadState()))
}

func handleIdentity(c *gin.Context) {
	var body struct {
		Enabled       bool     `json:"enabled"`
		AllowedLogins []string `json:"allowed_logins"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body must be JSON like {\"enabled\":true,\"allowed_logins\":[\"alice@example.com\"]}"})
		return
	}
	logins, err := NormalizeLogins(body.AllowedLogins)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	info, err := currentInfo()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the config: " + err.Error()})
		return
	}
	if body.Enabled {
		if !identitySupported {
			c.JSON(http.StatusBadRequest, gin.H{"error": identityUnsupportedReason})
			return
		}
		if len(logins) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "add at least one allowed login before turning identity sign-in on"})
			return
		}
		if !setup.IsLoopbackHost(info.Host) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "identity sign-in needs the server bound to loopback; set server.host to 127.0.0.1 and restart"})
			return
		}
	}
	state := LoadState()
	state.Identity = Identity{Enabled: body.Enabled, AllowedLogins: logins}
	if err := SaveState(state); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save: " + strings.TrimSpace(err.Error())})
		return
	}
	c.JSON(http.StatusOK, defaultClient.BuildReport(c.Request.Context(), info, state))
}
