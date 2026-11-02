// Command seed writes realistic synthetic quota history for fake example.com
// accounts into $TPR_HOME, so the panel can be checked against a realistic chart:
// six Claude and three Codex accounts over two weeks.
//
//	TPR_HOME=/tmp/tpr-dev go run ./internal/tpr/quota/cmd/seed
//
// It refuses to run unless TPR_HOME is set, so it cannot touch a real data folder.
// The accounts' tokens are fake, and polling is turned off in the settings it
// writes, so a server started on this folder sends nothing to any provider unless
// a client sends it a request.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router/engine"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/store"
)

const (
	days      = 14
	step      = 5 * time.Minute
	idleEvery = 3 // idle accounts report every third step, as polling would
	fakeToken = "seed-token-not-real"
	shortSpan = 5 * time.Hour
	longSpan  = 7 * 24 * time.Hour
	threshold = 5.0
)

type fakeAccount struct {
	provider string
	email    string
	plan     string
	// weeklyOffset staggers when the account's weekly window resets.
	weeklyOffset time.Duration
	// weeklyCost scales how much of the weekly window one unit of work uses: a
	// bigger plan moves less per unit.
	weeklyCost float64
}

func accounts() []fakeAccount {
	return []fakeAccount{
		{engine.ProviderClaude, "alice@example.com", "max5", 18 * time.Hour, 1},
		{engine.ProviderClaude, "bob@example.com", "max5", 40 * time.Hour, 1},
		{engine.ProviderClaude, "carol@example.com", "max5", 65 * time.Hour, 1},
		{engine.ProviderClaude, "dave@example.com", "max5", 90 * time.Hour, 1},
		{engine.ProviderClaude, "erin@example.com", "max5", 115 * time.Hour, 1},
		{engine.ProviderClaude, "frank@example.com", "max20", 140 * time.Hour, 0.25},
		{engine.ProviderCodex, "grace@example.com", "plus", 30 * time.Hour, 1},
		{engine.ProviderCodex, "heidi@example.com", "plus", 80 * time.Hour, 1},
		{engine.ProviderCodex, "ivan@example.com", "pro", 130 * time.Hour, 0.05},
	}
}

func (a fakeAccount) id() string { return a.provider + "-" + a.email + "-" + a.plan + ".json" }

type simState struct {
	fakeAccount
	shortUsed, longUsed   float64
	shortReset, longReset time.Time
}

// demand is how much work arrives per step: a working-hours curve with lighter
// weekends and some noise, so the chart has the shape real use has.
func demand(at time.Time, rng *rand.Rand) float64 {
	hour := float64(at.Hour()) + float64(at.Minute())/60
	curve := math.Max(0, math.Sin((hour-7)/12*math.Pi))
	if hour < 7 || hour > 19 {
		curve = 0.03
	}
	if wd := at.Weekday(); wd == time.Saturday || wd == time.Sunday {
		curve *= 0.25
	}
	return curve * (0.5 + rng.Float64())
}

// simulate runs one pool's two weeks with the router's one-current-account rule and
// returns the samples plus each account's final numbers.
func simulate(pool []fakeAccount, now time.Time, rng *rand.Rand) ([]store.SampleRecord, []*simState, int) {
	start := now.Add(-days * 24 * time.Hour)
	states := make([]*simState, 0, len(pool))
	for _, a := range pool {
		// Anchor each weekly window so its resets fall on a steady schedule.
		anchor := start.Add(a.weeklyOffset - longSpan)
		for !anchor.After(start) {
			anchor = anchor.Add(longSpan)
		}
		states = append(states, &simState{fakeAccount: a, longReset: anchor})
	}
	current := 0
	var records []store.SampleRecord
	tick := 0
	for at := start; !at.After(now); at = at.Add(step) {
		tick++
		for _, s := range states {
			if !at.Before(s.longReset) {
				s.longUsed = 0
				s.longReset = s.longReset.Add(longSpan)
			}
			if !s.shortReset.IsZero() && !at.Before(s.shortReset) {
				s.shortUsed = 0
				s.shortReset = time.Time{}
			}
		}
		work := demand(at, rng)
		// The current account takes the work until it runs low, as the router would.
		for tried := 0; tried < len(states); tried++ {
			s := states[current]
			if s.longUsed < 100-threshold && s.shortUsed < 100-threshold {
				break
			}
			current = (current + 1) % len(states)
		}
		active := states[current]
		if work > 0.02 && active.longUsed < 100-threshold && active.shortUsed < 100-threshold {
			if active.shortReset.IsZero() {
				active.shortReset = at.Add(shortSpan)
			}
			active.shortUsed = math.Min(100, active.shortUsed+work*1.6*active.weeklyCost)
			active.longUsed = math.Min(100, active.longUsed+work*0.28*active.weeklyCost)
		}
		for i, s := range states {
			if i != current && tick%idleEvery != 0 {
				continue
			}
			source := "poll"
			if i == current && work > 0.02 {
				source = "response"
			}
			records = append(records, store.SampleRecord{Provider: s.provider, Account: s.id(), Sample: store.Sample{
				At:     at,
				Source: source,
				Plan:   s.plan,
				Short:  &store.WindowSample{Remaining: 100 - s.shortUsed, ResetAt: s.shortReset},
				Long:   &store.WindowSample{Remaining: 100 - s.longUsed, ResetAt: s.longReset},
			}})
		}
	}
	return records, states, current
}

func writeJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// run does the work; main only reports. It returns how many samples it wrote.
func run(now time.Time) (int, error) {
	if os.Getenv(home.EnvHome) == "" {
		return 0, errors.New("TPR_HOME is not set. Refusing to run: this tool writes fake accounts and history into the data folder.\nSet it to a throwaway folder: TPR_HOME=/tmp/tpr-dev go run ./internal/tpr/quota/cmd/seed")
	}
	if err := home.Ensure(); err != nil {
		return 0, fmt.Errorf("create the data folder: %w", err)
	}
	st, err := store.Open(store.DefaultPath())
	if err != nil {
		return 0, err
	}
	defer func() { _ = st.Close() }()

	byPool := map[string][]fakeAccount{}
	for _, a := range accounts() {
		byPool[a.provider] = append(byPool[a.provider], a)
	}
	rng := rand.New(rand.NewPCG(20261026, 9))
	total := 0
	for _, provider := range router.Providers {
		records, states, current := simulate(byPool[provider], now, rng)
		for len(records) > 0 {
			batch := min(len(records), 5000)
			if errAppend := st.AppendSamples(records[:batch]); errAppend != nil {
				return total, fmt.Errorf("write samples: %w", errAppend)
			}
			total += batch
			records = records[batch:]
		}
		if errState := writePoolFiles(provider, states, current, now); errState != nil {
			return total, errState
		}
	}
	return total, nil
}

// writePoolFiles writes the fake auth files and the pool's state and settings, so a
// server started on this folder lists the accounts with their latest numbers.
func writePoolFiles(provider string, states []*simState, current int, now time.Time) error {
	state := engine.State{
		Version: 1, Mode: engine.ModeAuto, Active: states[current].id(), ActiveReason: "Seeded for development",
		ActiveSince: now, LastSwitch: now, Families: map[string]*engine.Binding{}, Links: map[string]engine.Link{},
		Accounts: map[string]*engine.Account{},
		History: []engine.HistoryEntry{{At: now, Kind: "switch", To: states[current].id(),
			Reason: "Seeded for development: synthetic history for fake accounts"}},
	}
	for _, s := range states {
		auth := map[string]any{"type": provider, "email": s.email, "access_token": fakeToken, "plan_type": s.plan,
			"expired": now.Add(365 * 24 * time.Hour).UTC().Format(time.RFC3339)}
		if err := writeJSON(filepath.Join(home.AuthDir(), s.id()), auth); err != nil {
			return fmt.Errorf("write auth file: %w", err)
		}
		state.Accounts[s.id()] = &engine.Account{
			ID: s.id(), Label: s.email, Email: s.email, Plan: s.plan, EverInRoster: true,
			Quota: &engine.Quota{
				Short:      &engine.Window{UsedPercent: s.shortUsed, WindowMinutes: 300, ResetAt: s.shortReset, ObservedAt: now},
				Long:       &engine.Window{UsedPercent: s.longUsed, WindowMinutes: 10080, ResetAt: s.longReset, ObservedAt: now},
				ObservedAt: now, Source: "poll", Plan: s.plan,
			},
			LastPolled: now,
		}
	}
	if err := engine.SaveState(router.StatePath(provider), state); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	settings := engine.DefaultSettingsFor(provider)
	settings.QuotaPollMinutes = 0 // the tokens are fake: never ask a provider about them
	if err := writeJSON(router.SettingsPath(provider), settings); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	return nil
}

func main() {
	total, err := run(time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Wrote %d samples for %d fake accounts into %s\n", total, len(accounts()), home.Dir())
}
