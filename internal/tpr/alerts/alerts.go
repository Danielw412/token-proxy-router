// Package alerts watches the quota forecast and tells the owner when a pool is
// about to run out, an account runs low, or quota comes back. One condition sends
// one notification, until it clears.
package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/quota"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/store"
	log "github.com/sirupsen/logrus"
)

// Event kinds.
const (
	KindPoolEmptySoon = "pool_empty_soon"
	KindAccountLow    = "account_low"
	KindQuotaReset    = "quota_reset"
	KindCleared       = "cleared"
	KindDeliveryError = "delivery_failed"
)

const (
	settingsKey  = "alerts.settings"
	activeKey    = "alerts.active"
	resetMarkKey = "alerts.reset_mark"

	evaluateEvery = time.Minute
	maxChannels   = 10
	maxEventsRead = 500

	// Clearing is harder than triggering, so a value hovering at a threshold does
	// not notify over and over.
	accountClearMargin = 2.0
	poolClearMargin    = 30 * time.Minute

	forecastBurn     = 3 * time.Hour
	forecastHorizon  = 14 * 24 * time.Hour
	maxResetLookback = 24 * time.Hour
)

// Channel is one notification destination. Its URL is a secret.
type Channel struct {
	Type string `json:"type"`
	URL  string `json:"url"`
	Name string `json:"name"`
}

// Settings is the owner's alert configuration.
type Settings struct {
	Enabled bool `json:"enabled"`
	// PoolEmptyWithinHours alerts when the forecast has a pool running out this
	// soon. 0 turns it off.
	PoolEmptyWithinHours float64 `json:"pool_empty_within_hours"`
	// AccountBelowPercent alerts when an account's weekly quota falls below this.
	// 0 turns it off.
	AccountBelowPercent float64   `json:"account_below_percent"`
	NotifyOnReset       bool      `json:"notify_on_reset"`
	Channels            []Channel `json:"channels"`
}

// DefaultSettings watches for trouble and shows it in the panel. Nothing is sent
// anywhere until the owner adds a channel.
func DefaultSettings() Settings {
	return Settings{Enabled: true, PoolEmptyWithinHours: 6, AccountBelowPercent: 10, Channels: []Channel{}}
}

// Condition is something that is true right now.
type Condition struct {
	Key      string    `json:"key"`
	Kind     string    `json:"kind"`
	Provider string    `json:"provider"`
	Account  string    `json:"account,omitempty"`
	Message  string    `json:"message"`
	Since    time.Time `json:"since"`
}

// Event is one entry of the events log.
type Event struct {
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"`
	Provider string    `json:"provider"`
	Account  string    `json:"account"`
	Message  string    `json:"message"`
}

// Forecaster supplies the pool numbers. quota.Service implements it.
type Forecaster interface {
	Pools(q quota.Query) ([]quota.PoolView, time.Time, error)
}

// Service evaluates conditions on a timer.
type Service struct {
	store    *store.Store
	forecast Forecaster
	send     Sender
	now      func() time.Time

	mu        sync.Mutex
	settings  Settings
	active    map[string]Condition
	resetMark time.Time

	cancel context.CancelFunc
	done   chan struct{}
}

var current atomic.Pointer[Service]

// Current returns the running service, or nil before it starts.
func Current() *Service { return current.Load() }

// New builds a service, restoring its settings and active conditions.
func New(st *store.Store, f Forecaster, send Sender, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	if send == nil {
		send = deliver
	}
	s := &Service{store: st, forecast: f, send: send, now: now, settings: DefaultSettings(), active: map[string]Condition{}}
	if _, err := st.GetJSON(settingsKey, &s.settings); err != nil {
		log.Warnf("alerts: saved settings could not be read: %v", err)
	}
	if s.settings.Channels == nil {
		s.settings.Channels = []Channel{}
	}
	if _, err := st.GetJSON(activeKey, &s.active); err != nil {
		log.Warnf("alerts: saved alert state could not be read: %v", err)
	}
	if s.active == nil {
		s.active = map[string]Condition{}
	}
	if ok, err := st.GetJSON(resetMarkKey, &s.resetMark); err != nil || !ok {
		s.resetMark = now()
	}
	return s
}

