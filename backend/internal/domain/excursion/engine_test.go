package excursion

import (
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)

// chilled: 2–8 °C, 30 min cumulative, at most 10 min in any hour.
func chilled() Rules {
	return Rules{
		MinCC: 200, MaxCC: 800,
		MaxCumulative: 30 * time.Minute,
		Window:        time.Hour, WindowMax: 10 * time.Minute,
		MaxGap:           2 * time.Minute,
		Policy:           Uncertain,
		ActivationEnergy: 83144,
	}
}

// series builds one reading every 30 s from the given temperatures (°C).
func series(start time.Time, temps ...float64) []Reading {
	rs := make([]Reading, len(temps))
	for i, t := range temps {
		rs[i] = Reading{TS: start.Add(time.Duration(i) * 30 * time.Second), TempCC: int(math.Round(t * 100))}
	}
	return rs
}

func repeat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func concat(parts ...[]float64) []float64 {
	var out []float64
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func end(rs []Reading) time.Time { return rs[len(rs)-1].TS.Add(30 * time.Second) }

func TestInRangeSeriesHasNoBreaches(t *testing.T) {
	rs := series(t0, repeat(5, 120)...)
	res := Evaluate(rs, chilled(), end(rs))
	if len(res.Breaches) != 0 {
		t.Fatalf("expected no breaches, got %+v", res.Breaches)
	}
	if res.ExcursionDuration != 0 {
		t.Fatalf("excursion = %v", res.ExcursionDuration)
	}
	if math.Abs(res.MKTCelsius-5) > 1e-9 {
		t.Fatalf("MKT of constant 5 °C must be 5, got %v", res.MKTCelsius)
	}
}

func TestHighExcursionDurationIsCountedPerHeldReading(t *testing.T) {
	// 10 readings at 9 °C = 5 minutes above the limit
	rs := series(t0, concat(repeat(5, 20), repeat(9, 10), repeat(5, 20))...)
	res := Evaluate(rs, chilled(), end(rs))
	if res.HighDuration != 5*time.Minute {
		t.Fatalf("high = %v, want 5m", res.HighDuration)
	}
	if !res.Breached(RuleLimitHigh) {
		t.Fatal("limit_high not raised")
	}
	if got := res.Breaches[0].At; !got.Equal(t0.Add(10 * time.Minute)) {
		t.Fatalf("limit breach at %v, want first 9 °C reading", got)
	}
	if res.Breached(RuleWindow) || res.Breached(RuleCumulative) {
		t.Fatal("5 min must not trigger window or cumulative rules")
	}
	if res.PeakCC != 900 {
		t.Fatalf("peak = %d", res.PeakCC)
	}
}

func TestLowExcursion(t *testing.T) {
	rs := series(t0, concat(repeat(4, 10), repeat(1, 4), repeat(4, 10))...)
	res := Evaluate(rs, chilled(), end(rs))
	if res.LowDuration != 2*time.Minute || !res.Breached(RuleLimitLow) {
		t.Fatalf("low = %v breaches %+v", res.LowDuration, res.Breaches)
	}
}

func TestSlidingWindowRule(t *testing.T) {
	// two 6-minute excursions 20 minutes apart: each is fine on its own, but
	// together they put 12 minutes into one hour (limit 10).
	temps := concat(repeat(5, 10), repeat(10, 12), repeat(5, 40), repeat(10, 12), repeat(5, 10))
	rs := series(t0, temps...)
	res := Evaluate(rs, chilled(), end(rs))
	if res.MaxWindowExcursion != 12*time.Minute {
		t.Fatalf("max window = %v, want 12m", res.MaxWindowExcursion)
	}
	if !res.Breached(RuleWindow) {
		t.Fatal("window rule not raised")
	}
	// allowance runs out 4 minutes into the second excursion
	second := t0.Add(time.Duration(10+12+40) * 30 * time.Second)
	for _, b := range res.Breaches {
		if b.Rule == RuleWindow && !b.At.Equal(second.Add(4*time.Minute)) {
			t.Fatalf("window breach at %v, want %v", b.At, second.Add(4*time.Minute))
		}
	}
}

func TestSlidingWindowSpacedFarApartIsFine(t *testing.T) {
	// same two excursions but 70 minutes apart never share a window
	temps := concat(repeat(10, 12), repeat(5, 140), repeat(10, 12))
	rs := series(t0, temps...)
	res := Evaluate(rs, chilled(), end(rs))
	if res.MaxWindowExcursion != 6*time.Minute || res.Breached(RuleWindow) {
		t.Fatalf("max window = %v breaches %+v", res.MaxWindowExcursion, res.Breaches)
	}
}

func TestCumulativeRuleAcrossManyShortExcursions(t *testing.T) {
	// 8 excursions of 4 min every 2 h: 32 min total > 30 min budget, but no
	// single hour exceeds the window rule.
	var temps []float64
	for i := 0; i < 8; i++ {
		temps = append(temps, repeat(9, 8)...)
		temps = append(temps, repeat(5, 232)...)
	}
	rs := series(t0, temps...)
	res := Evaluate(rs, chilled(), end(rs))
	if res.ExcursionDuration != 32*time.Minute {
		t.Fatalf("cumulative = %v", res.ExcursionDuration)
	}
	if !res.Breached(RuleCumulative) || res.Breached(RuleWindow) {
		t.Fatalf("breaches %+v", res.Breaches)
	}
	// budget is exhausted 2 minutes into the 8th excursion
	want := t0.Add(7*2*time.Hour + 2*time.Minute)
	for _, b := range res.Breaches {
		if b.Rule == RuleCumulative && !b.At.Equal(want) {
			t.Fatalf("cumulative breach at %v, want %v", b.At, want)
		}
	}
}

func TestGapUncertainPolicyCountsGapAsExcursion(t *testing.T) {
	a := series(t0, repeat(5, 10)...)
	b := series(t0.Add(30*time.Minute), repeat(5, 10)...) // 25.5 min hole
	rs := append(a, b...)
	res := Evaluate(rs, chilled(), end(rs))
	if len(res.Gaps) != 1 {
		t.Fatalf("gaps = %+v", res.Gaps)
	}
	gap := res.Gaps[0].End.Sub(res.Gaps[0].Start)
	if res.UncertainDuration != gap || res.ExcursionDuration != gap {
		t.Fatalf("uncertain=%v excursion=%v gap=%v", res.UncertainDuration, res.ExcursionDuration, gap)
	}
	if !res.Breached(RuleGap) || !res.Breached(RuleWindow) {
		t.Fatalf("breaches %+v", res.Breaches)
	}
	if res.HighDuration != 0 {
		t.Fatal("no measured high time expected")
	}
	// MKT ignores unmeasured time
	if math.Abs(res.MKTCelsius-5) > 1e-9 {
		t.Fatalf("MKT = %v", res.MKTCelsius)
	}
}

func TestGapInterpolatePolicyFillsLinearly(t *testing.T) {
	// 6 °C, then nothing for 20 min, then 10 °C. The line crosses 8 °C halfway
	// through the hole, so 10 of the 20 minutes are high.
	rs := []Reading{
		{TS: t0, TempCC: 600},
		{TS: t0.Add(20 * time.Minute), TempCC: 1000},
	}
	rules := chilled()
	rules.Policy = Interpolate
	res := Evaluate(rs, rules, t0.Add(20*time.Minute))
	if res.HighDuration != 10*time.Minute {
		t.Fatalf("high = %v, want 10m", res.HighDuration)
	}
	if res.UncertainDuration != 0 {
		t.Fatal("interpolated gap must not be uncertain")
	}
	if !res.Breached(RuleGap) {
		t.Fatal("gap must still be reported")
	}
}

func TestTrailingGapIsUnknownEvenWhenInterpolating(t *testing.T) {
	rs := series(t0, repeat(5, 4)...)
	rules := chilled()
	rules.Policy = Interpolate
	res := Evaluate(rs, rules, t0.Add(time.Hour))
	if res.UncertainDuration < 58*time.Minute {
		t.Fatalf("uncertain = %v", res.UncertainDuration)
	}
}

func TestShortGapWithinToleranceIsHeld(t *testing.T) {
	rs := []Reading{{TS: t0, TempCC: 500}, {TS: t0.Add(90 * time.Second), TempCC: 500}}
	res := Evaluate(rs, chilled(), t0.Add(2*time.Minute))
	if len(res.Gaps) != 0 || res.Breached(RuleGap) {
		t.Fatalf("90 s is within max gap, got %+v", res.Gaps)
	}
}

func TestUnsortedAndDuplicateReadings(t *testing.T) {
	rs := series(t0, 5, 9, 9, 5)
	shuffled := []Reading{rs[3], rs[1], rs[0], rs[2], rs[1]}
	res := Evaluate(shuffled, chilled(), end(rs))
	if res.HighDuration != time.Minute {
		t.Fatalf("high = %v", res.HighDuration)
	}
}

func TestEmptySeries(t *testing.T) {
	res := Evaluate(nil, chilled(), t0)
	if res.HasMKT || len(res.Breaches) != 0 {
		t.Fatal("empty input must give empty result")
	}
}

func TestMKTKnownValue(t *testing.T) {
	// Reference value computed independently (Python, same USP <1160> formula)
	// for equal weights of 20 °C and 30 °C with ΔH = 83.144 kJ/mol: 26.2599 °C,
	// above the arithmetic mean because warm periods weigh more.
	got := MKT([]float64{20, 30}, 83144)
	if math.Abs(got-26.2599) > 0.001 {
		t.Fatalf("MKT = %.4f, want 26.2599", got)
	}
	if got <= 25 {
		t.Fatal("MKT must exceed the arithmetic mean for unequal temperatures")
	}
}

func TestMKTFromEngineMatchesClosedForm(t *testing.T) {
	rs := series(t0, concat(repeat(4, 60), repeat(12, 60))...)
	res := Evaluate(rs, chilled(), end(rs))
	want := MKT([]float64{4, 12}, 83144)
	if math.Abs(res.MKTCelsius-want) > 1e-6 {
		t.Fatalf("engine MKT %.6f, closed form %.6f", res.MKTCelsius, want)
	}
}

func TestBreachesAreOrderedByTime(t *testing.T) {
	temps := concat(repeat(1, 2), repeat(5, 10), repeat(12, 30))
	rs := series(t0, temps...)
	res := Evaluate(rs, chilled(), end(rs))
	for i := 1; i < len(res.Breaches); i++ {
		if res.Breaches[i].At.Before(res.Breaches[i-1].At) {
			t.Fatalf("not ordered: %+v", res.Breaches)
		}
	}
	if res.Breaches[0].Rule != RuleLimitLow {
		t.Fatalf("first breach = %s", res.Breaches[0].Rule)
	}
}
