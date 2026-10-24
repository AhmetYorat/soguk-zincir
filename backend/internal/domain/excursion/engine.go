// Package excursion evaluates a temperature series against a product's cold
// chain rules: instantaneous limits, cumulative time out of range, a sliding
// window rule, sensor gaps and the mean kinetic temperature (MKT).
//
// The engine is pure: it takes readings and returns a Result. Persisting
// alarms and reports is the caller's job.
package excursion

import (
	"math"
	"sort"
	"time"
)

// GasConstant R in J/(mol·K).
const GasConstant = 8.314462618

// GapPolicy tells the engine what to assume when readings are missing.
type GapPolicy string

const (
	// Uncertain counts a gap as time out of range (conservative). The gap is
	// reported separately so a quality manager can see it was not measured.
	Uncertain GapPolicy = "uncertain"
	// Interpolate fills the gap linearly between the readings on both sides.
	Interpolate GapPolicy = "interpolate"
)

// Rules are the per-product limits. Temperatures are centi-degrees Celsius.
type Rules struct {
	MinCC            int
	MaxCC            int
	MaxCumulative    time.Duration // total allowed time outside [Min, Max]
	Window           time.Duration // sliding window length
	WindowMax        time.Duration // allowed time outside limits inside any window
	MaxGap           time.Duration // readings further apart than this form a gap
	Policy           GapPolicy
	ActivationEnergy float64 // J/mol, used for MKT
}

// Reading is one measurement.
type Reading struct {
	TS     time.Time
	TempCC int
}

// State classifies a stretch of time.
type State string

const (
	InRange State = "in_range"
	High    State = "high"
	Low     State = "low"
	Unknown State = "unknown" // gap under the Uncertain policy
)

// Segment is a stretch of time with a single classification. For measured
// segments TempCC is the held reading; for interpolated ones it is the mean
// of the two ends; for Unknown it is the worse (higher) neighbouring reading.
type Segment struct {
	Start        time.Time
	End          time.Time
	State        State
	TempCC       float64
	Interpolated bool
}

// Duration of the segment.
func (s Segment) Duration() time.Duration { return s.End.Sub(s.Start) }

// Gap is a stretch without readings longer than Rules.MaxGap.
type Gap struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Rule names match the alarm_rule enum in the database.
const (
	RuleLimitHigh  = "limit_high"
	RuleLimitLow   = "limit_low"
	RuleCumulative = "cumulative"
	RuleWindow     = "window"
	RuleGap        = "gap"
)

// Breach is a violated rule and when it was first violated.
type Breach struct {
	Rule string    `json:"rule"`
	At   time.Time `json:"at"`
}

// Result of an evaluation.
type Result struct {
	Start, End        time.Time
	Segments          []Segment
	Gaps              []Gap
	HighDuration      time.Duration
	LowDuration       time.Duration
	UncertainDuration time.Duration
	// ExcursionDuration = High + Low (+ Uncertain under the Uncertain policy).
	ExcursionDuration time.Duration
	// MaxWindowExcursion is the largest excursion time found in any window of
	// Rules.Window; MaxWindowStart is where that window starts.
	MaxWindowExcursion time.Duration
	MaxWindowStart     time.Time
	PeakCC             int // highest reading
	MinCC              int // lowest reading
	MKTCelsius         float64
	HasMKT             bool
	Breaches           []Breach
}

// Breached reports whether the given rule was violated.
func (r Result) Breached(rule string) bool {
	for _, b := range r.Breaches {
		if b.Rule == rule {
			return true
		}
	}
	return false
}

