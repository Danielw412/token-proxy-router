package engine

import (
	"testing"
	"time"
)

func staggered(s *Settings) {
	s.AutoStartWindows = true
	s.StaggerWindows = true
	s.AccountOrder = []string{accA, accB, accC}
}

// startDue expects exactly one automatic start, for want, and records it as
// started.
func startDue(t *testing.T, e *Engine, clk *fakeClock, want string) {
	t.Helper()
	due := e.DueStarts()
	if len(due) != 1 || due[0].Account != want || due[0].Source != SourceAuto {
		t.Fatalf("want one automatic start for %s at %v, got %+v", want, clk.now().Sub(t0), due)
	}
	e.RecordStart(StartResult{At: clk.now(), Account: want, Source: SourceAuto, Status: StartStarted, ResetAt: clk.now().Add(5 * time.Hour)})
}

func autoStartAtFor(e *Engine, id string) time.Time {
	for _, a := range e.View(0).Accounts {
		if a.ID == id {
			return a.AutoStartAt
		}
	}
	return time.Time{}
}

func TestStaggerStartsAWindowEvery100MinutesWithThreeAccounts(t *testing.T) {
	e, clk := newTestEngine(t, staggered)
	for _, id := range []string{accA, accB, accC} {
		polled(e, id, 0, 10, 5*time.Hour, 5*24*time.Hour)
	}
	v := e.View(0)
	if v.Starter.StaggerAccounts != 3 || v.Starter.StaggerMinutes != 100 {
		t.Fatalf("3 accounts should be staggered 100 minutes apart: %d %d", v.Starter.StaggerAccounts, v.Starter.StaggerMinutes)
	}
	if b, c := autoStartAtFor(e, accB), autoStartAtFor(e, accC); !b.Equal(t0.Add(100*time.Minute)) || !c.Equal(t0.Add(200*time.Minute)) {
		t.Fatalf("the panel should show the staggered times: bob %v, carol %v", b.Sub(t0), c.Sub(t0))
	}

	// Every window is idle, but only one starts now; the rest take their turn.
	startDue(t, e, clk, accA)
	if next := e.NextStartAt(); !next.Equal(t0.Add(100 * time.Minute)) {
		t.Fatalf("bob should be next, 100 minutes on: %v", next.Sub(t0))
	}
	clk.add(99 * time.Minute)
	if due := e.DueStarts(); len(due) != 0 {
		t.Fatalf("too early: %+v", due)
	}
	clk.add(time.Minute)
	startDue(t, e, clk, accB)
	clk.add(100 * time.Minute)
	startDue(t, e, clk, accC)

	// Alice's window resets at 5 h; her next one starts a minute later, which
	// keeps the cycle going at the same spacing.
	if next := e.NextStartAt(); !next.Equal(t0.Add(5*time.Hour + time.Minute)) {
		t.Fatalf("alice should restart a minute after her reset: %v", next.Sub(t0))
	}
	clk.add(101 * time.Minute)
	startDue(t, e, clk, accA)
	if next := e.NextStartAt(); !next.Equal(t0.Add(6*time.Hour + 41*time.Minute)) {
		t.Fatalf("bob should follow 100 minutes after alice: %v", next.Sub(t0))
	}
}

func TestStaggerSpreadsWindowsThatStartedTogether(t *testing.T) {
	e, clk := newTestEngine(t, staggered)
	for _, id := range []string{accA, accB, accC} {
		quota(e, id, 20, 30, 5*time.Hour, 5*24*time.Hour) // all started just now
	}
	if due := e.DueStarts(); len(due) != 0 {
		t.Fatalf("every window is running: %+v", due)
	}
	clk.add(5*time.Hour + time.Minute)
	startDue(t, e, clk, accA)
	if b, c := autoStartAtFor(e, accB), autoStartAtFor(e, accC); !b.Equal(clk.now().Add(100*time.Minute)) || !c.Equal(clk.now().Add(200*time.Minute)) {
		t.Fatalf("bob and carol should wait their turn: %v %v", b.Sub(clk.now()), c.Sub(clk.now()))
	}
	clk.add(100 * time.Minute)
	startDue(t, e, clk, accB)
	clk.add(100 * time.Minute)
	startDue(t, e, clk, accC)
}

func TestStaggerFollowsAWindowARequestStarted(t *testing.T) {
	e, clk := newTestEngine(t, staggered)
	for _, id := range []string{accA, accB, accC} {
		polled(e, id, 0, 10, 5*time.Hour, 5*24*time.Hour)
	}
	startDue(t, e, clk, accA)
	// Thirty minutes on, a request starts carol's window early.
	clk.add(30 * time.Minute)
	quota(e, accC, 1, 10, 5*time.Hour, 5*24*time.Hour)
	if next := e.NextStartAt(); !next.Equal(t0.Add(130 * time.Minute)) {
		t.Fatalf("bob's start should follow carol's, 100 minutes on: %v", next.Sub(t0))
	}
	clk.add(100 * time.Minute)
	startDue(t, e, clk, accB)
}

