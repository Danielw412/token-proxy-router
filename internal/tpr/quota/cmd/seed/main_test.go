package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/quota"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router/engine"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/store"
)

var now = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func TestRefusesWithoutAnExplicitHome(t *testing.T) {
	t.Setenv(home.EnvHome, "")
	if _, err := run(now); err == nil || !strings.Contains(err.Error(), "TPR_HOME") {
		t.Fatalf("err = %v", err)
	}
}

func TestSeededFolderDrivesARealisticChart(t *testing.T) {
	t.Setenv(home.EnvHome, t.TempDir())
	total, err := run(now)
	if err != nil {
		t.Fatal(err)
	}
	if total < 9*14*24*3 {
		t.Fatalf("only %d samples", total)
	}
	for _, a := range accounts() {
		if !strings.HasSuffix(a.email, "@example.com") {
			t.Fatalf("a seeded account is not fake: %s", a.email)
		}
		raw, errRead := os.ReadFile(filepath.Join(home.AuthDir(), a.id()))
		if errRead != nil {
			t.Fatal(errRead)
		}
		if !strings.Contains(string(raw), fakeToken) {
			t.Fatalf("%s: %s", a.id(), raw)
		}
	}
	for _, provider := range router.Providers {
		s, _ := loadSettings(t, provider)
		if s.QuotaPollMinutes != 0 {
			t.Fatalf("%s polling must be off: %+v", provider, s)
		}
	}

	st, err := store.Open(store.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	clock := func() time.Time { return now }
	r := router.NewWithClock(rosterlessHost{}, clock)
	service := quota.New(st, r, clock)
	pools, _, err := service.Pools(quota.Query{From: now.Add(-14 * 24 * time.Hour), To: now.Add(7 * 24 * time.Hour),
		Burn: 6 * time.Hour, Window: quota.WindowWeekly, Future: quota.FutureSame})
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 2 {
		t.Fatalf("pools = %d", len(pools))
	}
	for _, pool := range pools {
		wantAccounts := 6
		if pool.Provider == engine.ProviderCodex {
			wantAccounts = 3
		}
		if len(pool.Accounts) != wantAccounts {
			t.Fatalf("%s accounts = %d", pool.Provider, len(pool.Accounts))
		}
		if pool.RemainingPercent <= 0 || pool.RemainingPercent > pool.CapacityPercent {
			t.Fatalf("%s: %v of %v", pool.Provider, pool.RemainingPercent, pool.CapacityPercent)
		}
		if len(pool.Recorded) < 500 {
			t.Fatalf("%s recorded only %d points", pool.Provider, len(pool.Recorded))
		}
		if pool.Coverage.FirstSampleAt == nil || now.Sub(*pool.Coverage.FirstSampleAt) < 13*24*time.Hour {
			t.Fatalf("%s coverage = %+v", pool.Provider, pool.Coverage)
		}
		var low, high = pool.Recorded[0].Percent, pool.Recorded[0].Percent
		for _, pt := range pool.Recorded {
			low, high = min(low, pt.Percent), max(high, pt.Percent)
		}
		if high-low < 20 {
			t.Fatalf("%s history is too flat to check a chart against: %v to %v", pool.Provider, low, high)
		}
		var historyResets int
		for _, reset := range pool.Resets {
			if !reset.Projected {
				historyResets++
			}
		}
		if historyResets == 0 {
			t.Fatalf("%s history has no resets", pool.Provider)
		}
	}
	if !strings.Contains(pools[0].WeightNote, "Max 20x") {
		t.Fatalf("claude weight note = %q", pools[0].WeightNote)
	}
	if !strings.Contains(pools[1].WeightNote, "Pro") {
		t.Fatalf("codex weight note = %q", pools[1].WeightNote)
	}
}

func TestSeedingTwiceIsSafe(t *testing.T) {
	t.Setenv(home.EnvHome, t.TempDir())
	if _, err := run(now); err != nil {
		t.Fatal(err)
	}
	if _, err := run(now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

type rosterlessHost struct{ router.Host }

func (rosterlessHost) Accounts() []engine.HostAccount { return nil }

func loadSettings(t *testing.T, provider string) (engine.Settings, bool) {
	t.Helper()
	raw, err := os.ReadFile(router.SettingsPath(provider))
	if err != nil {
		t.Fatal(err)
	}
	s, warnings := engine.ParseSettings(raw)
	return s, len(warnings) == 0
}
