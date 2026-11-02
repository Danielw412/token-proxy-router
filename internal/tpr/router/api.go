package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router/engine"
)

func init() {
	tpr.AddRoutes(registerRoutes)
}

func registerRoutes(group *gin.RouterGroup) {
	g := group.Group("/router")
	g.GET("/state", handleState)
	g.POST("/mode", withPool(handleMode))
	g.POST("/select", withPool(handleSelect))
	g.POST("/refresh", withPool(handleRefresh))
	g.GET("/settings", withPool(handleGetSettings))
	g.PATCH("/settings", withPool(handlePatchSettings))
	g.POST("/windows/start", withPool(handleWindowsStart))
	g.POST("/windows/schedule", withPool(handleWindowsSchedule))
	g.POST("/windows/cancel", withPool(handleWindowsCancel))
	g.POST("/pause", withPool(handlePause))
	g.POST("/resume", withPool(handleResume))
	g.POST("/slowdown", withPool(handleSlowdown))
}

// withPool resolves ?provider= and answers 503 until the router has started.
func withPool(fn func(*gin.Context, *Pool)) gin.HandlerFunc {
	return func(c *gin.Context) {
		r := Current()
		if r == nil {
			jsonError(c, http.StatusServiceUnavailable, "the router has not started")
			return
		}
		provider := strings.ToLower(strings.TrimSpace(c.Query("provider")))
		pool := r.Pool(provider)
		if pool == nil {
			jsonError(c, http.StatusBadRequest, "provider must be one of: "+strings.Join(Providers, ", "))
			return
		}
		fn(c, pool)
	}
}

func jsonError(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"error": msg})
}