func TestStaggerGivesAFailedStartsSlotToTheNextAccount(t *testing.T) {
	e, clk := newTestEngine(t, staggered)
	for _, id := range []string{accA, accB, accC} {
		polled(e, id, 0, 10, 5*time.Hour, 5*24*time.Hour)
	}
	due := e.DueStarts()
	if len(due) != 1 || due[0].Account != accA {
		t.Fatalf("alice first: %+v", due)
	}
	// While alice's start is in flight, nobody else is due.
	if again := e.DueStarts(); len(again) != 0 {
		t.Fatalf("a start in flight holds the slot: %+v", again)
	}
	e.RecordStart(StartResult{At: clk.now(), Account: accA, Source: SourceAuto, Status: StartFailed, Detail: "HTTP 500"})
	startDue(t, e, clk, accB)
	if next := e.NextStartAt(); !next.Equal(t0.Add(100 * time.Minute)) {
		t.Fatalf("the next slot is 100 minutes after bob's start: %v", next.Sub(t0))
	}
}

func TestStaggerCountsOnlyAccountsAutoStartLooksAfter(t *testing.T) {
	e, clk := newTestEngine(t, func(s *Settings) {
		staggered(s)
		s.DisabledAccounts = []string{accC}
	})
	for _, id := range []string{accA, accB, accC} {
		polled(e, id, 0, 10, 5*time.Hour, 5*24*time.Hour)
	}
	if v := e.View(0); v.Starter.StaggerAccounts != 2 || v.Starter.StaggerMinutes != 150 {
		t.Fatalf("an Off account is left out, so 2 accounts 150 minutes apart: %d %d", v.Starter.StaggerAccounts, v.Starter.StaggerMinutes)
	}
	startDue(t, e, clk, accA)
	// Bob hits his 5-hour limit: he still counts, so the spacing holds.
	e.ObserveUsage(UsageObservation{AuthID: accB, Provider: "codex", Failed: true, Status: 429,
		Body: `{"error":{"type":"usage_limit_reached","resets_in_seconds":3600}}`})
	v := e.View(0)
	for _, a := range v.Accounts {
		if a.ID == accB && a.State != "exhausted" {
			t.Fatalf("bob should be blocked: %s %s", a.State, a.Detail)
		}
	}
	if v.Starter.StaggerAccounts != 2 {
		t.Fatalf("an account waiting out a 5-hour limit still counts: %d", v.Starter.StaggerAccounts)
	}
}

func TestStaggerNeedsAutoStartAndTwoAccounts(t *testing.T) {
	e, _ := newTestEngine(t, func(s *Settings) { s.StaggerWindows = true })
	for _, id := range []string{accA, accB, accC} {
		polled(e, id, 0, 10, 5*time.Hour, 5*24*time.Hour)
	}
	if due := e.DueStarts(); len(due) != 0 {
		t.Fatalf("stagger alone starts nothing: %+v", due)
	}
	if v := e.View(0); v.Starter.StaggerAccounts != 0 {
		t.Fatalf("no stagger without auto-start: %+v", v.Starter)
	}

	e, _ = newTestEngine(t, func(s *Settings) {
		staggered(s)
		s.DisabledAccounts = []string{accB, accC}
	})
	polled(e, accA, 0, 10, 5*time.Hour, 5*24*time.Hour)
	if due := e.DueStarts(); len(due) != 1 || due[0].Account != accA {
		t.Fatalf("one account starts as plain auto-start would: %+v", due)
	}
	if v := e.View(0); v.Starter.StaggerAccounts != 0 {
		t.Fatalf("nothing to stagger with one account: %+v", v.Starter)
	}
}

func TestTurningStaggerOnAndOffIsInHistory(t *testing.T) {
	e, _ := newTestEngine(t, nil)
	s, warn := ParseSettings([]byte("auto_start_windows: true\nstagger_windows: true\n"))
	if len(warn) != 0 || !s.StaggerWindows {
		t.Fatalf("stagger_windows should parse: %v %v", s.StaggerWindows, warn)
	}
	e.Configure(s, nil)
	if !historyHas(e, "window", "Stagger on") || historyHas(e, "settings", "Routing preferences updated") {
		t.Fatalf("history: %+v", e.View(0).History)
	}
	s.StaggerWindows = false
	e.Configure(s, nil)
	if !historyHas(e, "window", "Stagger off") {
		t.Fatalf("history: %+v", e.View(0).History)
	}
}
