// Package router is the native, multi-provider port of the custom-quota-router
// plugin. One Pool per provider routes that provider's requests by remaining
// quota while keeping each conversation on one account. See docs/routing.md.
//
// The router sits in front of the auth manager's scheduler. Any request it does
// not handle, and any panic or bad data in here, falls through to whatever would
// have decided otherwise: an upstream scheduler plugin, or upstream selection.
package router

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router/engine"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

// usagePluginName is fixed, so a restart in one process replaces the old plugin.
const usagePluginName = "tpr-router"

// Observer is told an account's quota changed. It runs on the usage or polling
// goroutine, so it must return quickly.
type Observer func(provider, id string, q engine.Quota)

// Router implements coreauth.PluginScheduler, coreauth.SchedulerFront and
// usage.Plugin.
type Router struct {
	pools map[string]*Pool
	next  atomic.Pointer[coreauth.PluginScheduler]

	stopped atomic.Bool

	observersMu sync.RWMutex
	observers   []Observer

	// pickHook runs at the start of every engine decision. Tests use it to prove
	// a panic in the decision falls back to upstream selection.
	pickHook atomic.Pointer[func()]

	lastPanicLog atomic.Int64
}

var current atomic.Pointer[Router]

// Current returns the running router, or nil before it starts.
func Current() *Router { return current.Load() }

// New builds a router on a host. Most callers want Start.
func New(host Host) *Router { return NewWithClock(host, time.Now) }

// NewWithClock builds a router whose engines read an injected clock.
func NewWithClock(host Host, clock func() time.Time) *Router {
	r := &Router{pools: make(map[string]*Pool, len(Providers))}
	for _, provider := range Providers {
		r.pools[provider] = newPool(provider, host, r.notifyObservers, clock)
	}
	return r
}

// Start builds the router on the auth manager, puts it in front of the manager's
// scheduler and starts its background work.
func Start(mgr *coreauth.Manager, cfg func() *config.Config) *Router {
	r := New(newManagerHost(mgr, cfg))
	mgr.SetSchedulerFront(r)
	usage.RegisterNamedPlugin(usagePluginName, r)
	for _, pool := range r.pools {
		pool.start()
	}
	current.Store(r)
	return r
}

// Stop lets held requests go, stops background work and saves state. Requests
// after this point go to upstream selection.
func (r *Router) Stop() {
	r.stopped.Store(true)
	current.CompareAndSwap(r, nil)
	for _, pool := range r.pools {
		pool.stop()
	}
}

// Pool returns one provider's pool, or nil if the provider has no engine.
func (r *Router) Pool(provider string) *Pool {
	if r == nil {
		return nil
	}
	return r.pools[strings.ToLower(strings.TrimSpace(provider))]
}

// RequestPollAll asks every pool to refresh all its accounts now.
func (r *Router) RequestPollAll() {
	for _, pool := range r.pools {
		pool.RequestPoll()
	}
}

// AddObserver registers a quota observer.
func (r *Router) AddObserver(fn Observer) {
	r.observersMu.Lock()
	defer r.observersMu.Unlock()
	r.observers = append(r.observers, fn)
}

func (r *Router) notifyObservers(provider, id string) {
	r.observersMu.RLock()
	observers := r.observers
	r.observersMu.RUnlock()
	if len(observers) == 0 {
		return
	}
	pool := r.pools[provider]
	if pool == nil {
		return
	}
	q, ok := pool.Engine.QuotaOf(id)
	if !ok {
		return
	}
	for _, fn := range observers {
		fn(provider, id, q)
	}
}

// SetPickHookForTest installs a function that runs at the start of every engine
// decision, or removes it when fn is nil.
func (r *Router) SetPickHookForTest(fn func()) {
	if fn == nil {
		r.pickHook.Store(nil)
		return
	}
	r.pickHook.Store(&fn)
}

// SetNextScheduler implements coreauth.SchedulerFront.
func (r *Router) SetNextScheduler(next coreauth.PluginScheduler) {
	if next == nil {
		r.next.Store(nil)
		return
	}
	r.next.Store(&next)
}

// HasScheduler tells the manager to consult PickAuth. It must stay true while the
// router runs, whatever sits behind it.
func (r *Router) HasScheduler() bool { return true }

// SchedulerWantsAcrossPriorities asks for candidates of every priority tier, so
// the account order and roles decide rather than a credential's auth priority.
func (r *Router) SchedulerWantsAcrossPriorities() bool { return true }