// Evaluate classifies the series up to `until`. Readings need not be sorted;
// duplicate timestamps keep the first value. The last reading is held until
// `until` if that is within MaxGap, otherwise the remainder is a gap.
func Evaluate(readings []Reading, rules Rules, until time.Time) Result {
	rs := normalize(readings)
	res := Result{}
	if len(rs) == 0 {
		return res
	}
	res.Start = rs[0].TS
	res.End = until
	if until.Before(rs[len(rs)-1].TS) {
		res.End = rs[len(rs)-1].TS
	}
	res.PeakCC, res.MinCC = rs[0].TempCC, rs[0].TempCC

	for i, r := range rs {
		if r.TempCC > res.PeakCC {
			res.PeakCC = r.TempCC
		}
		if r.TempCC < res.MinCC {
			res.MinCC = r.TempCC
		}
		if r.TempCC > rules.MaxCC {
			res.addBreachOnce(RuleLimitHigh, r.TS)
		}
		if r.TempCC < rules.MinCC {
			res.addBreachOnce(RuleLimitLow, r.TS)
		}

		var end time.Time
		hasNext := i+1 < len(rs)
		if hasNext {
			end = rs[i+1].TS
		} else {
			end = res.End
		}
		span := end.Sub(r.TS)
		if span <= 0 {
			continue
		}
		if span <= rules.MaxGap {
			res.Segments = append(res.Segments, held(r, end, rules))
			continue
		}

		// Gap. With no reading on the far side (trailing gap) nothing can be
		// interpolated, so it is always treated as unknown.
		res.Gaps = append(res.Gaps, Gap{Start: r.TS, End: end})
		res.addBreachOnce(RuleGap, r.TS.Add(rules.MaxGap))
		if rules.Policy == Interpolate && hasNext {
			res.Segments = append(res.Segments, interpolate(r, rs[i+1], rules)...)
		} else {
			worse := float64(r.TempCC)
			if hasNext && rs[i+1].TempCC > r.TempCC {
				worse = float64(rs[i+1].TempCC)
			}
			res.Segments = append(res.Segments, Segment{Start: r.TS, End: end, State: Unknown, TempCC: worse})
		}
	}

	res.Segments = merge(res.Segments)
	res.tally(rules)
	res.window(rules)
	res.mkt(rules)
	sort.SliceStable(res.Breaches, func(i, j int) bool { return res.Breaches[i].At.Before(res.Breaches[j].At) })
	return res
}

