package router

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router/engine"
	log "github.com/sirupsen/logrus"
)

const (
	rosterEvery     = 30 * time.Second
	pollTimeout     = 20 * time.Second
	maxPollsPerTick = 3
)

// Loop timing; variables so tests can shorten them.
var (
	InitialLoopDelay = 2 * time.Second
	TickEvery        = 15 * time.Second
)

// Pool is one provider's engine with its background work: roster refresh, quota
// polling, window starts, persistence and holding requests.
type Pool struct {
	Engine *engine.Engine

	provider string
	host     Host
	observe  func(provider, id string)
	// persist is off in tests that have no data folder.
	persist bool

	mu        sync.Mutex
	cancel    context.CancelFunc
	done      chan struct{}
	pollNow   chan struct{}
	startWake chan struct{}
	lastErr   string
	held      *holder
}

// newPool creates a pool and restores its saved settings and state.
func newPool(provider string, host Host, observe func(provider, id string), clock func() time.Time) *Pool {
	settings, warnings := loadSettings(provider)
	p := &Pool{
		Engine:    engine.NewForProvider(provider, settings, warnings, clock),
		provider:  provider,
		host:      host,
		observe:   observe,
		persist:   true,
		pollNow:   make(chan struct{}, 1),
		startWake: make(chan struct{}, 1),
		held:      newHolder(),
	}
	if st, ok, err := engine.LoadState(engineStatePath(provider)); err != nil {
		p.lastErr = err.Error()
		log.WithField("provider", provider).Warnf("router: starting with fresh state: %v", err)
	} else if ok {
		p.Engine.Restore(st)
	}
	return p
}

// Provider returns the pool's provider name.
func (p *Pool) Provider() string { return p.provider }

// start launches the background loops (idempotent).
func (p *Pool) start() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		return
	}
	p.held.start()
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	loopDone, starterDone := make(chan struct{}), make(chan struct{})
	go p.loop(ctx, loopDone)
	go p.starterLoop(ctx, starterDone)
	go func(done chan struct{}) {
		<-loopDone
		<-starterDone
		close(done)
	}(p.done)
}

// stop lets held requests go, halts the loops and saves state.
func (p *Pool) stop() {
	p.held.stop()
	p.mu.Lock()
	cancel, done := p.cancel, p.done
	p.cancel, p.done = nil, nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
		}
		p.logEvents()
	}
	p.Flush()
}

// WakeStarter re-evaluates scheduled window starts now, after a change.
func (p *Pool) WakeStarter() {
	select {
	case p.startWake <- struct{}{}:
	default:
	}
}

// RequestPoll asks the loop to refresh every account's quota now.
func (p *Pool) RequestPoll() {
	p.Engine.ForcePollAll()
	select {
	case p.pollNow <- struct{}{}:
	default:
	}
}

// Flush saves state if it changed.
func (p *Pool) Flush() {
	st, dirty := p.Engine.Export()
	if !dirty || !p.persist {
		return
	}
	if err := engine.SaveState(engineStatePath(p.provider), st); err != nil {
		p.Engine.MarkDirty()
		p.setErr(fmt.Sprintf("save state: %v", err))
	}
}

// LastError returns the most recent background error, for the panel.
func (p *Pool) LastError() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastErr
}

func (p *Pool) setErr(msg string) {
	p.mu.Lock()
	p.lastErr = msg
	p.mu.Unlock()
}

func (p *Pool) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	select {
	case <-ctx.Done():
		return
	case <-time.After(InitialLoopDelay):
	}
	var lastRoster time.Time
	ticker := time.NewTicker(TickEvery)
	defer ticker.Stop()
	for {
		p.iteration(ctx, &lastRoster)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-p.pollNow:
			lastRoster = time.Time{}
		}
	}
}

// iteration runs one maintenance pass. A panic is contained to this pass, so the
// loop keeps refreshing, polling and saving afterwards.
func (p *Pool) iteration(ctx context.Context, lastRoster *time.Time) {
	defer func() {
		if r := recover(); r != nil {
			p.setErr(fmt.Sprintf("background task recovered from panic: %v", r))
		}
	}()
	if time.Since(*lastRoster) >= rosterEvery {
		p.Engine.ObserveAccounts(p.host.Accounts())
		*lastRoster = time.Now()
	}
	p.Engine.Tick()
	p.pollDue(ctx)
	p.logEvents()
	p.Flush()
}

func (p *Pool) pollDue(ctx context.Context) {
	for i, t := range p.Engine.PollTargets() {
		if i >= maxPollsPerTick || ctx.Err() != nil {
			return
		}
		p.refreshQuota(ctx, t.ID, t.AuthIndex)
	}
}

// refreshQuota polls one account, if it has a usage endpoint to poll, and records
// the result. API-key credentials have none and rely on response headers.
func (p *Pool) refreshQuota(ctx context.Context, id, authIndex string) {
	if authIndex == "" {
		return
	}
	q, err := p.host.FetchQuota(ctx, p.provider, id)
	p.Engine.ApplyPolledQuota(id, q, err)
	if err == nil && p.observe != nil {
		p.observe(p.provider, id)
	}
}

func (p *Pool) logEvents() {
	for _, ev := range p.Engine.DrainEvents() {
		log.WithFields(log.Fields{"provider": p.provider, "kind": ev.Kind, "from": ev.From, "to": ev.To}).Info("router: " + ev.Reason)
	}
}
