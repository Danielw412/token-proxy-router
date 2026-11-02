package tailscale

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
	"golang.org/x/sync/singleflight"
)

const (
	headerLogin   = "Tailscale-User-Login"
	headerFunnel  = "Tailscale-Funnel-Request"
	headerForward = "X-Forwarded-For"
	whoisCacheTTL = 10 * time.Second
)

// tailnetRanges are the only addresses Tailscale gives to tailnet devices.
var tailnetRanges = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

// Checker decides whether a management request may skip the key. Every check
// fails closed: an unexpected value means "use the normal key check".
//
// Tailscale identity is ambient. A browser on the tailnet carries it on every
// request, and upstream answers with `Access-Control-Allow-Origin: *`, so without
// the Host and Origin checks any website the user visits could read the management
// API. Those checks go beyond the header-and-whois rule for that reason.
type Checker struct {
	// Settings returns the current identity settings.
	Settings func() Identity
	// Whois resolves a tailnet address to a login.
	Whois func(ctx context.Context, addr string) (string, error)
	// DNSName returns this machine's tailnet name.
	DNSName func(ctx context.Context) string
	// Now is the clock used for the whois cache.
	Now func() time.Time
	// PeerTrusted decides whether the connection's local owner may speak for
	// Tailscale. Nil means the real check for this OS.
	PeerTrusted func(*http.Request) bool

	mu    sync.Mutex
	cache map[string]whoisEntry
	group singleflight.Group
}

type whoisEntry struct {
	login   string
	expires time.Time
}

// Check returns the verified login, or ok=false when any condition fails.
func (k *Checker) Check(r *http.Request, bindHost string) (string, bool) {
	if r == nil || k == nil {
		return "", false
	}
	settings := k.Settings()
	if !settings.Enabled || len(settings.AllowedLogins) == 0 {
		return "", false
	}
	if !setup.IsLoopbackHost(bindHost) || !peerIsLoopback(r.RemoteAddr) {
		return "", false
	}
	// Funnel traffic comes from the public internet and is never a tailnet user.
	if r.Header.Get(headerFunnel) != "" {
		return "", false
	}
	logins := r.Header.Values(headerLogin)
	if len(logins) != 1 {
		return "", false
	}
	login := strings.TrimSpace(logins[0])
	if login == "" || strings.EqualFold(login, taggedLogin) || !settings.allows(login) {
		return "", false
	}
	addr, ok := forwardedTailnetAddr(r)
	if !ok {
		return "", false
	}
	if !browserRequestIsSameOrigin(r) {
		return "", false
	}
	peerTrusted := k.PeerTrusted
	if peerTrusted == nil {
		peerTrusted = defaultPeerTrusted
	}
	if !peerTrusted(r) {
		return "", false
	}
	ctx := r.Context()
	dnsName := k.DNSName(ctx)
	if dnsName == "" || !strings.EqualFold(requestHostname(r.Host), dnsName) {
		return "", false
	}
	verified, err := k.lookup(ctx, addr)
	if err != nil || !strings.EqualFold(strings.TrimSpace(verified), login) {
		return "", false
	}
	return login, true
}

func (k *Checker) lookup(ctx context.Context, addr string) (string, error) {
	now := time.Now
	if k.Now != nil {
		now = k.Now
	}
	k.mu.Lock()
	if entry, ok := k.cache[addr]; ok && now().Before(entry.expires) {
		k.mu.Unlock()
		return entry.login, nil
	}
	k.mu.Unlock()
	value, err, _ := k.group.Do(addr, func() (any, error) {
		login, errWhois := k.Whois(context.WithoutCancel(ctx), addr)
		if errWhois != nil {
			return "", errWhois
		}
		k.mu.Lock()
		if k.cache == nil || len(k.cache) > 256 {
			k.cache = map[string]whoisEntry{}
		}
		k.cache[addr] = whoisEntry{login: login, expires: now().Add(whoisCacheTTL)}
		k.mu.Unlock()
		return login, nil
	})
	if err != nil {
		return "", err
	}
	return value.(string), nil
}

func peerIsLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.Unmap().IsLoopback()
}

// forwardedTailnetAddr returns the single tailnet address tailscale serve put in
// X-Forwarded-For. A list means something other than serve wrote the header.
func forwardedTailnetAddr(r *http.Request) (string, bool) {
	values := r.Header.Values(headerForward)
	if len(values) != 1 {
		return "", false
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(values[0]))
	if err != nil {
		return "", false
	}
	addr = addr.Unmap()
	for _, prefix := range tailnetRanges {
		if prefix.Contains(addr) {
			return addr.String(), true
		}
	}
	return "", false
}

// browserRequestIsSameOrigin rejects what a website can make a browser send.
// Non-browser clients send neither header and are not affected.
func browserRequestIsSameOrigin(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))) {
	case "", "same-origin", "none":
	default:
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Scheme == "https" && strings.EqualFold(parsed.Host, r.Host)
}

func requestHostname(host string) string {
	if name, _, err := net.SplitHostPort(host); err == nil {
		return name
	}
	return host
}