// Start begins evaluating on a timer and makes the service current.
func Start(st *store.Store, f Forecaster) *Service {
	s := New(st, f, nil, time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	go s.loop(ctx)
	current.Store(s)
	return s
}

// Stop ends the evaluation loop.
func (s *Service) Stop() {
	current.CompareAndSwap(s, nil)
	if s.cancel != nil {
		s.cancel()
		<-s.done
	}
}

func (s *Service) loop(ctx context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(evaluateEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.safeEvaluate(ctx)
		}
	}
}

func (s *Service) safeEvaluate(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("alerts: recovered from panic: %v", r)
		}
	}()
	if err := s.Evaluate(ctx); err != nil {
		log.Warnf("alerts: evaluation failed: %v", err)
	}
}

// Evaluate compares the forecast with what was already reported. A condition that
// appears is logged and sent once; one that goes away is logged and may fire again.
func (s *Service) Evaluate(ctx context.Context) error {
	s.mu.Lock()
	settings := s.settings
	s.mu.Unlock()
	now := s.now()
	if !settings.Enabled {
		s.mu.Lock()
		wasActive := len(s.active) > 0
		s.active = map[string]Condition{}
		s.mu.Unlock()
		if wasActive {
			return s.store.PutJSON(activeKey, map[string]Condition{})
		}
		return nil
	}
	// Only recent history matters here, so a timer this frequent stays cheap. The
	// lookback covers resets since the last run, within reason after a long stop.
	s.mu.Lock()
	since := s.resetMark
	s.mu.Unlock()
	if earliest := now.Add(-maxResetLookback); since.Before(earliest) {
		since = earliest
	}
	pools, _, err := s.forecast.Pools(quota.Query{
		From:   since,
		To:     now.Add(forecastHorizon),
		Burn:   forecastBurn,
		Window: quota.WindowWeekly,
		Future: quota.FutureSame,
	})
	if err != nil {
		return err
	}

	s.mu.Lock()
	conditions := Conditions(now, pools, settings, s.active)
	var appeared, cleared []Condition
	next := make(map[string]Condition, len(conditions))
	for _, c := range conditions {
		if old, was := s.active[c.Key]; was {
			c.Since = old.Since
		} else {
			appeared = append(appeared, c)
		}
		next[c.Key] = c
	}
	for key, c := range s.active {
		if _, still := next[key]; !still {
			cleared = append(cleared, c)
		}
	}
	changed := len(appeared) > 0 || len(cleared) > 0
	s.active = next
	resets, mark := newResets(pools, s.resetMark, now, settings)
	s.resetMark = mark
	s.mu.Unlock()

	if changed {
		if errSave := s.store.PutJSON(activeKey, next); errSave != nil {
			log.Warnf("alerts: could not save alert state: %v", errSave)
		}
	}
	if errSave := s.store.PutJSON(resetMarkKey, mark); errSave != nil {
		log.Warnf("alerts: could not save reset mark: %v", errSave)
	}
	sort.Slice(appeared, func(i, j int) bool { return appeared[i].Key < appeared[j].Key })
	sort.Slice(cleared, func(i, j int) bool { return cleared[i].Key < cleared[j].Key })
	for _, c := range cleared {
		s.logEvent(Event{At: now, Kind: KindCleared, Provider: c.Provider, Account: c.Account, Message: "Cleared: " + c.Message})
	}
	for _, c := range append(appeared, resets...) {
		s.logEvent(Event{At: now, Kind: c.Kind, Provider: c.Provider, Account: c.Account, Message: c.Message})
		s.notify(ctx, settings.Channels, c.Message, c.Provider)
	}
	return nil
}

