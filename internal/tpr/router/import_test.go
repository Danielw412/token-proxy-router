package router

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router/engine"
)

const (
	fixtureAlice = "codex-aaaa1111-alice@example.com-plus.json"
	fixtureBob   = "codex-bbbb2222-bob@example.com-plus.json"
)

// pluginStateFixture is a state.json as the old plugin wrote it.
func pluginStateFixture(t *testing.T) string {
	t.Helper()
	body := `{
  "version": 1,
  "mode": "manual",
  "pinned": "` + fixtureBob + `",
  "active": "` + fixtureBob + `",
  "active_reason": "Pinned manually",
  "active_since": "2026-10-25T10:00:00Z",
  "last_switch": "2026-10-25T10:00:00Z",
  "history": [{"at":"2026-10-25T10:00:00Z","kind":"manual","to":"` + fixtureBob + `","reason":"Pinned bob@example.com; manual mode"}],
  "families": {"codex:session-1":{"account":"` + fixtureAlice + `","bound_at":"2026-10-25T09:00:00Z","last_seen":"2026-10-25T09:30:00Z","requests":4}},
  "links": {"codex:child-1":{"parent":"codex:session-1","seen":"2026-10-25T09:20:00Z"}},
  "accounts": {
    "` + fixtureAlice + `": {"id":"` + fixtureAlice + `","label":"alice@example.com","plan":"plus","roster":true,
      "quota":{"short":{"used_percent":12,"window_minutes":300,"reset_at":"2026-10-25T14:00:00Z","observed_at":"2026-10-25T10:00:00Z"},
               "long":{"used_percent":40,"window_minutes":10080,"reset_at":"2026-10-30T14:00:00Z","observed_at":"2026-10-25T10:00:00Z"},
               "observed_at":"2026-10-25T10:00:00Z","source":"response"},
      "last_used":"2026-10-25T09:30:00Z","window_until":"2026-10-25T14:00:00Z"},
    "` + fixtureBob + `": {"label":"bob@example.com","roster":true},
    "codex-null.json": null
  },
  "starts": [{"id":"plan1","account":"` + fixtureAlice + `","at":"2026-10-26T08:00:00Z","daily":true,"source":"manual"}],
  "start_log": [{"at":"2026-10-25T08:00:00Z","account":"` + fixtureAlice + `","status":"started"}],
  "pause": {"since":"2026-10-25T10:30:00Z","auto_resume_minutes":99},
  "slow_down_seconds": 30,
  "some_future_key": {"access_token":"must-not-be-carried-over"}
}`
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func pluginSettingsFixture() map[string]any {
	return map[string]any{
		"enabled":                    true,
		"priority":                   1,
		"deployed_version":           "1.6.0",
		"state_file":                 "/example/state.json",
		"switch_threshold_percent":   7,
		"rank_by":                    []any{"five_hour_reset", "account_order"},
		"switch_back":                true,
		"switch_back_margin_percent": 10,
		"min_dwell_minutes":          25,
		"cache_warm_minutes":         45,
		"account_order":              []any{fixtureBob, fixtureAlice},
		"reserve_accounts":           []any{fixtureBob},
		"disabled_accounts":          []any{},
		"manual_fallback":            "strict",
		"quota_poll_minutes":         15,
		"auto_start_windows":         true,
		"not_a_real_setting":         "ignored",
	}
}

func TestImportConvertsStateAndSettingsWithNoServerRunning(t *testing.T) {
	t.Setenv(home.EnvHome, t.TempDir())
	if err := ImportPluginState(pluginStateFixture(t), pluginSettingsFixture()); err != nil {
		t.Fatal(err)
	}

	st, ok, err := engine.LoadState(engineStatePath(engine.ProviderCodex))
	if err != nil || !ok {
		t.Fatalf("load state: ok=%v err=%v", ok, err)
	}
	if st.Mode != engine.ModeManual || st.Pinned != fixtureBob || st.Active != fixtureBob {
		t.Fatalf("mode/pin not carried: %+v", st)
	}
	if b := st.Families["codex:session-1"]; b == nil || b.Account != fixtureAlice || b.Requests != 4 {
		t.Fatalf("session binding = %+v", b)
	}
	if st.Links["codex:child-1"].Parent != "codex:session-1" {
		t.Fatalf("links = %+v", st.Links)
	}
	alice := st.Accounts[fixtureAlice]
	if alice == nil || alice.Quota == nil || alice.Quota.Long == nil || alice.Quota.Long.UsedPercent != 40 || alice.Plan != "plus" {
		t.Fatalf("alice = %+v", alice)
	}
	if st.Accounts[fixtureBob] == nil || st.Accounts[fixtureBob].ID != fixtureBob {
		t.Fatalf("bob's ID must be filled in from its key: %+v", st.Accounts[fixtureBob])
	}
	if _, present := st.Accounts["codex-null.json"]; present {
		t.Fatal("a null account must be dropped")
	}
	if len(st.Starts) != 1 || !st.Starts[0].Daily {
		t.Fatalf("starts = %+v", st.Starts)
	}
	if st.Pause == nil || st.Pause.AutoResumeMinutes != 0 {
		t.Fatalf("an out-of-range auto-resume must be repaired: %+v", st.Pause)
	}
	if st.SlowDownSeconds != 30 {
		t.Fatalf("slow down = %d", st.SlowDownSeconds)
	}

	raw, err := os.ReadFile(engineStatePath(engine.ProviderCodex))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "must-not-be-carried-over") {
		t.Fatal("unknown state keys must not be carried over")
	}

	settings, warnings := loadSettings(engine.ProviderCodex)
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	if settings.SwitchThresholdPercent != 7 || settings.MinDwellMinutes != 25 || settings.CacheWarmMinutes != 45 ||
		settings.SwitchBackMarginPercent != 10 || settings.ManualFallback != engine.FallbackStrict ||
		settings.QuotaPollMinutes != 15 || !settings.AutoStartWindows {
		t.Fatalf("settings = %+v", settings)
	}
	if settings.RankBy[0] != engine.RankFiveHourReset || len(settings.RankBy) != len(engine.RankCriteria) {
		t.Fatalf("rank_by must be completed in order: %v", settings.RankBy)
	}
	if settings.AccountOrder[0] != fixtureBob || settings.ReserveAccounts[0] != fixtureBob {
		t.Fatalf("lists = %+v", settings)
	}
}

