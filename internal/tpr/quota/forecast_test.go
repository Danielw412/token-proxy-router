package quota

import (
	"math"
	"testing"
	"time"
)

var (
	now  = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	hour = time.Hour
	day  = 24 * time.Hour
	week = 7 * day
)

func params(to time.Duration, burn time.Duration, future string) Params {
	return Params{Now: now, From: now.Add(-week), To: now.Add(to), Burn: burn, Future: future}
}

func acct(id string, remaining float64) AccountInput {
	return AccountInput{ID: id, Weight: 1, Known: true, Counted: true, Remaining: remaining, Period: week}
}

// burning gives an account a series that used pct percent in the last hour.
func burning(a AccountInput, pct float64) AccountInput {
	a.Series = []SeriesPoint{
		{At: now.Add(-hour), Remaining: a.Remaining + pct},
		{At: now, Remaining: a.Remaining},
	}
	return a
}

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.02 {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func nearTime(t *testing.T, name string, got, want time.Time) {
	t.Helper()
	if d := got.Sub(want); d < -time.Second || d > time.Second {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func TestPoolTotalsAreWeighted(t *testing.T) {
	a := acct("a", 91)
	b := acct("b", 50)
	b.Weight = 2.5
	r := Forecast(params(week, 3*hour, FutureIdle), []AccountInput{a, b})
	near(t, "remaining", r.RemainingPercent, 91+125)
	near(t, "capacity", r.CapacityPercent, 100+250)
}

func TestUnknownAndUncountedAccountsAreOutOfTheTotals(t *testing.T) {
	unknown := acct("unknown", 100)
	unknown.Known = false
	off := acct("off", 30)
	off.Counted = false
	r := Forecast(params(week, 3*hour, FutureIdle), []AccountInput{acct("a", 40), unknown, off})
	near(t, "remaining", r.RemainingPercent, 40)
	near(t, "capacity", r.CapacityPercent, 100)
}

func TestNoDataGivesAnEmptyButWellFormedResult(t *testing.T) {
	for _, accounts := range [][]AccountInput{nil, {{ID: "a"}}} {
		r := Forecast(params(week, 3*hour, FutureSame), accounts)
		if r.RemainingPercent != 0 || r.CapacityPercent != 0 || r.BurnPerHour != 0 {
			t.Fatalf("result = %+v", r)
		}
		if r.PoolEmptyAt != nil || r.FirstEmpty != nil || r.NextReset != nil {
			t.Fatalf("no data must not claim anything: %+v", r)
		}
		if r.Recorded == nil || r.Projected == nil || r.Resets == nil || r.EmptyMarks == nil {
			t.Fatalf("slices must marshal as [] not null: %+v", r)
		}
		if r.Coverage.Samples != 0 || r.Coverage.FirstSampleAt != nil {
			t.Fatalf("coverage = %+v", r.Coverage)
		}
	}
}

func TestProjectedStartsAtNowAndEndsAtTo(t *testing.T) {
	r := Forecast(params(week, 3*hour, FutureIdle), []AccountInput{acct("a", 60)})
	if len(r.Projected) < 2 {
		t.Fatalf("projected = %+v", r.Projected)
	}
	if !r.Projected[0].At.Equal(now) || r.Projected[0].Percent != 60 {
		t.Fatalf("first point = %+v", r.Projected[0])
	}
	if last := r.Projected[len(r.Projected)-1]; !last.At.Equal(now.Add(week)) || last.Percent != 60 {
		t.Fatalf("last point = %+v", last)
	}
}

func TestNothingProjectedWhenTheRangeEndsInThePast(t *testing.T) {
	p := Params{Now: now, From: now.Add(-week), To: now.Add(-hour), Burn: 3 * hour}
	r := Forecast(p, []AccountInput{burning(acct("a", 50), 10)})
	if len(r.Projected) != 0 || r.PoolEmptyAt != nil {
		t.Fatalf("projected into a past range: %+v", r)
	}
}

func TestOneAccountRunsOutOnTheCurve(t *testing.T) {
	a := burning(acct("a", 20), 10) // 10% an hour, so empty in 2 hours
	r := Forecast(params(day, 3*hour, FutureSame), []AccountInput{a})
	near(t, "burn", r.BurnPerHour, 10)
	if r.PoolEmptyAt == nil || r.FirstEmpty == nil {
		t.Fatalf("result = %+v", r)
	}
	nearTime(t, "pool empty", *r.PoolEmptyAt, now.Add(2*hour))
	nearTime(t, "first empty", r.FirstEmpty.At, now.Add(2*hour))
	if r.FirstEmpty.Account != "a" {
		t.Fatalf("first empty = %+v", r.FirstEmpty)
	}
	if len(r.EmptyMarks) != 1 || r.EmptyMarks[0].Label != "Empty" {
		t.Fatalf("marks = %+v", r.EmptyMarks)
	}
	if last := r.Projected[len(r.Projected)-1]; last.Percent != 0 {
		t.Fatalf("last = %+v", last)
	}
}

func TestAccountsDrainOneAtATimeInOrder(t *testing.T) {
	first := burning(acct("first", 10), 10)
	second := acct("second", 30)
	r := Forecast(params(day, 3*hour, FutureSame), []AccountInput{first, second})
	nearTime(t, "first empty", r.FirstEmpty.At, now.Add(hour))
	if r.FirstEmpty.Account != "first" {
		t.Fatalf("first empty = %+v", r.FirstEmpty)
	}
	nearTime(t, "pool empty", *r.PoolEmptyAt, now.Add(4*hour))
}

func TestWeightScalesHowFastAnAccountDrains(t *testing.T) {
	// Burn is in pool-weighted percent: 10 points an hour against a 2x account
	// takes 5% of it an hour.
	a := burning(acct("a", 50), 5)
	a.Weight = 2
	r := Forecast(params(day, 3*hour, FutureSame), []AccountInput{a})
	near(t, "burn", r.BurnPerHour, 10)
	nearTime(t, "pool empty", *r.PoolEmptyAt, now.Add(10*hour))
}

func TestKnownResetJumpsTheCurveAndStopsAnEmpty(t *testing.T) {
	a := burning(acct("a", 50), 10)
	a.ResetAt = now.Add(2 * hour)
	r := Forecast(params(4*hour, 3*hour, FutureSame), []AccountInput{a})
	if r.PoolEmptyAt != nil {
		t.Fatalf("the reset arrives before the pool runs out: %+v", r.PoolEmptyAt)
	}
	if r.NextReset == nil {
		t.Fatalf("next reset missing: %+v", r)
	}
	nearTime(t, "next reset", r.NextReset.At, now.Add(2*hour))
	near(t, "reset delta", r.NextReset.DeltaPercent, 70)
	// Both sides of the jump are on the curve at one instant.
	var below, above bool
	for _, pt := range r.Projected {
		if pt.At.Sub(now.Add(2*hour)).Abs() < time.Second {
			if math.Abs(pt.Percent-30) < 0.1 {
				below = true
			}
			if math.Abs(pt.Percent-100) < 0.1 {
				above = true
			}
		}
	}
	if !below || !above {
		t.Fatalf("curve has no step at the reset: %+v", r.Projected)
	}
	if len(r.Resets) != 1 || !r.Resets[0].Projected {
		t.Fatalf("resets = %+v", r.Resets)
	}
}

func TestResetRecursEachPeriodWhileBurning(t *testing.T) {
	a := burning(acct("a", 90), 10)
	a.ResetAt = now.Add(day)
	r := Forecast(params(3*week, 3*hour, FutureSame), []AccountInput{a})
	if len(r.Resets) < 2 {
		t.Fatalf("resets = %+v", r.Resets)
	}
	if !r.Resets[0].Projected || !r.Resets[1].Projected {
		t.Fatalf("resets = %+v", r.Resets)
	}
}

func TestAnchoredResetsKeepTheirSchedule(t *testing.T) {
	a := burning(acct("a", 90), 2)
	a.ResetAt = now.Add(day)
	a.Anchored = true
	r := Forecast(params(3*week, 3*hour, FutureSame), []AccountInput{a})
	if len(r.Resets) < 3 {
		t.Fatalf("resets = %+v", r.Resets)
	}
	for i, reset := range r.Resets {
		want := now.Add(day).Add(time.Duration(i) * week)
		nearTime(t, "anchored reset", reset.At, want)
	}
}

func TestFirstUseWindowsRestartWithTheirFirstUse(t *testing.T) {
	// The second account's window starts when the first runs out, not before.
	first := burning(acct("first", 10), 10)
	second := acct("second", 100)
	second.Period = 5 * hour
	r := Forecast(params(day, 3*hour, FutureSame), []AccountInput{first, second})
	if len(r.Resets) == 0 {
		t.Fatalf("resets = %+v", r.Resets)
	}
	// First empties at +1h, second then starts a 5-hour window, resetting at +6h.
	nearTime(t, "second's reset", r.Resets[0].At, now.Add(6*hour))
	near(t, "delta", r.Resets[0].DeltaPercent, 50)
}

func TestAccountAlreadyAtZero(t *testing.T) {
	zero := acct("zero", 0)
	zero.ResetAt = now.Add(2 * hour)
	full := acct("full", 80)
	r := Forecast(params(day, 3*hour, FutureIdle), []AccountInput{zero, full})
	if r.FirstEmpty == nil || r.FirstEmpty.Account != "zero" || !r.FirstEmpty.At.Equal(now) {
		t.Fatalf("first empty = %+v", r.FirstEmpty)
	}
	if r.PoolEmptyAt != nil {
		t.Fatalf("the other account still has quota: %v", r.PoolEmptyAt)
	}
	nearTime(t, "next reset", r.NextReset.At, now.Add(2*hour))
	near(t, "delta", r.NextReset.DeltaPercent, 100)
}

func TestPoolAlreadyEmptyMarksNowAndRecoversAtTheReset(t *testing.T) {
	a := burning(acct("a", 0), 10)
	a.ResetAt = now.Add(hour)
	r := Forecast(params(3*hour, 3*hour, FutureSame), []AccountInput{a})
	if r.PoolEmptyAt == nil || !r.PoolEmptyAt.Equal(now) {
		t.Fatalf("pool empty = %v", r.PoolEmptyAt)
	}
	if len(r.EmptyMarks) != 1 || !r.EmptyMarks[0].At.Equal(now) {
		t.Fatalf("marks = %+v", r.EmptyMarks)
	}
	if r.Projected[0].Percent != 0 {
		t.Fatalf("first point = %+v", r.Projected[0])
	}
	var recovered bool
	for _, pt := range r.Projected {
		if pt.Percent > 50 {
			recovered = true
		}
	}
	if !recovered {
		t.Fatalf("no recovery in %+v", r.Projected)
	}
}

func TestEmptyMarksRepeatForEachDrySpellButPoolEmptyKeepsTheFirst(t *testing.T) {
	// 30 points an hour empties a full window in 3h20, before its 5-hour reset.
	a := burning(acct("a", 10), 15)
	a.ResetAt = now.Add(2 * hour)
	a.Period = 5 * hour
	r := Forecast(params(2*day, 3*hour, FutureDouble), []AccountInput{a})
	if len(r.EmptyMarks) < 2 {
		t.Fatalf("marks = %+v", r.EmptyMarks)
	}
	if !r.PoolEmptyAt.Equal(r.EmptyMarks[0].At) {
		t.Fatalf("pool empty %v vs first mark %v", r.PoolEmptyAt, r.EmptyMarks[0].At)
	}
}

func TestFuturesChangeTheRate(t *testing.T) {
	a := burning(acct("a", 50), 10)
	same := Forecast(params(day, 3*hour, FutureSame), []AccountInput{a})
	nearTime(t, "same", *same.PoolEmptyAt, now.Add(5*hour))

	double := Forecast(params(day, 3*hour, FutureDouble), []AccountInput{a})
	nearTime(t, "double", *double.PoolEmptyAt, now.Add(150*time.Minute))
	// The recorded burn is reported as measured, whichever future is drawn.
	near(t, "double burn", double.BurnPerHour, 10)

	idle := Forecast(params(day, 3*hour, FutureIdle), []AccountInput{a})
	if idle.PoolEmptyAt != nil || idle.FirstEmpty != nil {
		t.Fatalf("idle pool must not run out: %+v", idle)
	}
	for _, pt := range idle.Projected {
		if pt.Percent != 50 {
			t.Fatalf("idle curve moved: %+v", idle.Projected)
		}
	}
	near(t, "idle burn", idle.BurnPerHour, 10)

	unknown := Forecast(params(day, 3*hour, "whatever"), []AccountInput{a})
	nearTime(t, "unknown future falls back to same", *unknown.PoolEmptyAt, now.Add(5*hour))
}

func TestIdleFutureStillAppliesKnownResets(t *testing.T) {
	a := acct("a", 40)
	a.ResetAt = now.Add(3 * hour)
	r := Forecast(params(day, 3*hour, FutureIdle), []AccountInput{a})
	nearTime(t, "next reset", r.NextReset.At, now.Add(3*hour))
	near(t, "delta", r.NextReset.DeltaPercent, 60)
	if last := r.Projected[len(r.Projected)-1]; last.Percent != 100 {
		t.Fatalf("last = %+v", last)
	}
	if len(r.Resets) != 1 {
		t.Fatalf("a full window has nothing more to give back: %+v", r.Resets)
	}
}

func TestSimultaneousResetsAddUp(t *testing.T) {
	a, b := acct("a", 50), acct("b", 20)
	a.ResetAt, b.ResetAt = now.Add(hour), now.Add(hour)
	r := Forecast(params(day, 3*hour, FutureIdle), []AccountInput{a, b})
	near(t, "next reset delta", r.NextReset.DeltaPercent, 130)
	if len(r.Resets) != 2 {
		t.Fatalf("resets = %+v", r.Resets)
	}
}

func TestResetExactlyAtTheHorizonIsApplied(t *testing.T) {
	a := acct("a", 40)
	a.ResetAt = now.Add(hour)
	r := Forecast(params(hour, 3*hour, FutureIdle), []AccountInput{a})
	if last := r.Projected[len(r.Projected)-1]; last.Percent != 100 || !last.At.Equal(now.Add(hour)) {
		t.Fatalf("last = %+v", last)
	}
}

func TestResetPastTheHorizonIsNotReported(t *testing.T) {
	a := acct("a", 40)
	a.ResetAt = now.Add(2 * day)
	r := Forecast(params(day, 3*hour, FutureIdle), []AccountInput{a})
	if r.NextReset != nil || len(r.Resets) != 0 {
		t.Fatalf("result = %+v", r)
	}
}

func TestBurnRateUsesTheWindowAndIgnoresResets(t *testing.T) {
	series := []SeriesPoint{
		{At: now.Add(-10 * hour), Remaining: 100},
		{At: now.Add(-4 * hour), Remaining: 80}, // before a 3h window: baseline only
		{At: now.Add(-2 * hour), Remaining: 74},
		{At: now.Add(-hour), Remaining: 70},
		{At: now, Remaining: 68},
	}
	a := acct("a", 68)
	a.Series = series
	// 80 -> 68 over the 3-hour window.
	near(t, "burn", BurnPerHour(now, 3*hour, []AccountInput{a}), 4)

	// A reset between two samples: drop is measured from a full window, and the
	// rise itself is not counted as use.
	withReset := acct("r", 90)
	withReset.Series = []SeriesPoint{
		{At: now.Add(-2 * hour), Remaining: 10, ResetAt: now.Add(-hour)},
		{At: now, Remaining: 90},
	}
	near(t, "burn across reset", BurnPerHour(now, 3*hour, []AccountInput{withReset}), 5)

	// A rise with no reset known (provider reset early) counts for nothing.
	risen := acct("x", 90)
	risen.Series = []SeriesPoint{{At: now.Add(-hour), Remaining: 10}, {At: now, Remaining: 90}}
	near(t, "burn on rise", BurnPerHour(now, 3*hour, []AccountInput{risen}), 0)

	near(t, "no window", BurnPerHour(now, 0, []AccountInput{a}), 0)
	near(t, "no samples", BurnPerHour(now, 3*hour, []AccountInput{acct("n", 5)}), 0)
	one := acct("o", 5)
	one.Series = []SeriesPoint{{At: now, Remaining: 5}}
	near(t, "one sample", BurnPerHour(now, 3*hour, []AccountInput{one}), 0)
}

func TestBurnRateDividesAShortHistoryByItsRealSpanWithAFloor(t *testing.T) {
	a := acct("a", 90)
	a.Series = []SeriesPoint{{At: now.Add(-30 * time.Minute), Remaining: 95}, {At: now, Remaining: 90}}
	near(t, "half hour of data", BurnPerHour(now, 3*hour, []AccountInput{a}), 10)

	quick := acct("q", 90)
	quick.Series = []SeriesPoint{{At: now.Add(-time.Minute), Remaining: 91}, {At: now, Remaining: 90}}
	near(t, "floor", BurnPerHour(now, 3*hour, []AccountInput{quick}), 4)
}

func TestBurnRateSumsAccountsWithWeights(t *testing.T) {
	a := burning(acct("a", 50), 10)
	b := burning(acct("b", 50), 4)
	b.Weight = 2.5
	near(t, "burn", BurnPerHour(now, 3*hour, []AccountInput{a, b}), 20)
}

func TestSamplesAfterNowDoNotCount(t *testing.T) {
	a := acct("a", 50)
	a.Series = []SeriesPoint{{At: now.Add(-hour), Remaining: 60}, {At: now, Remaining: 50}, {At: now.Add(hour), Remaining: 0}}
	near(t, "burn", BurnPerHour(now, 3*hour, []AccountInput{a}), 10)
}

func TestRecordedHasRealSampleTimesOnly(t *testing.T) {
	a, b := acct("a", 50), acct("b", 80)
	a.Series = []SeriesPoint{{At: now.Add(-3 * hour), Remaining: 70}, {At: now.Add(-hour), Remaining: 50}}
	b.Series = []SeriesPoint{{At: now.Add(-2 * hour), Remaining: 80}}
	r := Forecast(params(day, 3*hour, FutureIdle), []AccountInput{a, b})
	want := []Point{
		{At: now.Add(-3 * hour), Percent: 150}, // b has no reading yet: its first one counts
		{At: now.Add(-2 * hour), Percent: 150},
		{At: now.Add(-hour), Percent: 130},
	}
	if len(r.Recorded) != len(want) {
		t.Fatalf("recorded = %+v", r.Recorded)
	}
	for i := range want {
		if !r.Recorded[i].At.Equal(want[i].At) || r.Recorded[i].Percent != want[i].Percent {
			t.Fatalf("recorded[%d] = %+v, want %+v", i, r.Recorded[i], want[i])
		}
	}
	if r.Coverage.Samples != 3 || !r.Coverage.FirstSampleAt.Equal(now.Add(-3*hour)) {
		t.Fatalf("coverage = %+v", r.Coverage)
	}
}

func TestRecordedStaysWithinTheRequestedRangeAndBeforeNow(t *testing.T) {
	a := acct("a", 50)
	a.Series = []SeriesPoint{
		{At: now.Add(-3 * week), Remaining: 90},
		{At: now.Add(-hour), Remaining: 60},
		{At: now.Add(hour), Remaining: 40},
	}
	r := Forecast(params(day, 3*hour, FutureIdle), []AccountInput{a})
	if len(r.Recorded) != 1 || !r.Recorded[0].At.Equal(now.Add(-hour)) {
		t.Fatalf("recorded = %+v", r.Recorded)
	}
	if r.Coverage.Samples != 1 || !r.Coverage.FirstSampleAt.Equal(now.Add(-3*week)) {
		t.Fatalf("coverage = %+v", r.Coverage)
	}
}

func TestRecordedTreatsAPassedResetAsFullBetweenReadings(t *testing.T) {
	a, b := acct("a", 100), acct("b", 50)
	a.Series = []SeriesPoint{{At: now.Add(-3 * hour), Remaining: 10, ResetAt: now.Add(-2 * hour)}}
	b.Series = []SeriesPoint{{At: now.Add(-3 * hour), Remaining: 50}, {At: now.Add(-hour), Remaining: 50}}
	r := Forecast(params(day, 3*hour, FutureIdle), []AccountInput{a, b})
	if r.Recorded[0].Percent != 60 || r.Recorded[1].Percent != 150 {
		t.Fatalf("recorded = %+v", r.Recorded)
	}
}

func TestRecordedResetsAreFoundInHistoryAndMarkedNotProjected(t *testing.T) {
	a := acct("a", 80)
	a.Series = []SeriesPoint{
		{At: now.Add(-3 * hour), Remaining: 20, ResetAt: now.Add(-2 * hour)},
		{At: now.Add(-hour), Remaining: 95, ResetAt: now.Add(week)},
	}
	a.ResetAt = now.Add(hour)
	r := Forecast(params(day, 3*hour, FutureIdle), []AccountInput{a})
	if len(r.Resets) < 2 {
		t.Fatalf("resets = %+v", r.Resets)
	}
	if r.Resets[0].Projected || !r.Resets[0].At.Equal(now.Add(-2*hour)) {
		t.Fatalf("recorded reset = %+v", r.Resets[0])
	}
	near(t, "recorded delta", r.Resets[0].DeltaPercent, 75)
	if !r.Resets[1].Projected {
		t.Fatalf("projected reset = %+v", r.Resets[1])
	}
}

func TestRecordedIsThinnedKeepingRealTimesAndTheNewest(t *testing.T) {
	a := acct("a", 50)
	const n = 5000
	for i := 0; i < n; i++ {
		a.Series = append(a.Series, SeriesPoint{At: now.Add(-time.Duration(n-i) * time.Minute), Remaining: 50})
	}
	r := Forecast(Params{Now: now, From: now.Add(-10 * day), To: now.Add(day), Burn: 3 * hour}, []AccountInput{a})
	if len(r.Recorded) != maxRecordedPoints {
		t.Fatalf("points = %d", len(r.Recorded))
	}
	real := map[time.Time]bool{}
	for _, s := range a.Series {
		real[s.At] = true
	}
	for _, pt := range r.Recorded {
		if !real[pt.At] {
			t.Fatalf("invented time %v", pt.At)
		}
	}
	if !r.Recorded[len(r.Recorded)-1].At.Equal(a.Series[n-1].At) {
		t.Fatalf("newest sample dropped: %v", r.Recorded[len(r.Recorded)-1])
	}
	if r.Coverage.Samples != n {
		t.Fatalf("coverage counts the real samples: %d", r.Coverage.Samples)
	}
}

// DST: a pool in a zone that changes its clocks during the horizon. Everything is
// an instant, so a weekly reset stays exactly 168 hours from the last one.
func TestDSTBoundariesKeepResetsExactlyOnePeriodApart(t *testing.T) {
	loc := time.FixedZone("before", -4*3600)
	nyc, err := time.LoadLocation("America/New_York")
	if err == nil {
		loc = nyc
	}
	// DST ends in New York on 2026-11-01; DST began on 2026-03-08.
	for _, start := range []time.Time{
		time.Date(2026, 10, 30, 1, 30, 0, 0, loc),
		time.Date(2026, 3, 6, 2, 30, 0, 0, loc),
	} {
		a := acct("a", 90)
		a.Series = []SeriesPoint{{At: start.Add(-hour), Remaining: 91}, {At: start, Remaining: 90}}
		a.ResetAt = start.Add(day)
		a.Anchored = true
		p := Params{Now: start, From: start.Add(-week), To: start.Add(4 * week), Burn: 3 * hour}
		r := Forecast(p, []AccountInput{a})
		if len(r.Resets) < 3 {
			t.Fatalf("resets = %+v", r.Resets)
		}
		for i := 1; i < len(r.Resets); i++ {
			if gap := r.Resets[i].At.Sub(r.Resets[i-1].At); gap != week {
				t.Fatalf("reset gap across DST = %v, want %v", gap, week)
			}
		}
		// The curve only moves forward in time.
		for i := 1; i < len(r.Projected); i++ {
			if r.Projected[i].At.Before(r.Projected[i-1].At) {
				t.Fatalf("curve went backward at %d: %+v", i, r.Projected[i])
			}
		}
	}
}

func TestDSTFallBackHourDoesNotDoubleCountBurn(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	// 01:30 EDT and 01:30 EST are an hour apart in real time on this date.
	before := time.Date(2026, 11, 1, 1, 30, 0, 0, loc)
	after := before.Add(hour)
	if before.Format("15:04") != after.Format("15:04") {
		t.Fatalf("expected the repeated hour: %v %v", before, after)
	}
	a := acct("a", 50)
	a.Series = []SeriesPoint{{At: before, Remaining: 60}, {At: after, Remaining: 50}}
	near(t, "burn", BurnPerHour(after, 3*hour, []AccountInput{a}), 10)
}

func TestBadValuesAreClamped(t *testing.T) {
	a := acct("a", 250)
	b := acct("b", -5)
	c := acct("c", math.NaN())
	d := acct("d", 50)
	d.Weight = -3
	r := Forecast(params(hour, 3*hour, FutureIdle), []AccountInput{a, b, c, d})
	near(t, "remaining", r.RemainingPercent, 150)
	near(t, "capacity", r.CapacityPercent, 400)
}

func TestSimulationTerminatesOnPathologicalInputs(t *testing.T) {
	// A tiny period with a huge burn over a long horizon must still finish, and
	// never report beyond the horizon.
	a := burning(acct("a", 5), 90)
	a.Period = time.Second
	a.ResetAt = now.Add(time.Second)
	a.Anchored = true
	p := Params{Now: now, From: now.Add(-week), To: now.Add(10 * 365 * day), Burn: hour, Future: FutureDouble}
	r := Forecast(p, []AccountInput{a})
	for _, pt := range r.Projected {
		if pt.At.After(p.To) {
			t.Fatalf("point past the horizon: %+v", pt)
		}
	}
	if last := r.Projected[len(r.Projected)-1]; !last.At.Equal(p.To) {
		t.Fatalf("curve must still end at to: %+v", last)
	}
}

func TestCurveIsNeverNegativeNorAboveCapacity(t *testing.T) {
	a := burning(acct("a", 33), 7)
	a.ResetAt = now.Add(5 * hour)
	a.Period = 5 * hour
	b := burning(acct("b", 3), 7)
	b.Weight = 4
	r := Forecast(params(week, 3*hour, FutureDouble), []AccountInput{a, b})
	for _, pt := range r.Projected {
		if pt.Percent < 0 || pt.Percent > r.CapacityPercent {
			t.Fatalf("point out of range: %+v of %v", pt, r.CapacityPercent)
		}
	}
}