func normalize(in []Reading) []Reading {
	rs := make([]Reading, len(in))
	copy(rs, in)
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].TS.Before(rs[j].TS) })
	out := rs[:0]
	for i, r := range rs {
		if i > 0 && r.TS.Equal(out[len(out)-1].TS) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func classify(tempCC float64, rules Rules) State {
	switch {
	case tempCC > float64(rules.MaxCC):
		return High
	case tempCC < float64(rules.MinCC):
		return Low
	default:
		return InRange
	}
}

func held(r Reading, end time.Time, rules Rules) Segment {
	t := float64(r.TempCC)
	return Segment{Start: r.TS, End: end, State: classify(t, rules), TempCC: t}
}

// interpolate splits the straight line between a and b at the points where it
// crosses the limits, so the time out of range is exact for the linear model.
func interpolate(a, b Reading, rules Rules) []Segment {
	t0, t1 := float64(a.TempCC), float64(b.TempCC)
	total := b.TS.Sub(a.TS)
	cuts := []float64{0, 1}
	for _, lim := range []float64{float64(rules.MinCC), float64(rules.MaxCC)} {
		if (t0-lim)*(t1-lim) < 0 {
			cuts = append(cuts, (lim-t0)/(t1-t0))
		}
	}
	sort.Float64s(cuts)
	var segs []Segment
	for i := 0; i+1 < len(cuts); i++ {
		f0, f1 := cuts[i], cuts[i+1]
		if f1 <= f0 {
			continue
		}
		mid := t0 + (t1-t0)*(f0+f1)/2
		segs = append(segs, Segment{
			Start:        a.TS.Add(time.Duration(float64(total) * f0)),
			End:          a.TS.Add(time.Duration(float64(total) * f1)),
			State:        classify(mid, rules),
			TempCC:       mid,
			Interpolated: true,
		})
	}
	return segs
}

// merge joins adjacent segments that are identical apart from their bounds
// (same state, same temperature), so MKT and durations are unaffected.
func merge(segs []Segment) []Segment {
	if len(segs) < 2 {
		return segs
	}
	out := []Segment{segs[0]}
	for _, s := range segs[1:] {
		last := &out[len(out)-1]
		if s.State == last.State && s.Interpolated == last.Interpolated && s.Start.Equal(last.End) && s.TempCC == last.TempCC {
			last.End = s.End
			continue
		}
		out = append(out, s)
	}
	return out
}

func (r *Result) addBreachOnce(rule string, at time.Time) {
	for i, b := range r.Breaches {
		if b.Rule == rule {
			if at.Before(b.At) {
				r.Breaches[i].At = at
			}
			return
		}
	}
	r.Breaches = append(r.Breaches, Breach{Rule: rule, At: at})
}

// excursionIntervals returns the stretches that count against the limits.
func (r *Result) excursionIntervals() []Segment {
	var out []Segment
	for _, s := range r.Segments {
		// Unknown only appears under the Uncertain policy or for a trailing gap
		// that cannot be interpolated; both are counted conservatively.
		if s.State != InRange {
			out = append(out, s)
		}
	}
	return out
}

func (r *Result) tally(rules Rules) {
	for _, s := range r.Segments {
		switch s.State {
		case High:
			r.HighDuration += s.Duration()
		case Low:
			r.LowDuration += s.Duration()
		case Unknown:
			r.UncertainDuration += s.Duration()
		}
	}
	// Cumulative breach time: walk the excursion intervals until the budget
	// is used up.
	var sum time.Duration
	for _, s := range r.excursionIntervals() {
		d := s.Duration()
		if sum+d > rules.MaxCumulative && !r.Breached(RuleCumulative) {
			r.addBreachOnce(RuleCumulative, s.Start.Add(rules.MaxCumulative-sum))
		}
		sum += d
	}
	r.ExcursionDuration = sum
}

// window finds the maximum excursion time inside any window of length W.
// The optimum is attained with a window that starts at an interval start or
// ends at an interval end, so only those 2n candidates are checked.
func (r *Result) window(rules Rules) {
	ivs := r.excursionIntervals()
	if len(ivs) == 0 || rules.Window <= 0 {
		return
	}
	prefix := make([]time.Duration, len(ivs)+1)
	for i, s := range ivs {
		prefix[i+1] = prefix[i] + s.Duration()
	}
	// covered returns the excursion time inside [from, to).
	covered := func(from, to time.Time) time.Duration {
		// first interval ending after from
		lo := sort.Search(len(ivs), func(i int) bool { return ivs[i].End.After(from) })
		// first interval starting at or after to
		hi := sort.Search(len(ivs), func(i int) bool { return !ivs[i].Start.Before(to) })
		if lo >= hi {
			return 0
		}
		total := prefix[hi] - prefix[lo]
		if ivs[lo].Start.Before(from) {
			total -= from.Sub(ivs[lo].Start)
		}
		if ivs[hi-1].End.After(to) {
			total -= ivs[hi-1].End.Sub(to)
		}
		return total
	}
	best, bestStart := time.Duration(-1), time.Time{}
	try := func(start time.Time) {
		if c := covered(start, start.Add(rules.Window)); c > best {
			best, bestStart = c, start
		}
	}
	for _, s := range ivs {
		try(s.Start)
		try(s.End.Add(-rules.Window))
	}
	r.MaxWindowExcursion, r.MaxWindowStart = best, bestStart
	if best > rules.WindowMax {
		// report the moment the allowance inside that window ran out
		r.addBreachOnce(RuleWindow, firstExceed(ivs, bestStart, rules.WindowMax))
	}
}

func firstExceed(ivs []Segment, from time.Time, budget time.Duration) time.Time {
	var used time.Duration
	for _, s := range ivs {
		if !s.End.After(from) {
			continue
		}
		start := s.Start
		if start.Before(from) {
			start = from
		}
		d := s.End.Sub(start)
		if used+d > budget {
			return start.Add(budget - used)
		}
		used += d
	}
	return from
}

// mkt computes the time-weighted mean kinetic temperature over measured and
// interpolated time. Unknown gaps carry no measurement and are excluded.
//
//	MKT = (Ea/R) / -ln( Σ wᵢ·exp(-Ea/(R·Tᵢ)) / Σ wᵢ )
func (r *Result) mkt(rules Rules) {
	ea := rules.ActivationEnergy
	if ea <= 0 {
		return
	}
	var num, den float64
	for _, s := range r.Segments {
		if s.State == Unknown {
			continue
		}
		w := s.Duration().Seconds()
		tk := s.TempCC/100 + 273.15
		num += w * math.Exp(-ea/(GasConstant*tk))
		den += w
	}
	if den == 0 {
		return
	}
	r.MKTCelsius = (ea/GasConstant)/(-math.Log(num/den)) - 273.15
	r.HasMKT = true
}

// MKT is a convenience for equally weighted temperatures in °C.
func MKT(tempsC []float64, activationEnergy float64) float64 {
	var sum float64
	for _, t := range tempsC {
		sum += math.Exp(-activationEnergy / (GasConstant * (t + 273.15)))
	}
	return (activationEnergy/GasConstant)/(-math.Log(sum/float64(len(tempsC)))) - 273.15
}
