// Package health reports whether each Codex and Claude account works, from auth
// state, cooldowns, token expiry and recent results.
package health

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/store"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

// Status values, most serious first.
const (
	StatusDisabled = "disabled"
	StatusExpired  = "expired"
	StatusFailing  = "failing"
	StatusCooling  = "cooling"
	StatusExpiring = "expiring"
	StatusOK       = "ok"
)

const (
	usagePluginName = "tpr-health"
	activityKey     = "health.activity"
	saveEvery       = time.Minute
	// expiringWithin flags a token that will run out and cannot renew itself.
	expiringWithin = 48 * time.Hour
	// failingWindow is how recent an unresolved failure must be to count.
	failingWindow = 15 * time.Minute
)

func init() {
	tpr.AddRoutes(func(group *gin.RouterGroup) {
		group.GET("/health/accounts", handleAccounts)
	})
}

// Activity is what the server remembers about an account's recent requests.
type Activity struct {
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`
	LastErrorAt   time.Time `json:"last_error_at,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
}

// Reauth tells the panel which login flow fixes this account.
type Reauth struct {
	Provider string `json:"provider"`
}

// Account is one row of GET /health/accounts. It holds no credential material.
type Account struct {
	ID             string     `json:"id"`
	Provider       string     `json:"provider"`
	Label          string     `json:"label"`
	Email          string     `json:"email"`
	Status         string     `json:"status"`
	Reason         string     `json:"reason"`
	LastSuccessAt  *time.Time `json:"last_success_at"`
	LastErrorAt    *time.Time `json:"last_error_at"`
	LastError      string     `json:"last_error"`
	TokenExpiresAt *time.Time `json:"token_expires_at"`
	Reauth         *Reauth    `json:"reauth"`
}

// Lister lists credentials. *coreauth.Manager implements it.
type Lister interface {
	List() []*coreauth.Auth
}

// Service implements usage.Plugin and answers health queries.
type Service struct {
	lister Lister
	store  *store.Store
	now    func() time.Time

	mu       sync.Mutex
	activity map[string]Activity
	dirty    bool

	cancel context.CancelFunc
	done   chan struct{}
}

var current atomic.Pointer[Service]

// Current returns the running service, or nil before it starts.
func Current() *Service { return current.Load() }

// New builds a service. The store is optional: without it, activity is only kept
// until the next restart.
func New(lister Lister, st *store.Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	s := &Service{lister: lister, store: st, now: now, activity: map[string]Activity{}}
	if st != nil {
		if _, err := st.GetJSON(activityKey, &s.activity); err != nil {
			log.Warnf("health: saved activity could not be read: %v", err)
		}
		if s.activity == nil {
			s.activity = map[string]Activity{}
		}
	}
	return s
}

// Start registers the usage plugin and makes the service current.
func Start(lister Lister, st *store.Store) *Service {
	s := New(lister, st, time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	go s.loop(ctx)
	usage.RegisterNamedPlugin(usagePluginName, s)
	current.Store(s)
	return s
}

// Stop saves activity and stops the save loop.
func (s *Service) Stop() {
	current.CompareAndSwap(s, nil)
	if s.cancel != nil {
		s.cancel()
		<-s.done
	}
	s.save()
}

func (s *Service) loop(ctx context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(saveEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.save()
		}
	}
}

func (s *Service) save() {
	if s.store == nil {
		return
	}
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return
	}
	snapshot := make(map[string]Activity, len(s.activity))
	for id, a := range s.activity {
		snapshot[id] = a
	}
	s.dirty = false
	s.mu.Unlock()
	if err := s.store.PutJSON(activityKey, snapshot); err != nil {
		log.Warnf("health: could not save activity: %v", err)
	}
}

// HandleUsage implements usage.Plugin. An error is stored only after sanitizing, so
// it can never contain a token or a response body.
func (s *Service) HandleUsage(_ context.Context, rec usage.Record) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("health: recovered from panic: %v", r)
		}
	}()
	if rec.AuthID == "" || !isPoolProvider(rec.Provider) {
		return
	}
	at := rec.RequestedAt
	if at.IsZero() {
		at = s.now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.activity[rec.AuthID]
	if rec.Failed {
		a.LastErrorAt = at
		a.LastError = failureText(rec.Fail)
	} else {
		a.LastSuccessAt = at
	}
	s.activity[rec.AuthID] = a
	s.dirty = true
}

func failureText(f usage.Failure) string {
	if msg := router.SanitizeError(f.Body); msg != "" {
		return msg
	}
	if f.StatusCode > 0 {
		return "upstream returned HTTP " + http.StatusText(f.StatusCode)
	}
	return "request failed"
}

func isPoolProvider(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	for _, p := range router.Providers {
		if p == provider {
			return true
		}
	}
	return false
}