// PickAuth chooses a credential when the request belongs to a pool, and otherwise
// passes it on. Whatever happens in here must never fail the request: a panic or
// bad data returns not-handled, and upstream selection decides.
func (r *Router) PickAuth(ctx context.Context, req pluginapi.SchedulerPickRequest) (resp pluginapi.SchedulerPickResponse, handled bool, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			r.logPanic(rec)
			resp, handled, err = pluginapi.SchedulerPickResponse{}, false, nil
		}
	}()
	pool := r.poolFor(req)
	if pool == nil {
		return r.delegate(ctx, req)
	}
	settings, _ := pool.Engine.Settings()
	if !settings.Enabled {
		return r.delegate(ctx, req)
	}
	cands := make([]engine.Candidate, 0, len(req.Candidates))
	for _, c := range req.Candidates {
		cands = append(cands, engine.Candidate{ID: c.ID, Provider: c.Provider, Priority: c.Priority, Status: c.Status})
	}
	pick := engine.PickRequest{
		Candidates: cands,
		Headers:    req.Options.Headers,
		Metadata:   req.Options.Metadata,
		Model:      req.Model,
	}
	// While paused or slowed down this waits; it never holds the engine lock.
	if reason := pool.Hold(ctx, pick); reason != "" {
		return pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: RejectPaused, RejectReason: reason}, true, nil
	}
	if hook := r.pickHook.Load(); hook != nil {
		(*hook)()
	}
	res := pool.Engine.Pick(pick)
	if !res.Handled {
		return r.delegate(ctx, req)
	}
	return pluginapi.SchedulerPickResponse{
		AuthID:       res.AuthID,
		Handled:      true,
		Reject:       res.Reject,
		RejectCode:   res.RejectCode,
		RejectReason: res.RejectReason,
	}, true, nil
}

// poolFor returns the pool that should decide this request. A route is a pool's
// only when every candidate belongs to it: if a model is also offered by another
// provider, the choice between providers belongs to upstream selection.
func (r *Router) poolFor(req pluginapi.SchedulerPickRequest) *Pool {
	if r.stopped.Load() || len(req.Candidates) == 0 {
		return nil
	}
	provider := ""
	for _, c := range req.Candidates {
		p := strings.ToLower(strings.TrimSpace(c.Provider))
		if provider == "" {
			provider = p
		} else if p != provider {
			return nil
		}
	}
	return r.pools[provider]
}

// delegate hands the request to whatever sat behind the router. An upstream
// scheduler plugin that did not opt into every priority tier only sees the
// highest one, as it would have without the router in front.
func (r *Router) delegate(ctx context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	nextPtr := r.next.Load()
	if nextPtr == nil || *nextPtr == nil {
		return pluginapi.SchedulerPickResponse{}, false, nil
	}
	next := *nextPtr
	if state, ok := next.(interface{ HasScheduler() bool }); ok && !state.HasScheduler() {
		return pluginapi.SchedulerPickResponse{}, false, nil
	}
	across := false
	if opt, ok := next.(coreauth.PluginSchedulerAcrossPriorities); ok {
		across = opt.SchedulerWantsAcrossPriorities()
	}
	if !across {
		req.Candidates = highestPriority(req.Candidates)
	}
	return next.PickAuth(ctx, req)
}

func highestPriority(cands []pluginapi.SchedulerAuthCandidate) []pluginapi.SchedulerAuthCandidate {
	best, found := 0, false
	for _, c := range cands {
		if !found || c.Priority > best {
			best, found = c.Priority, true
		}
	}
	out := make([]pluginapi.SchedulerAuthCandidate, 0, len(cands))
	for _, c := range cands {
		if c.Priority == best {
			out = append(out, c)
		}
	}
	return out
}

// HandleUsage implements usage.Plugin: every completed request updates quota.
func (r *Router) HandleUsage(_ context.Context, rec usage.Record) {
	defer func() {
		if v := recover(); v != nil {
			r.logPanic(v)
		}
	}()
	if r.stopped.Load() || rec.AuthID == "" {
		return
	}
	pool := r.pools[strings.ToLower(strings.TrimSpace(rec.Provider))]
	if pool == nil {
		return
	}
	// Rate-limit data reflects the start of the upstream response.
	at := rec.RequestedAt
	if rec.TTFT > 0 {
		at = at.Add(rec.TTFT)
	}
	obs := engine.UsageObservation{
		AuthID:   rec.AuthID,
		Provider: rec.Provider,
		Model:    rec.Model,
		At:       at,
		Headers:  map[string][]string(rec.ResponseHeaders),
		Failed:   rec.Failed,
		Status:   rec.Fail.StatusCode,
		Body:     rec.Fail.Body,
	}
	if pool.provider == engine.ProviderClaude {
		obs.LimitRejected = claudeRejected(rec.ResponseHeaders)
	}
	pool.Engine.ObserveUsage(obs)
	r.notifyObservers(pool.provider, rec.AuthID)
}

// claudeRejected reuses the executor's reading of Anthropic's headers, so an
// overage-only rejection does not take a whole account out of rotation.
func claudeRejected(headers http.Header) bool {
	return helps.ClaudeHeadersIndicateUnifiedRateLimitRejection(headers)
}

// logPanic reports a contained panic without flooding the log on the request path.
func (r *Router) logPanic(v any) {
	now := time.Now().Unix()
	if last := r.lastPanicLog.Load(); now-last < 60 || !r.lastPanicLog.CompareAndSwap(last, now) {
		return
	}
	log.Errorf("router: recovered from panic, falling back to upstream selection: %v", v)
}
