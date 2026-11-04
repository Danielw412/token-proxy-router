// Package quota records quota history for every Codex and Claude account and
// projects it forward. This file is the pure part: no I/O and no clock, so all of
// the forecast math is unit tested with fixed inputs.
package quota

import (
	"math"
	"sort"
	"strings"
	"time"
)

// Futures for the projected curve.
const (
	FutureSame   = "same"   // keep burning at the recorded rate
	FutureIdle   = "idle"   // stop using the pool
	FutureDouble = "double" // burn twice as fast
)

const (
	// minBurnSpan keeps the burn rate from swinging on a few minutes of data: a
	// short history is divided by at least this long.
	minBurnSpan = 15 * time.Minute
	// maxRecordedPoints bounds the history curve; the panel cannot draw more.
	maxRecordedPoints = 1500
	// maxSimulationSteps bounds the projection whatever the inputs.
	maxSimulationSteps = 20000
	// percentEpsilon is the smallest amount worth reporting on the curve.
	percentEpsilon = 0.01
)

// SeriesPoint is one recorded reading of one account's window.
type SeriesPoint struct {
	At        time.Time
	Remaining float64
	ResetAt   time.Time
}

// AccountInput is one account as the forecast sees it.
type AccountInput struct {
	ID     string
	Weight float64
	// Known is false for an account with no quota data, which counts for nothing.
	Known bool
	// Counted is false for accounts outside the pool's totals: Off or disabled.
	Counted bool
	// Remaining is percent left now, 0 to 100.
	Remaining float64
	// ResetAt is the window's next known reset, or zero.
	ResetAt time.Time
	// Period is the window's length; it makes resets recur past the known one.
	Period time.Duration
	// Anchored windows recur on a fixed schedule whether used or not. Others start
	// counting again at the account's first use after a reset.
	Anchored bool
	// Series is the account's recorded history, oldest first.
	Series []SeriesPoint
}

// Params selects what to compute.
type Params struct {
	Now, From, To time.Time
	Burn          time.Duration
	Future        string
}

// Point is one spot on a curve, in pool-weighted percent.
type Point struct {
	At      time.Time `json:"at"`
	Percent float64   `json:"percent"`
}

// Reset is one reset the pool saw or will see.
type Reset struct {
	At           time.Time `json:"at"`
	DeltaPercent float64   `json:"delta_percent"`
	Projected    bool      `json:"projected"`
}

// EmptyMark labels a moment the projected pool runs dry.
type EmptyMark struct {
	At    time.Time `json:"at"`
	Label string    `json:"label"`
}

// FirstEmpty is the first account to run out.
type FirstEmpty struct {
	Account string    `json:"account"`
	At      time.Time `json:"at"`
}

// NextReset is the next reset that gives quota back.
type NextReset struct {
	At           time.Time `json:"at"`
	DeltaPercent float64   `json:"delta_percent"`
}

// Coverage says how much real history stands behind the numbers.
type Coverage struct {
	FirstSampleAt *time.Time `json:"first_sample_at"`
	Samples       int        `json:"samples"`
}

// Result is one pool's forecast.
type Result struct {
	RemainingPercent float64     `json:"remaining_percent"`
	CapacityPercent  float64     `json:"capacity_percent"`
	PoolEmptyAt      *time.Time  `json:"pool_empty_at"`
	FirstEmpty       *FirstEmpty `json:"first_empty"`
	NextReset        *NextReset  `json:"next_reset"`
	BurnPerHour      float64     `json:"burn_per_hour"`
	Recorded         []Point     `json:"recorded"`
	Projected        []Point     `json:"projected"`
	Resets           []Reset     `json:"resets"`
	EmptyMarks       []EmptyMark `json:"empty_marks"`
	Coverage         Coverage    `json:"coverage"`
}

// Forecast computes one pool's numbers. Accounts drain in the order given, which
// is how the router concentrates traffic: one current account at a time.
func Forecast(p Params, accounts []AccountInput) Result {
	r := Result{Recorded: []Point{}, Projected: []Point{}, Resets: []Reset{}, EmptyMarks: []EmptyMark{}}
	counted := countedAccounts(accounts)
	r.RemainingPercent, r.CapacityPercent = PoolTotals(counted)

	r.BurnPerHour = round2(BurnPerHour(p.Now, p.Burn, counted))
	r.Recorded, r.Coverage = recordedCurve(p, counted)
	r.Resets = recordedResets(p, counted)

	sim := simulate(p, counted, futureRate(r.BurnPerHour, p.Future))
	r.Projected = sim.points
	r.Resets = append(r.Resets, sim.resets...)
	r.EmptyMarks = sim.marks
	r.PoolEmptyAt = sim.poolEmptyAt
	r.FirstEmpty = sim.firstEmpty
	r.NextReset = sim.nextReset
	return r
}

