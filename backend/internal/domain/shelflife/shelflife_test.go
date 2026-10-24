package shelflife

import (
	"math"
	"testing"
	"time"

	"github.com/AhmetYorat/soguk-zincir/backend/internal/domain/excursion"
)

var produced = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

var milk = Product{ShelfLife: 7 * 24 * time.Hour, RefCC: 800, ActivationEnergy: 83144}

func seg(fromH, toH float64, tempC float64) excursion.Segment {
	return excursion.Segment{
		Start:  produced.Add(time.Duration(fromH * float64(time.Hour))),
		End:    produced.Add(time.Duration(toH * float64(time.Hour))),
		TempCC: tempC * 100,
	}
}

func TestFactorIsOneAtOrBelowReference(t *testing.T) {
	for _, temp := range []float64{-20, 0, 4, 8} {
		if f := Factor(temp, 8, 83144); f != 1 {
			t.Fatalf("Factor(%v) = %v", temp, f)
		}
	}
}

func TestFactorGrowsWithTemperature(t *testing.T) {
	f12, f18 := Factor(12, 8, 83144), Factor(18, 8, 83144)
	if !(f12 > 1 && f18 > f12) {
		t.Fatalf("factors not increasing: %v %v", f12, f18)
	}
	// ~10 K warmer roughly triples the rate at Ea = 83 kJ/mol (Q10 ≈ 3.3)
	if q10 := Factor(18, 8, 83144); q10 < 3 || q10 > 3.6 {
		t.Fatalf("Q10 = %v", q10)
	}
}

func TestComputeAtReferenceIsNominal(t *testing.T) {
	now := produced.Add(48 * time.Hour)
	est := Compute(milk, produced, now, []excursion.Segment{seg(0, 48, 5)})
	if est.Penalty != 0 {
		t.Fatalf("penalty = %v", est.Penalty)
	}
	if est.Remaining != 5*24*time.Hour {
		t.Fatalf("remaining = %v", est.Remaining)
	}
	if !est.ExpiresAt.Equal(est.NominalEnd) {
		t.Fatalf("expires %v nominal %v", est.ExpiresAt, est.NominalEnd)
	}
}

func TestComputeWarmPeriodShortensShelfLife(t *testing.T) {
	now := produced.Add(48 * time.Hour)
	// 2 hours at 18 °C
	est := Compute(milk, produced, now, []excursion.Segment{seg(0, 10, 5), seg(10, 12, 18), seg(12, 48, 5)})
	f := Factor(18, 8, 83144)
	wantPenalty := time.Duration((f - 1) * 2 * float64(time.Hour))
	if d := est.Penalty - wantPenalty; d > time.Second || d < -time.Second {
		t.Fatalf("penalty = %v, want %v", est.Penalty, wantPenalty)
	}
	if est.Remaining >= 5*24*time.Hour {
		t.Fatal("remaining must be below nominal")
	}
}

func TestUncoveredTimeAgesNominally(t *testing.T) {
	now := produced.Add(24 * time.Hour)
	est := Compute(milk, produced, now, nil)
	if est.Covered != 0 || est.Equivalent != 24*time.Hour {
		t.Fatalf("%+v", est)
	}
}

func TestSegmentsAreClippedToLifetime(t *testing.T) {
	now := produced.Add(10 * time.Hour)
	est := Compute(milk, produced, now, []excursion.Segment{seg(-5, 20, 5)})
	if est.Covered != 10*time.Hour {
		t.Fatalf("covered = %v", est.Covered)
	}
}

func TestExpiredAndSellable(t *testing.T) {
	short := Product{ShelfLife: 10 * time.Hour, RefCC: 800, ActivationEnergy: 83144}
	now := produced.Add(8 * time.Hour)
	est := Compute(short, produced, now, []excursion.Segment{seg(0, 8, 30)})
	if !est.Expired {
		t.Fatalf("8 h at 30 °C must exhaust a 10 h shelf life, remaining %v", est.Remaining)
	}
	fresh := Compute(milk, produced, produced.Add(time.Hour), nil)
	if !fresh.Sellable(24*time.Hour) || fresh.Sellable(7*24*time.Hour) {
		t.Fatalf("sellable wrong, remaining %v", fresh.Remaining)
	}
}

func TestFactorMatchesArrheniusClosedForm(t *testing.T) {
	want := math.Exp(83144 / excursion.GasConstant * (1/281.15 - 1/291.15))
	if got := Factor(18, 8, 83144); math.Abs(got-want) > 1e-12 {
		t.Fatalf("%v != %v", got, want)
	}
}