// Accounts returns every Codex and Claude account's health, sorted for display.
func (s *Service) Accounts() []Account {
	now := s.now()
	s.mu.Lock()
	activity := make(map[string]Activity, len(s.activity))
	for id, a := range s.activity {
		activity[id] = a
	}
	s.mu.Unlock()
	out := []Account{}
	for _, auth := range s.lister.List() {
		if auth == nil || !isPoolProvider(auth.Provider) {
			continue
		}
		out = append(out, Derive(now, auth, activity[auth.ID]))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		if out[i].Label != out[j].Label {
			return out[i].Label < out[j].Label
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Derive works out one account's status. It is pure, for testing.
func Derive(now time.Time, a *coreauth.Auth, activity Activity) Account {
	out := Account{
		ID:       a.ID,
		Provider: strings.ToLower(strings.TrimSpace(a.Provider)),
		Label:    a.Label,
		Email:    emailOf(a),
		Status:   StatusOK,
	}
	if out.Label == "" {
		out.Label = out.Email
	}
	if out.Label == "" {
		out.Label = a.ID
	}
	if !activity.LastSuccessAt.IsZero() {
		t := activity.LastSuccessAt
		out.LastSuccessAt = &t
	}
	if !activity.LastErrorAt.IsZero() {
		t := activity.LastErrorAt
		out.LastErrorAt = &t
	}
	out.LastError = router.SanitizeError(activity.LastError)

	oauth := isOAuth(a)
	var expires time.Time
	var hasExpiry bool
	if oauth {
		out.Reauth = &Reauth{Provider: out.Provider}
		if t, ok := a.AccessTokenExpirationTime(); ok && !t.IsZero() && t.Unix() > 0 {
			expires, hasExpiry = t, true
			out.TokenExpiresAt = &t
		}
	}
	canRenew := oauth && hasRefreshToken(a) && !a.RequiresSignIn
	set := func(status, reason string) { out.Status, out.Reason = status, reason }

	switch {
	case a.Disabled || a.Status == coreauth.StatusDisabled:
		set(StatusDisabled, "Turned off")
	case oauth && a.RequiresSignIn:
		set(StatusExpired, "Its sign-in was rejected and cannot renew. Sign in again.")
	case oauth && hasExpiry && !expires.After(now) && !canRenew:
		set(StatusExpired, "Its token has expired and cannot renew. Sign in again.")
	case recentFailure(now, activity) || a.Status == coreauth.StatusError:
		reason := out.LastError
		if reason == "" && a.LastError != nil {
			reason = router.SanitizeError(a.LastError.Message)
		}
		if reason == "" {
			reason = router.SanitizeError(a.StatusMessage)
		}
		if reason == "" {
			reason = "Recent requests failed"
		}
		if out.LastError == "" {
			out.LastError = reason
		}
		set(StatusFailing, reason)
	case isCooling(now, a):
		set(StatusCooling, "Resting after an upstream error or limit, until "+cooldownEnd(now, a).Format(time.RFC3339))
	case oauth && a.RefreshError != nil:
		set(StatusExpiring, "Its last token renewal failed and will be retried")
	case oauth && hasExpiry && !canRenew && expires.Sub(now) <= expiringWithin:
		set(StatusExpiring, "Its token runs out soon and cannot renew. Sign in again.")
	}
	return out
}

func recentFailure(now time.Time, activity Activity) bool {
	if activity.LastErrorAt.IsZero() || !activity.LastErrorAt.After(activity.LastSuccessAt) {
		return false
	}
	return now.Sub(activity.LastErrorAt) <= failingWindow
}

func isCooling(now time.Time, a *coreauth.Auth) bool {
	if a.Unavailable && a.NextRetryAfter.After(now) {
		return true
	}
	return a.Quota.Exceeded && a.Quota.NextRecoverAt.After(now)
}

func cooldownEnd(now time.Time, a *coreauth.Auth) time.Time {
	end := a.NextRetryAfter
	if a.Quota.NextRecoverAt.After(end) {
		end = a.Quota.NextRecoverAt
	}
	if !end.After(now) {
		return now
	}
	return end
}

func emailOf(a *coreauth.Auth) string {
	if a.Metadata != nil {
		if v, ok := a.Metadata["email"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	if a.Attributes != nil {
		return strings.TrimSpace(a.Attributes["email"])
	}
	return ""
}

func isOAuth(a *coreauth.Auth) bool {
	return a.AuthKind() != coreauth.AuthKindAPIKey && (a.Attributes == nil || strings.TrimSpace(a.Attributes["api_key"]) == "")
}

func hasRefreshToken(a *coreauth.Auth) bool {
	if a.Metadata == nil {
		return false
	}
	for _, key := range []string{"refresh_token", "refreshToken"} {
		if v, ok := a.Metadata[key].(string); ok && strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

func handleAccounts(c *gin.Context) {
	s := Current()
	if s == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "account health is not running"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"accounts": s.Accounts()})
}