func futureRate(burn float64, future string) float64 {
	switch strings.ToLower(strings.TrimSpace(future)) {
	case FutureIdle:
		return 0
	case FutureDouble:
		return burn * 2
	default:
		return burn
	}
}

// BurnPerHour is the pool-weighted percent used per hour over the burn window. It
// adds up the drops between each account's consecutive samples, so a reset (a rise
// between samples) does not hide usage or count as negative use.
func BurnPerHour(now time.Time, window time.Duration, accounts []AccountInput) float64 {
	if window <= 0 {
		return 0
	}
	start := now.Add(-window)
	var used float64
	var earliest time.Time
	for _, a := range accounts {
		// The last sample before the window is the baseline for the first drop.
		var prev *SeriesPoint
		for i := range a.Series {
			s := &a.Series[i]
			if s.At.After(now) {
				break
			}
			if s.At.Before(start) {
				prev = s
				continue
			}
			if prev != nil {
				used += dropInWindow(*prev, *s, start) * weightOf(a)
				first := prev.At
				if first.Before(start) {
					first = start
				}
				if earliest.IsZero() || first.Before(earliest) {
					earliest = first
				}
			}
			prev = s
		}
	}
	if used <= 0 || earliest.IsZero() {
		return 0
	}
	span := now.Sub(earliest)
	if span < minBurnSpan {
		span = minBurnSpan
	}
	return used / span.Hours()
}

// dropInWindow is the part of a drop that happened after start, assuming steady
// use between the two readings. Without this, a long gap in polling would squeeze
// hours of use into the burn window.
func dropInWindow(prev, next SeriesPoint, start time.Time) float64 {
	drop := dropBetween(prev, next)
	from := prev.At
	// After a reset, the later reading's use all happened after that reset.
	if !prev.ResetAt.IsZero() && prev.ResetAt.After(from) && !next.At.Before(prev.ResetAt) {
		from = prev.ResetAt
	}
	if !from.Before(start) {
		return drop
	}
	total := next.At.Sub(from)
	if total <= 0 {
		return drop
	}
	return drop * next.At.Sub(start).Seconds() / total.Seconds()
}

func weightOf(a AccountInput) float64 {
	if a.Weight <= 0 {
		return 1
	}
	return a.Weight
}

// dropBetween is the quota used between two readings. If the window reset between
// them, the later reading's use all happened after the reset.
func dropBetween(prev, next SeriesPoint) float64 {
	start := prev.Remaining
	if !prev.ResetAt.IsZero() && !next.At.Before(prev.ResetAt) {
		start = 100
	}
	if drop := start - next.Remaining; drop > 0 {
		return drop
	}
	return 0
}

// valueAt is an account's remaining percent at a moment, from its last reading at
// or before it. If that reading's reset has since passed, the window is full. An
// account with no reading yet counts as its first one, so a newly added account
// does not make the pool appear to jump.
func valueAt(a AccountInput, cursor int, at time.Time) float64 {
	if len(a.Series) == 0 {
		return a.Remaining
	}
	if cursor < 0 {
		return clampPercent(a.Series[0].Remaining)
	}
	s := a.Series[cursor]
	if !s.ResetAt.IsZero() && !at.Before(s.ResetAt) {
		return 100
	}
	return clampPercent(s.Remaining)
}

