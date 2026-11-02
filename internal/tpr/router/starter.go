package router

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router/engine"
)

const (
	starterIdleWait = 30 * time.Second
	starterTimeout  = 90 * time.Second
)

// StarterSettleWait is how long to wait before re-reading quota after a window
// start (a variable so tests can shorten it).
var StarterSettleWait = 4 * time.Second

// starterLoop sends scheduled and automatic window starts at their due time.
func (p *Pool) starterLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	select {
	case <-ctx.Done():
		return
	case <-time.After(InitialLoopDelay):
	}
	for {
		p.runDueStarts(ctx)
		wait := starterIdleWait
		if next := p.Engine.NextStartAt(); !next.IsZero() {
			if d := time.Until(next); d < wait {
				wait = d
			}
		}
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-p.startWake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (p *Pool) runDueStarts(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			p.setErr(fmt.Sprintf("window start recovered from panic: %v", r))
		}
	}()
	settings, _ := p.Engine.Settings()
	if !settings.Enabled {
		return
	}
	for _, plan := range p.Engine.DueStarts() {
		if ctx.Err() != nil {
			return
		}
		p.Engine.RecordStart(p.startWindow(ctx, plan))
	}
	p.logEvents()
	p.Flush()
}

// startWindow sends one tiny request to start an account's 5-hour window, unless
// it is already running, and measures what it cost. The request is not part of
// any session or thread and its text is never kept.
func (p *Pool) startWindow(ctx context.Context, plan engine.StartPlan) engine.StartResult {
	settings, _ := p.Engine.Settings()
	r := engine.StartResult{At: time.Now(), Account: plan.Account, Source: plan.Source, Model: settings.WindowStartModel, Effort: settings.WindowStartEffort}
	authIndex, ok := p.Engine.StartTarget(plan.Account)
	if !ok {
		r.Status, r.Detail = engine.StartFailed, "account is no longer in the proxy"
		return r
	}
	// Fresh numbers first, so a running window is never restarted needlessly.
	p.refreshQuota(ctx, plan.Account, authIndex)
	if state, reset := p.Engine.WindowState(plan.Account); state == engine.WindowRunning {
		r.Status, r.ResetAt = engine.StartRunning, reset
		return r
	}
	r.ShortBefore, r.LongBefore = p.Engine.UsedPercents(plan.Account)

	// A fresh random ID per start, used as the upstream session and cache key.
	// Without it the proxy derives one from the identical prompt, so every
	// account's start would share the same ID.
	oneOff := randomUUID()
	body, headers := starterBody(p.provider, settings.WindowStartModel, settings.WindowStartEffort, oneOff)
	// A pause or slow down is for agents' requests, not for starts you scheduled.
	p.held.setExempt(oneOff, true)
	defer p.held.setExempt(oneOff, false)

	execCtx, cancel := context.WithTimeout(ctx, starterTimeout)
	defer cancel()
	sent := time.Now()
	resp, err := p.host.ExecuteModel(execCtx, p.provider, settings.WindowStartModel, body, headers, plan.Account)
	if err == nil && resp.StatusCode != 0 && resp.StatusCode != 200 {
		err = fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err != nil {
		r.Status, r.Detail = engine.StartFailed, shortError(err)
		return r
	}
	readStarterUsage(&r, resp.Body)

	// Let the provider's counters settle, then measure again.
	select {
	case <-ctx.Done():
	case <-time.After(StarterSettleWait):
	}
	p.refreshQuota(ctx, plan.Account, authIndex)
	r.ShortAfter, r.LongAfter = p.Engine.UsedPercents(plan.Account)
	r.Status = engine.StartStarted
	r.ResetAt = sent.Add(5 * time.Hour)
	if state, reset := p.Engine.WindowState(plan.Account); state == engine.WindowRunning && !reset.IsZero() {
		r.ResetAt = reset
	}
	return r
}

// readStarterUsage reads the token counts of either protocol's response.
func readStarterUsage(r *engine.StartResult, body []byte) {
	var out struct {
		Usage struct {
			InputTokens        int64 `json:"input_tokens"`
			OutputTokens       int64 `json:"output_tokens"`
			CacheReadTokens    int64 `json:"cache_read_input_tokens"`
			InputTokensDetails struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			OutputTokensDetails struct {
				ReasoningTokens int64 `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(body, &out)
	r.InputTokens = out.Usage.InputTokens
	r.CachedTokens = out.Usage.InputTokensDetails.CachedTokens + out.Usage.CacheReadTokens
	r.OutputTokens = out.Usage.OutputTokens
	r.ReasoningTokens = out.Usage.OutputTokensDetails.ReasoningTokens
}

func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// shortError keeps a failure readable and bounded. Window-start errors come from
// upstream responses, so they go through the same scrubbing as any stored error.
func shortError(err error) string {
	msg := strings.TrimSpace(SanitizeError(err.Error()))
	if len(msg) > 160 {
		msg = msg[:160] + "…"
	}
	return msg
}