// Conditions lists what is true now. It is pure, for testing. An account or pool
// already active needs to clear by a margin before it stops being a condition.
func Conditions(now time.Time, pools []quota.PoolView, settings Settings, active map[string]Condition) []Condition {
	var out []Condition
	for _, pool := range pools {
		if within := settings.PoolEmptyWithinHours; within > 0 && pool.PoolEmptyAt != nil {
			key := KindPoolEmptySoon + ":" + pool.Provider
			limit := now.Add(time.Duration(within * float64(time.Hour)))
			if _, was := active[key]; was {
				limit = limit.Add(poolClearMargin)
			}
			if !pool.PoolEmptyAt.After(limit) {
				out = append(out, Condition{Key: key, Kind: KindPoolEmptySoon, Provider: pool.Provider, Since: now,
					Message: fmt.Sprintf("%s pool is forecast to run out in %s", displayName(pool.Provider), untilText(pool.PoolEmptyAt.Sub(now)))})
			}
		}
		if below := settings.AccountBelowPercent; below > 0 {
			for _, account := range pool.Accounts {
				if account.RemainingPercent == nil || account.Status == "off" {
					continue
				}
				key := KindAccountLow + ":" + pool.Provider + ":" + account.ID
				limit := below
				if _, was := active[key]; was {
					limit += accountClearMargin
				}
				if *account.RemainingPercent < limit {
					out = append(out, Condition{Key: key, Kind: KindAccountLow, Provider: pool.Provider, Account: account.ID, Since: now,
						Message: fmt.Sprintf("%s account %s has %.0f%% of its weekly quota left", displayName(pool.Provider), account.Label, *account.RemainingPercent)})
				}
			}
		}
	}
	return out
}

// newResets returns one-shot notices for resets recorded since the mark.
func newResets(pools []quota.PoolView, mark, now time.Time, settings Settings) ([]Condition, time.Time) {
	var out []Condition
	if settings.NotifyOnReset {
		for _, pool := range pools {
			for _, reset := range pool.Resets {
				if reset.Projected || !reset.At.After(mark) || reset.At.After(now) {
					continue
				}
				out = append(out, Condition{Kind: KindQuotaReset, Provider: pool.Provider, Since: reset.At,
					Message: fmt.Sprintf("%s pool got %.0f%% of quota back", displayName(pool.Provider), reset.DeltaPercent)})
			}
		}
	}
	return out, now
}

func displayName(provider string) string {
	switch provider {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	}
	return provider
}

func untilText(d time.Duration) string {
	if d < time.Minute {
		return "under a minute"
	}
	d = d.Round(time.Minute)
	h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
	switch {
	case h == 0:
		return fmt.Sprintf("%d min", m)
	case m == 0:
		return fmt.Sprintf("%d h", h)
	}
	return fmt.Sprintf("%d h %d min", h, m)
}

func (s *Service) logEvent(e Event) {
	if err := s.store.AppendEvent(e.At, e); err != nil {
		log.Warnf("alerts: could not log an event: %v", err)
	}
}

// notify sends to every channel in the background, once. A failure is reported in
// the events log and never retried.
func (s *Service) notify(ctx context.Context, channels []Channel, message, provider string) {
	if len(channels) == 0 {
		return
	}
	outgoing := MaskEmails(message)
	for _, channel := range channels {
		if err := s.send(ctx, channel, "Token Proxy Router", outgoing); err != nil {
			s.logEvent(Event{At: s.now(), Kind: KindDeliveryError, Provider: provider,
				Message: fmt.Sprintf("Could not notify %s: %s", channelLabel(channel), err.Error())})
		}
	}
}

func channelLabel(c Channel) string {
	if c.Name != "" {
		return c.Name
	}
	return c.Type
}

var emailPattern = regexp.MustCompile(`([A-Za-z0-9._%+-])[A-Za-z0-9._%+-]*@([A-Za-z0-9.-]+\.[A-Za-z]{2,})`)

// MaskEmails shortens addresses in text leaving this machine. The panel shows
// them, but a notification lands on someone else's service.
func MaskEmails(text string) string {
	return emailPattern.ReplaceAllString(text, "$1…@$2")
}

// SettingsView is what GET /alerts returns: settings with masked URLs, plus what
// is active now for the panel's banner.
type SettingsView struct {
	Settings
	Active []Condition `json:"active"`
}

// View returns the settings with masked channel URLs and the active conditions.
func (s *Service) View() SettingsView {
	s.mu.Lock()
	defer s.mu.Unlock()
	view := SettingsView{Settings: s.settings, Active: s.activeLocked()}
	view.Channels = make([]Channel, 0, len(s.settings.Channels))
	for _, channel := range s.settings.Channels {
		channel.URL = MaskURL(channel.URL)
		view.Channels = append(view.Channels, channel)
	}
	return view
}