func TestImportIsIdempotentAndCannotUndoLaterChanges(t *testing.T) {
	t.Setenv(home.EnvHome, t.TempDir())
	state, settings := pluginStateFixture(t), pluginSettingsFixture()
	if err := ImportPluginState(state, settings); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(engineStatePath(engine.ProviderCodex))
	if err != nil {
		t.Fatal(err)
	}
	if err = ImportPluginState(state, settings); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(engineStatePath(engine.ProviderCodex))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("a second import changed the state file")
	}

	// The router runs and moves on; importing the same inputs must not revert it.
	s := engine.DefaultSettingsFor(engine.ProviderCodex)
	s.SwitchThresholdPercent = 33
	if err = saveSettings(engine.ProviderCodex, s); err != nil {
		t.Fatal(err)
	}
	if err = ImportPluginState(state, settings); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadSettings(engine.ProviderCodex); got.SwitchThresholdPercent != 33 {
		t.Fatalf("threshold reverted to %v", got.SwitchThresholdPercent)
	}

	// Different inputs are a new import and do apply.
	settings["switch_threshold_percent"] = 9
	if err = ImportPluginState(state, settings); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadSettings(engine.ProviderCodex); got.SwitchThresholdPercent != 9 {
		t.Fatalf("threshold = %v, want the new import's 9", got.SwitchThresholdPercent)
	}
}

func TestImportToleratesMissingInputsAndClampsBadValues(t *testing.T) {
	t.Setenv(home.EnvHome, t.TempDir())
	missing := filepath.Join(t.TempDir(), "absent.json")
	if err := ImportPluginState(missing, map[string]any{"switch_threshold_percent": 500, "strategy": "weird"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(engineStatePath(engine.ProviderCodex)); !os.IsNotExist(err) {
		t.Fatalf("no state input must write no state file: %v", err)
	}
	s, _ := loadSettings(engine.ProviderCodex)
	if s.SwitchThresholdPercent != 90 || s.Strategy != engine.StrategyBalanced {
		t.Fatalf("settings = %+v", s)
	}
	if err := ImportPluginState("", nil); err != nil {
		t.Fatal(err)
	}
}

func TestImportRejectsDamagedStateWithoutWritingAnything(t *testing.T) {
	t.Setenv(home.EnvHome, t.TempDir())
	bad := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(bad, []byte(`{"accounts":`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := ImportPluginState(bad, pluginSettingsFixture())
	if err == nil || !strings.Contains(err.Error(), "parse plugin state") {
		t.Fatalf("err = %v", err)
	}
	if _, errStat := os.Stat(settingsPath(engine.ProviderCodex)); !os.IsNotExist(errStat) {
		t.Fatalf("settings written despite a failed import: %v", errStat)
	}
}

func TestImportedStateRestoresIntoARunningEngine(t *testing.T) {
	t.Setenv(home.EnvHome, t.TempDir())
	if err := ImportPluginState(pluginStateFixture(t), pluginSettingsFixture()); err != nil {
		t.Fatal(err)
	}
	st, ok, err := engine.LoadState(engineStatePath(engine.ProviderCodex))
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	settings, _ := loadSettings(engine.ProviderCodex)
	clock := time.Date(2026, 10, 25, 11, 0, 0, 0, time.UTC)
	e := engine.New(settings, nil, func() time.Time { return clock })
	e.Restore(st)
	got := e.Pick(engine.PickRequest{
		Candidates: []engine.Candidate{{ID: fixtureAlice, Provider: "codex"}, {ID: fixtureBob, Provider: "codex"}},
		Headers:    map[string][]string{"Session_id": {"session-1"}},
	})
	if got.AuthID != fixtureBob || got.Reason != "manual pin" {
		t.Fatalf("pick = %+v", got)
	}
	var v struct {
		Mode string `json:"mode"`
	}
	raw, errMarshal := json.Marshal(e.View(5))
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if err = json.Unmarshal(raw, &v); err != nil || v.Mode != engine.ModeManual {
		t.Fatalf("view mode = %q err=%v", v.Mode, err)
	}
}
