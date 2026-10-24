// Package loadplan builds a vehicle loading proposal for one product line.
//
// Steps:
//  1. Drop pallets whose remaining shelf life is shorter than transit time +
//     safety margin: they would arrive expired or nearly so.
//  2. Rank the rest FEFO (first expired, first out).
//  3. Solve a 0/1 knapsack over (quantity, pallet slots): maximise the number
//     of units loaded without exceeding the demand, the vehicle's weight limit
//     or its pallet slots. Among equally full loads, prefer earlier-expiring
//     pallets.
//
// Weight is quantity × unit weight for a single product, so the weight limit
// becomes a quantity limit and the DP has two dimensions only.
package loadplan

import (
	"errors"
	"sort"
	"time"
)

// Candidate is a pallet that could be loaded.
type Candidate struct {
	UnitID    int64         `json:"unit_id"`
	UnitCode  string        `json:"unit_code"`
	LotID     int64         `json:"lot_id"`
	LotCode   string        `json:"lot_code"`
	Quantity  int           `json:"quantity"`
	Remaining time.Duration `json:"-"` // computed by the shelf-life model
	ExpiresAt time.Time     `json:"expires_at"`
}

// Request describes the vehicle and the order line.
type Request struct {
	Demand          int           // units ordered; 0 = fill the vehicle
	UnitWeightG     int           // grams per unit
	CapacityKG      int           // remaining weight capacity of the vehicle
	CapacityPallets int           // remaining pallet slots
	Transit         time.Duration // expected door-to-door time
	SafetyMargin    time.Duration // shelf life the customer must still receive
}

// Rejection explains why a candidate was not considered.
type Rejection struct {
	Candidate Candidate `json:"candidate"`
	Reason    string    `json:"reason"`
}

// Plan is the proposal.
type Plan struct {
	Selected    []Candidate `json:"selected"`
	Rejected    []Rejection `json:"rejected"`
	Quantity    int         `json:"quantity"`
	WeightKG    float64     `json:"weight_kg"`
	Pallets     int         `json:"pallets"`
	MaxQuantity int         `json:"max_quantity"` // what the binding limit allowed
	Shortfall   int         `json:"shortfall"`    // demand not covered
}

// ReasonShelfLife is used when the pallet would not survive the trip.
const ReasonShelfLife = "remaining shelf life shorter than transit time + safety margin"

// ErrInvalid is returned for nonsensical requests.
var ErrInvalid = errors.New("loadplan: invalid request")

// maxQtyStates bounds the DP table (n × slots × states decisions are kept
// for backtracking); larger quantity limits are scaled down.
const maxQtyStates = 4000

// Build computes the plan.
func Build(req Request, cands []Candidate) (Plan, error) {
	if req.UnitWeightG <= 0 || req.CapacityKG < 0 || req.CapacityPallets < 0 || req.Demand < 0 {
		return Plan{}, ErrInvalid
	}
	need := req.Transit + req.SafetyMargin
	var eligible []Candidate
	var plan Plan
	for _, c := range cands {
		if c.Quantity <= 0 {
			continue
		}
		if c.Remaining < need {
			plan.Rejected = append(plan.Rejected, Rejection{Candidate: c, Reason: ReasonShelfLife})
			continue
		}
		eligible = append(eligible, c)
	}
	sortFEFO(eligible)

	qtyCap := req.CapacityKG * 1000 / req.UnitWeightG
	if req.Demand > 0 && req.Demand < qtyCap {
		qtyCap = req.Demand
	}
	plan.MaxQuantity = qtyCap
	chosen := solve(eligible, qtyCap, req.CapacityPallets)
	for _, i := range chosen {
		c := eligible[i]
		plan.Selected = append(plan.Selected, c)
		plan.Quantity += c.Quantity
	}
	sortFEFO(plan.Selected)
	plan.Pallets = len(plan.Selected)
	plan.WeightKG = float64(plan.Quantity*req.UnitWeightG) / 1000
	if req.Demand > plan.Quantity {
		plan.Shortfall = req.Demand - plan.Quantity
	}
	return plan, nil
}

func sortFEFO(cs []Candidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		if !cs[i].ExpiresAt.Equal(cs[j].ExpiresAt) {
			return cs[i].ExpiresAt.Before(cs[j].ExpiresAt)
		}
		return cs[i].UnitID < cs[j].UnitID
	})
}

// solve returns indexes of the chosen candidates. cands must be FEFO sorted.
//
// value(i) = qty(i)·B + (n − i), with B larger than any possible sum of the
// FEFO bonus, so quantity is maximised first and earlier pallets win ties.
func solve(cands []Candidate, qtyCap, slots int) []int {
	n := len(cands)
	if n == 0 || qtyCap <= 0 || slots <= 0 {
		return nil
	}
	if slots > n {
		slots = n
	}
	// Scale quantities down if the table would be too large. Scaling rounds
	// weights up, so a scaled solution never violates the real capacity.
	scale := 1
	for qtyCap/scale > maxQtyStates {
		scale *= 2
	}
	w := make([]int, n)
	for i, c := range cands {
		w[i] = (c.Quantity + scale - 1) / scale
	}
	capS := qtyCap / scale

	B := int64(n*(n+1)/2 + 1)
	width := capS + 1
	// best[s][q] over items processed so far; take[i] records decisions.
	best := make([]int64, (slots+1)*width)
	take := make([][]bool, n)
	for i := 0; i < n; i++ {
		take[i] = make([]bool, (slots+1)*width)
		val := int64(cands[i].Quantity)*B + int64(n-i)
		for s := slots; s >= 1; s-- {
			for q := capS; q >= w[i]; q-- {
				cand := best[(s-1)*width+q-w[i]] + val
				if cand > best[s*width+q] {
					best[s*width+q] = cand
					take[i][s*width+q] = true
				}
			}
		}
	}
	// best is monotone in s and q, so the optimum is at the full corner.
	s, q := slots, capS
	var out []int
	for i := n - 1; i >= 0; i-- {
		if take[i][s*width+q] {
			out = append(out, i)
			s--
			q -= w[i]
		}
	}
	return out
}