func (s *Service) activeLocked() []Condition {
	out := make([]Condition, 0, len(s.active))
	for _, c := range s.active {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// ErrInvalidSettings wraps a message safe to show the owner.
var ErrInvalidSettings = errors.New("invalid alert settings")

// Update validates and saves new settings. A channel still showing its masked URL
// keeps the stored one, so the panel can save without ever seeing the secret.
func (s *Service) Update(next Settings) (SettingsView, error) {
	if next.PoolEmptyWithinHours < 0 || next.PoolEmptyWithinHours > 24*14 {
		return SettingsView{}, fmt.Errorf("%w: pool_empty_within_hours must be 0 to %d", ErrInvalidSettings, 24*14)
	}
	if next.AccountBelowPercent < 0 || next.AccountBelowPercent > 100 {
		return SettingsView{}, fmt.Errorf("%w: account_below_percent must be 0 to 100", ErrInvalidSettings)
	}
	if len(next.Channels) > maxChannels {
		return SettingsView{}, fmt.Errorf("%w: at most %d channels", ErrInvalidSettings, maxChannels)
	}
	s.mu.Lock()
	stored := append([]Channel(nil), s.settings.Channels...)
	s.mu.Unlock()
	channels := make([]Channel, 0, len(next.Channels))
	for i, channel := range next.Channels {
		channel.Type = strings.ToLower(strings.TrimSpace(channel.Type))
		channel.Name = strings.TrimSpace(channel.Name)
		channel.URL = strings.TrimSpace(channel.URL)
		switch channel.Type {
		case "ntfy", "discord", "slack", "webhook":
		default:
			return SettingsView{}, fmt.Errorf("%w: channel %d type must be ntfy, discord, slack or webhook", ErrInvalidSettings, i+1)
		}
		if strings.Contains(channel.URL, maskMark) {
			found := false
			for j, old := range stored {
				if MaskURL(old.URL) == channel.URL && old.Type == channel.Type {
					channel.URL = old.URL
					stored = append(stored[:j], stored[j+1:]...)
					found = true
					break
				}
			}
			if !found {
				return SettingsView{}, fmt.Errorf("%w: channel %d shows a masked URL that no longer exists; enter the full URL", ErrInvalidSettings, i+1)
			}
		} else if err := validateURL(channel.URL); err != nil {
			return SettingsView{}, fmt.Errorf("%w: channel %d %s", ErrInvalidSettings, i+1, err.Error())
		}
		channels = append(channels, channel)
	}
	next.Channels = channels
	if err := s.store.PutJSON(settingsKey, next); err != nil {
		return SettingsView{}, fmt.Errorf("save alert settings: %w", err)
	}
	s.mu.Lock()
	s.settings = next
	s.mu.Unlock()
	return s.View(), nil
}

func validateURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("needs a full http or https URL")
	}
	return nil
}

// maskMark is what makes a URL recognizably masked.
const maskMark = "…"

// MaskURL shows only where a channel points. A webhook's path is its secret, and so
// is an ntfy topic, so nothing after the host survives.
func MaskURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return maskMark
	}
	return parsed.Scheme + "://" + parsed.Host + "/" + maskMark
}

// TestResult is one channel's outcome of POST /alerts/test.
type TestResult struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// SendTest sends a test message to every channel and reports each result.
func (s *Service) SendTest(ctx context.Context) []TestResult {
	s.mu.Lock()
	channels := append([]Channel(nil), s.settings.Channels...)
	s.mu.Unlock()
	results := make([]TestResult, 0, len(channels))
	for _, channel := range channels {
		result := TestResult{Name: channelLabel(channel), OK: true}
		if err := s.send(ctx, channel, "Token Proxy Router", "Test notification: this channel works."); err != nil {
			result.OK, result.Error = false, err.Error()
		}
		results = append(results, result)
	}
	return results
}

// Events returns logged events, newest first.
func (s *Service) Events(limit int) ([]Event, error) {
	raw, err := s.store.RecentEvents(limit)
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(raw))
	for _, item := range raw {
		var e Event
		if json.Unmarshal(item, &e) == nil {
			out = append(out, e)
		}
	}
	return out, nil
}