// decodeBody reads a small JSON body strictly. An empty body is fine.
func decodeBody(c *gin.Context, v any) bool {
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 64<<10+1))
	if err != nil {
		jsonError(c, http.StatusBadRequest, "could not read the request body")
		return false
	}
	if len(raw) > 64<<10 {
		jsonError(c, http.StatusRequestEntityTooLarge, "request body too large")
		return false
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return true
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if errDecode := dec.Decode(v); errDecode != nil {
		jsonError(c, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func accountError(c *gin.Context, err error) {
	if errors.Is(err, engine.ErrUnknownAccount) {
		jsonError(c, http.StatusNotFound, "unknown account")
		return
	}
	jsonError(c, http.StatusBadRequest, err.Error())
}

// windowRow is one account's 5-hour window, for the panel's windows section.
type windowRow struct {
	Account     string             `json:"account"`
	Label       string             `json:"label"`
	State       string             `json:"state"`
	ResetAt     *time.Time         `json:"reset_at,omitempty"`
	AutoStartAt *time.Time         `json:"auto_start_at,omitempty"`
	Plans       []engine.StartPlan `json:"plans"`
}

// stateBody is the contract of GET /router/state in docs/DESIGN.md.
type stateBody struct {
	Provider     string                `json:"provider"`
	Enabled      bool                  `json:"enabled"`
	Now          time.Time             `json:"now"`
	Mode         string                `json:"mode"`
	Pinned       string                `json:"pinned,omitempty"`
	Active       string                `json:"active,omitempty"`
	ActiveManual bool                  `json:"active_manual,omitempty"`
	ActiveReason string                `json:"active_reason,omitempty"`
	ActiveSince  *time.Time            `json:"active_since,omitempty"`
	Routing      string                `json:"routing,omitempty"`
	RoutingNote  string                `json:"routing_note,omitempty"`
	Accounts     []engine.AccountView  `json:"accounts"`
	Sessions     []engine.SessionView  `json:"sessions"`
	SessionCount int                   `json:"session_count"`
	WarmSessions int                   `json:"warm_sessions"`
	Settings     engine.Settings       `json:"settings"`
	Warnings     []string              `json:"warnings,omitempty"`
	History      []engine.HistoryEntry `json:"history"`
	Pause        *engine.PauseView     `json:"pause"`
	Slowdown     slowdownBody          `json:"slowdown"`
	Waiting      int                   `json:"waiting"`
	Windows      []windowRow           `json:"windows"`
	Starter      engine.StarterView    `json:"starter"`
	Picks        int64                 `json:"picks"`
	PickStats    map[string]int64      `json:"pick_stats"`
	LastPoll     *time.Time            `json:"last_poll,omitempty"`
	LastError    string                `json:"last_error"`
}

type slowdownBody struct {
	Seconds int `json:"seconds"`
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (p *Pool) state(historyLimit int) stateBody {
	v := p.Engine.View(historyLimit)
	windows := make([]windowRow, 0, len(v.Accounts))
	for _, a := range v.Accounts {
		windows = append(windows, windowRow{Account: a.ID, Label: a.Label, State: a.Window,
			ResetAt: optionalTime(a.WindowResetAt), AutoStartAt: optionalTime(a.AutoStartAt), Plans: a.Plans})
	}
	return stateBody{
		Provider:     p.provider,
		Enabled:      v.Settings.Enabled,
		Now:          v.Now,
		Mode:         v.Mode,
		Pinned:       v.Pinned,
		Active:       v.Active,
		ActiveManual: v.ActiveManual,
		ActiveReason: v.ActiveReason,
		ActiveSince:  optionalTime(v.ActiveSince),
		Routing:      v.Routing,
		RoutingNote:  v.RoutingNote,
		Accounts:     v.Accounts,
		Sessions:     p.Engine.SessionViews(),
		SessionCount: v.Sessions,
		WarmSessions: v.WarmSessions,
		Settings:     v.Settings,
		Warnings:     v.Warnings,
		History:      v.History,
		Pause:        v.Pause,
		Slowdown:     slowdownBody{Seconds: v.SlowDownSeconds},
		Waiting:      p.Waiting(),
		Windows:      windows,
		Starter:      v.Starter,
		Picks:        v.Picks,
		PickStats:    v.PickStats,
		LastPoll:     optionalTime(v.LastPoll),
		LastError:    p.LastError(),
	}
}

func handleState(c *gin.Context) {
	r := Current()
	if r == nil {
		jsonError(c, http.StatusServiceUnavailable, "the router has not started")
		return
	}
	limit := 50
	if v, err := strconv.Atoi(c.Query("history")); err == nil && v > 0 && v <= 1000 {
		limit = v
	}
	provider := strings.ToLower(strings.TrimSpace(c.Query("provider")))
	if provider == "" {
		pools := make([]stateBody, 0, len(Providers))
		for _, name := range Providers {
			pools = append(pools, r.pools[name].state(limit))
		}
		c.JSON(http.StatusOK, gin.H{"pools": pools})
		return
	}
	pool := r.Pool(provider)
	if pool == nil {
		jsonError(c, http.StatusBadRequest, "provider must be one of: "+strings.Join(Providers, ", "))
		return
	}
	c.JSON(http.StatusOK, pool.state(limit))
}

func respondState(c *gin.Context, p *Pool) {
	c.JSON(http.StatusOK, p.state(200))
}

func handleMode(c *gin.Context, p *Pool) {
	var req struct {
		Mode    string `json:"mode"`
		Account string `json:"account"`
	}
	if !decodeBody(c, &req) {
		return
	}
	switch strings.ToLower(strings.TrimSpace(req.Mode)) {
	case engine.ModeAuto:
		p.Engine.SetAuto()
	case engine.ModeManual:
		account := strings.TrimSpace(req.Account)
		if account == "" {
			jsonError(c, http.StatusBadRequest, "manual mode needs an account to pin")
			return
		}
		if err := p.Engine.Pin(account); err != nil {
			accountError(c, err)
			return
		}
	default:
		jsonError(c, http.StatusBadRequest, `mode must be "auto" or "manual"`)
		return
	}
	p.Flush()
	respondState(c, p)
}

func handleSelect(c *gin.Context, p *Pool) {
	var req struct {
		Account string `json:"account"`
	}
	if !decodeBody(c, &req) {
		return
	}
	if err := p.Engine.Select(strings.TrimSpace(req.Account)); err != nil {
		accountError(c, err)
		return
	}
	p.Flush()
	respondState(c, p)
}

func handleRefresh(c *gin.Context, p *Pool) {
	p.RequestPoll()
	respondState(c, p)
}

type settingsBody struct {
	Provider string          `json:"provider"`
	Settings engine.Settings `json:"settings"`
	Warnings []string        `json:"warnings"`
}

func handleGetSettings(c *gin.Context, p *Pool) {
	s, warnings := p.Engine.Settings()
	c.JSON(http.StatusOK, settingsBody{Provider: p.provider, Settings: s, Warnings: orEmpty(warnings)})
}

func orEmpty(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func handlePatchSettings(c *gin.Context, p *Pool) {
	settings, _ := p.Engine.Settings()
	if !decodeBody(c, &settings) {
		return
	}
	normalized, warnings := settings.NormalizeFor(p.provider, nil)
	if p.persist {
		if err := saveSettings(p.provider, normalized); err != nil {
			jsonError(c, http.StatusInternalServerError, "could not save the settings")
			return
		}
	}
	p.Engine.Configure(normalized, warnings)
	p.WakeStarter()
	p.Flush()
	c.JSON(http.StatusOK, settingsBody{Provider: p.provider, Settings: normalized, Warnings: orEmpty(warnings)})
}

func handleWindowsStart(c *gin.Context, p *Pool) {
	var req struct {
		Account        string `json:"account"`
		DelayMinutes   int    `json:"delay_minutes"`
		StaggerMinutes int    `json:"stagger_minutes"`
	}
	if !decodeBody(c, &req) {
		return
	}
	if req.DelayMinutes < 0 || req.DelayMinutes > 1440 || req.StaggerMinutes < 0 || req.StaggerMinutes > 720 {
		jsonError(c, http.StatusBadRequest, "delay must be 0-1440 minutes and stagger 0-720 minutes")
		return
	}
	_, err := p.Engine.PlanStarts(strings.TrimSpace(req.Account),
		time.Duration(req.DelayMinutes)*time.Minute, time.Duration(req.StaggerMinutes)*time.Minute)
	if err != nil {
		accountError(c, err)
		return
	}
	p.WakeStarter()
	p.Flush()
	respondState(c, p)
}

func handleWindowsSchedule(c *gin.Context, p *Pool) {
	var req struct {
		Account string    `json:"account"`
		At      time.Time `json:"at"`
		Daily   bool      `json:"daily"`
	}
	if !decodeBody(c, &req) {
		return
	}
	if _, err := p.Engine.PlanManual(strings.TrimSpace(req.Account), req.At, req.Daily); err != nil {
		if errors.Is(err, engine.ErrBadSchedule) {
			jsonError(c, http.StatusBadRequest, "pick a time within the next 7 days")
			return
		}
		accountError(c, err)
		return
	}
	p.WakeStarter()
	p.Flush()
	respondState(c, p)
}

func handleWindowsCancel(c *gin.Context, p *Pool) {
	var req struct {
		ID      string `json:"id"`
		Account string `json:"account"`
	}
	if !decodeBody(c, &req) {
		return
	}
	if _, err := p.Engine.CancelStarts(strings.TrimSpace(req.ID), strings.TrimSpace(req.Account)); err != nil {
		jsonError(c, http.StatusNotFound, "that scheduled start no longer exists")
		return
	}
	p.WakeStarter()
	p.Flush()
	respondState(c, p)
}

func handlePause(c *gin.Context, p *Pool) {
	var req struct {
		Minutes           int `json:"minutes"`
		AutoResumeMinutes int `json:"auto_resume_minutes"`
	}
	if !decodeBody(c, &req) {
		return
	}
	if err := p.Engine.Pause(time.Duration(req.Minutes)*time.Minute, req.AutoResumeMinutes); err != nil {
		jsonError(c, http.StatusBadRequest, fmt.Sprintf("pause for 0 (until resumed) to %d minutes, with auto-resume 0 (off) to %d minutes",
			int(engine.MaxPause/time.Minute), engine.MaxAutoResumeMinutes))
		return
	}
	pauseChanged(c, p)
}

func handleResume(c *gin.Context, p *Pool) {
	var req struct{}
	if !decodeBody(c, &req) {
		return
	}
	p.Engine.Resume()
	pauseChanged(c, p)
}

func handleSlowdown(c *gin.Context, p *Pool) {
	var req struct {
		Seconds int `json:"seconds"`
	}
	if !decodeBody(c, &req) {
		return
	}
	if err := p.Engine.SetSlowDown(req.Seconds); err != nil {
		jsonError(c, http.StatusBadRequest, fmt.Sprintf("slow down must be 0 (off) to %d seconds", engine.MaxSlowDownSeconds))
		return
	}
	pauseChanged(c, p)
}

// pauseChanged applies a pause or slow-down change to held requests at once and
// saves it, so a restart right after keeps it.
func pauseChanged(c *gin.Context, p *Pool) {
	p.WakeHeld()
	p.Flush()
	respondState(c, p)
}