// recordedCurve has one point per real sample time and never invents a time: each
// point is the pool's value at a moment one of its accounts really reported.
func recordedCurve(p Params, accounts []AccountInput) ([]Point, Coverage) {
	cov := Coverage{}
	end := p.To
	if p.Now.Before(end) {
		end = p.Now
	}
	var times []time.Time
	for _, a := range accounts {
		for _, s := range a.Series {
			if cov.FirstSampleAt == nil || s.At.Before(*cov.FirstSampleAt) {
				first := s.At
				cov.FirstSampleAt = &first
			}
			if s.At.Before(p.From) || s.At.After(end) {
				continue
			}
			times = append(times, s.At)
			cov.Samples++
		}
	}
	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
	times = thinTimes(dedupeTimes(times), maxRecordedPoints)

	cursors := make([]int, len(accounts))
	for i := range cursors {
		cursors[i] = -1
	}
	out := make([]Point, 0, len(times))
	for _, at := range times {
		var total float64
		for i, a := range accounts {
			for cursors[i]+1 < len(a.Series) && !a.Series[cursors[i]+1].At.After(at) {
				cursors[i]++
			}
			total += valueAt(a, cursors[i], at) * a.Weight
		}
		out = append(out, Point{At: at, Percent: round2(total)})
	}
	return out, cov
}

func dedupeTimes(times []time.Time) []time.Time {
	out := times[:0]
	for i, t := range times {
		if i == 0 || !t.Equal(times[i-1]) {
			out = append(out, t)
		}
	}
	return out
}

// thinTimes keeps real sample times only, evenly, always keeping the newest.
func thinTimes(times []time.Time, limit int) []time.Time {
	if len(times) <= limit || limit < 2 {
		return times
	}
	out := make([]time.Time, 0, limit)
	step := float64(len(times)-1) / float64(limit-1)
	for i := 0; i < limit; i++ {
		out = append(out, times[int(math.Round(float64(i)*step))])
	}
	return out
}

