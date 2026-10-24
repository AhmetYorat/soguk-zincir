// Package shelflife recomputes the remaining shelf life of a lot from its
// temperature history using the Arrhenius model.
//
// A product's nominal shelf life is valid while it is kept at or below its
// upper storage limit (the reference temperature). Time spent warmer ages the
// product faster by the factor
//
//	k(T)/k(Tref) = exp( Ea/R · (1/Tref − 1/T) )
//
// so one hour at that temperature consumes `factor` hours of shelf life.
// Time below the reference is counted at the nominal rate (factor 1): the
// model never gives shelf life back, and freezing damage is a quality
// decision, not a shelf-life calculation.
package shelflife

import (
	"math"
	"time"

	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/domain/excursion"
)

// Factor returns how many times faster than nominal the product ages at tempC.
func Factor(tempC, refC, activationEnergy float64) float64 {
	if tempC <= refC {
		return 1
	}
	tk, rk := tempC+273.15, refC+273.15
	return math.Exp(activationEnergy / excursion.GasConstant * (1/rk - 1/tk))
}

// Product holds the inputs of the model.
type Product struct {
	ShelfLife        time.Duration
	RefCC            int // upper storage limit, centi-degrees
	ActivationEnergy float64
}

// Estimate is the outcome for one lot.
type Estimate struct {
	Elapsed    time.Duration // wall-clock time since production
	Covered    time.Duration // part of Elapsed with temperature data
	Equivalent time.Duration // shelf life consumed, in reference-temperature time
	Penalty    time.Duration // Equivalent − Elapsed: extra ageing caused by warmth
	Remaining  time.Duration // ShelfLife − Equivalent, may be negative
	ExpiresAt  time.Time
	NominalEnd time.Time // expiry if the lot had always been at the reference
	Expired    bool
}

// Compute estimates the remaining shelf life at `now` for a lot produced at
// `produced`, given the temperature segments known for it. Time not covered
// by any segment is aged at the nominal rate. Unknown segments carry the
// worse neighbouring temperature (see excursion.Segment), so gaps under the
// Uncertain policy are aged pessimistically.
func Compute(p Product, produced, now time.Time, segments []excursion.Segment) Estimate {
	refC := float64(p.RefCC) / 100
	est := Estimate{Elapsed: now.Sub(produced)}
	var weighted float64 // seconds
	for _, s := range segments {
		start, end := s.Start, s.End
		if start.Before(produced) {
			start = produced
		}
		if end.After(now) {
			end = now
		}
		if !end.After(start) {
			continue
		}
		d := end.Sub(start)
		est.Covered += d
		weighted += d.Seconds() * Factor(s.TempCC/100, refC, p.ActivationEnergy)
	}
	uncovered := est.Elapsed - est.Covered
	if uncovered < 0 {
		uncovered = 0 // overlapping segments; should not happen
	}
	est.Equivalent = uncovered + time.Duration(weighted*float64(time.Second))
	est.Penalty = est.Equivalent - est.Elapsed
	est.Remaining = p.ShelfLife - est.Equivalent
	est.ExpiresAt = now.Add(est.Remaining)
	est.NominalEnd = produced.Add(p.ShelfLife)
	est.Expired = est.Remaining <= 0
	return est
}

// Sellable reports whether the lot still has at least `need` of shelf life
// left, e.g. the transit time plus a safety margin.
func (e Estimate) Sellable(need time.Duration) bool {
	return e.Remaining >= need
}