// recordedResets finds resets in history: a reading that reset since the one before
// it, sized by how much it gave back. Readings just after a reset already include
// any use since then, so the size is a lower bound.
func recordedResets(p Params, accounts []AccountInput) []Reset {
	out := []Reset{}
	for _, a := range accounts {
		for i := 1; i < len(a.Series); i++ {
			prev, next := a.Series[i-1], a.Series[i]
			if prev.ResetAt.IsZero() || next.At.Before(prev.ResetAt) {
				continue
			}
			at := prev.ResetAt
			if at.Before(p.From) || at.After(p.Now) {
				continue
			}
			if delta := (next.Remaining - prev.Remaining) * a.Weight; delta > percentEpsilon {
				out = append(out, Reset{At: at, DeltaPercent: round2(delta)})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

type simAccount struct {
	AccountInput
	rem     float64 // percent left, 0 to 100
	reset   time.Time
	running bool // a window is counting toward reset
}

type simResult struct {
	points      []Point
	resets      []Reset
	marks       []EmptyMark
	poolEmptyAt *time.Time
	firstEmpty  *FirstEmpty
	nextReset   *NextReset
}

// simulate walks the future event by event: an account runs out, or a reset gives
// quota back. Between events the curve is a straight line. The curve holds both
// sides of a reset at the same instant, so it draws as a step.
func simulate(p Params, accounts []AccountInput, rate float64) simResult {
	res := simResult{points: []Point{}, resets: []Reset{}, marks: []EmptyMark{}}
	if !p.To.After(p.Now) {
		return res
	}
	sims := make([]*simAccount, 0, len(accounts))
	for _, a := range accounts {
		s := &simAccount{AccountInput: a, rem: a.Remaining}
		if a.ResetAt.After(p.Now) {
			s.reset, s.running = a.ResetAt, true
		}
		sims = append(sims, s)
	}
	total := func() float64 {
		var sum float64
		for _, s := range sims {
			sum += s.rem * s.Weight
		}
		return sum
	}
	add := func(at time.Time) { res.points = append(res.points, Point{At: at, Percent: round2(total())}) }

	now := p.Now
	add(now)
	for _, s := range sims {
		if s.rem <= 0 {
			res.firstEmpty = &FirstEmpty{Account: s.ID, At: now}
			break
		}
	}
	dry := len(sims) > 0 && total() <= 0
	if dry {
		at := now
		res.poolEmptyAt = &at
		res.marks = append(res.marks, EmptyMark{At: at, Label: "Empty"})
	}

	for step := 0; step < maxSimulationSteps; step++ {
		current := currentAccount(sims)
		burning := rate > 0 && current != nil
		if burning && !current.running && current.Period > 0 {
			// The account's next window starts counting with its first use.
			current.reset, current.running = now.Add(current.Period), true
		}
		next, resetting := nextResetEvent(sims, now, p.To)
		var emptyAt time.Time
		if burning {
			hours := current.rem * current.Weight / rate
			emptyAt = now.Add(time.Duration(hours * float64(time.Hour)))
			if !emptyAt.After(now) {
				emptyAt = now.Add(time.Nanosecond)
			}
			if emptyAt.Before(next) {
				next, resetting = emptyAt, nil
			}
		}
		if burning {
			current.rem -= rate * next.Sub(now).Hours() / current.Weight
			if current.rem < 0 || next.Equal(emptyAt) {
				current.rem = 0
			}
		}
		now = next

		switch {
		case resetting != nil:
			// A reset of a full window changes nothing, so it adds no step.
			step := false
			for _, s := range resetting {
				step = step || (100-s.rem)*s.Weight > percentEpsilon
			}
			if step {
				add(now) // the step's bottom
			}
			for _, s := range resetting {
				delta := (100 - s.rem) * s.Weight
				s.rem = 100
				if s.Anchored && s.Period > 0 {
					s.reset = s.reset.Add(s.Period)
				} else {
					s.reset, s.running = time.Time{}, false
				}
				if delta <= percentEpsilon {
					continue
				}
				at := now
				res.resets = append(res.resets, Reset{At: at, DeltaPercent: round2(delta), Projected: true})
				if res.nextReset == nil {
					res.nextReset = &NextReset{At: at, DeltaPercent: round2(delta)}
				} else if res.nextReset.At.Equal(at) {
					res.nextReset.DeltaPercent = round2(res.nextReset.DeltaPercent + delta)
				}
			}
			if step {
				add(now) // the step's top
				dry = false
			}
		case burning && current.rem <= 0:
			add(now)
			if res.firstEmpty == nil {
				res.firstEmpty = &FirstEmpty{Account: current.ID, At: now}
			}
			if total() <= percentEpsilon && !dry {
				dry = true
				at := now
				if res.poolEmptyAt == nil {
					res.poolEmptyAt = &at
				}
				res.marks = append(res.marks, EmptyMark{At: at, Label: "Empty"})
			}
		}
		if !now.Before(p.To) {
			break
		}
	}
	if last := res.points[len(res.points)-1]; !last.At.Equal(p.To) {
		add(p.To)
	}
	return res
}

// currentAccount is the one being drained: the first with quota left.
func currentAccount(sims []*simAccount) *simAccount {
	for _, s := range sims {
		if s.rem > 0 {
			return s
		}
	}
	return nil
}

// nextResetEvent returns the next moment any window resets, capped at the end of
// the horizon, and the accounts resetting then. A reset exactly at the horizon is
// still applied.
func nextResetEvent(sims []*simAccount, now, to time.Time) (time.Time, []*simAccount) {
	var soonest time.Time
	for _, s := range sims {
		if s.reset.IsZero() || !s.reset.After(now) || s.reset.After(to) {
			continue
		}
		if soonest.IsZero() || s.reset.Before(soonest) {
			soonest = s.reset
		}
	}
	if soonest.IsZero() {
		return to, nil
	}
	var due []*simAccount
	for _, s := range sims {
		if s.reset.Equal(soonest) {
			due = append(due, s)
		}
	}
	return soonest, due
}

// countedAccounts keeps the accounts that count toward a pool's totals, with
// their weight defaulted and their remaining quota clamped.
func countedAccounts(accounts []AccountInput) []AccountInput {
	counted := make([]AccountInput, 0, len(accounts))
	for _, a := range accounts {
		if !a.Counted || !a.Known {
			continue
		}
		if a.Weight <= 0 {
			a.Weight = 1
		}
		a.Remaining = clampPercent(a.Remaining)
		counted = append(counted, a)
	}
	return counted
}

// PoolTotals is a pool's weighted remaining quota and capacity: each counted
// account adds remaining*weight and 100*weight.
func PoolTotals(accounts []AccountInput) (remaining, capacity float64) {
	for _, a := range countedAccounts(accounts) {
		remaining += a.Remaining * a.Weight
		capacity += 100 * a.Weight
	}
	return round2(remaining), round2(capacity)
}

func clampPercent(v float64) float64 {
	switch {
	case math.IsNaN(v) || v < 0:
		return 0
	case v > 100:
		return 100
	}
	return v
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
