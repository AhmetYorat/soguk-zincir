package loadplan

import (
	"math/rand/v2"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 1, 6, 0, 0, 0, time.UTC)

func cand(id int64, qty int, remainingH float64) Candidate {
	rem := time.Duration(remainingH * float64(time.Hour))
	return Candidate{UnitID: id, LotID: id, Quantity: qty, Remaining: rem, ExpiresAt: now.Add(rem)}
}

func baseReq() Request {
	return Request{
		UnitWeightG: 1000, CapacityKG: 1000, CapacityPallets: 10,
		Transit: 6 * time.Hour, SafetyMargin: 24 * time.Hour,
	}
}

func TestRejectsLotsThatWouldExpireInTransit(t *testing.T) {
	plan, err := Build(baseReq(), []Candidate{cand(1, 100, 20), cand(2, 100, 31), cand(3, 100, 29.9)})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Selected) != 1 || plan.Selected[0].UnitID != 2 {
		t.Fatalf("selected %+v", plan.Selected)
	}
	if len(plan.Rejected) != 2 || plan.Rejected[0].Reason != ReasonShelfLife {
		t.Fatalf("rejected %+v", plan.Rejected)
	}
}

func TestFEFOPreferredWhenQuantityTies(t *testing.T) {
	req := baseReq()
	req.Demand = 100
	// three identical pallets, different expiry: the earliest must be taken
	plan, _ := Build(req, []Candidate{cand(1, 100, 90), cand(2, 100, 40), cand(3, 100, 60)})
	if len(plan.Selected) != 1 || plan.Selected[0].UnitID != 2 {
		t.Fatalf("selected %+v", plan.Selected)
	}
}

func TestKnapsackBeatsGreedyFEFO(t *testing.T) {
	// demand 100: greedy FEFO takes 60 (earliest) then cannot fit 50 -> 60.
	// The optimum is 50 + 50 = 100.
	req := baseReq()
	req.Demand = 100
	plan, _ := Build(req, []Candidate{cand(1, 60, 40), cand(2, 50, 50), cand(3, 50, 60)})
	if plan.Quantity != 100 {
		t.Fatalf("quantity = %d, selected %+v", plan.Quantity, plan.Selected)
	}
	if plan.Shortfall != 0 {
		t.Fatalf("shortfall %d", plan.Shortfall)
	}
}

func TestRespectsWeightAndPalletSlots(t *testing.T) {
	req := baseReq()
	req.CapacityKG = 250 // 250 units of 1 kg
	req.CapacityPallets = 2
	plan, _ := Build(req, []Candidate{cand(1, 100, 40), cand(2, 100, 41), cand(3, 100, 42), cand(4, 40, 43)})
	if plan.Pallets > 2 || plan.Quantity > 250 {
		t.Fatalf("over capacity: %+v", plan)
	}
	if plan.Quantity != 200 {
		t.Fatalf("quantity = %d", plan.Quantity)
	}
	// with 3 slots the best is 100+100+40
	req.CapacityPallets = 3
	plan, _ = Build(req, []Candidate{cand(1, 100, 40), cand(2, 100, 41), cand(3, 100, 42), cand(4, 40, 43)})
	if plan.Quantity != 240 {
		t.Fatalf("quantity = %d", plan.Quantity)
	}
}

func TestShortfallReported(t *testing.T) {
	req := baseReq()
	req.Demand = 500
	plan, _ := Build(req, []Candidate{cand(1, 100, 40)})
	if plan.Shortfall != 400 {
		t.Fatalf("shortfall = %d", plan.Shortfall)
	}
}

func TestInvalidRequest(t *testing.T) {
	req := baseReq()
	req.UnitWeightG = 0
	if _, err := Build(req, nil); err != ErrInvalid {
		t.Fatalf("err = %v", err)
	}
}

func TestEmptyInputGivesEmptyPlan(t *testing.T) {
	plan, err := Build(baseReq(), nil)
	if err != nil || len(plan.Selected) != 0 {
		t.Fatalf("%+v %v", plan, err)
	}
}

// bruteForce returns the best achievable quantity.
func bruteForce(req Request, cands []Candidate) int {
	need := req.Transit + req.SafetyMargin
	var el []Candidate
	for _, c := range cands {
		if c.Remaining >= need {
			el = append(el, c)
		}
	}
	qtyCap := req.CapacityKG * 1000 / req.UnitWeightG
	if req.Demand > 0 && req.Demand < qtyCap {
		qtyCap = req.Demand
	}
	best := 0
	for mask := 0; mask < 1<<len(el); mask++ {
		q, n := 0, 0
		for i := range el {
			if mask&(1<<i) != 0 {
				q += el[i].Quantity
				n++
			}
		}
		if q <= qtyCap && n <= req.CapacityPallets && q > best {
			best = q
		}
	}
	return best
}

func TestMatchesBruteForceOnRandomInstances(t *testing.T) {
	r := rand.New(rand.NewPCG(42, 7))
	for iter := 0; iter < 300; iter++ {
		n := 1 + r.IntN(12)
		cands := make([]Candidate, n)
		for i := range cands {
			cands[i] = cand(int64(i+1), 1+r.IntN(120), float64(10+r.IntN(200)))
		}
		req := baseReq()
		req.CapacityKG = 50 + r.IntN(600)
		req.CapacityPallets = 1 + r.IntN(6)
		req.Demand = r.IntN(500)
		plan, err := Build(req, cands)
		if err != nil {
			t.Fatal(err)
		}
		if want := bruteForce(req, cands); plan.Quantity != want {
			t.Fatalf("iter %d: got %d want %d (req %+v)", iter, plan.Quantity, want, req)
		}
		if plan.Pallets > req.CapacityPallets || plan.Quantity > plan.MaxQuantity {
			t.Fatalf("iter %d: constraint violated %+v", iter, plan)
		}
	}
}

func TestLargeQuantitiesAreScaledSafely(t *testing.T) {
	req := baseReq()
	req.UnitWeightG = 1
	req.CapacityKG = 100 // 100 000 units, above the DP state limit
	req.CapacityPallets = 33
	var cands []Candidate
	for i := 0; i < 50; i++ {
		cands = append(cands, cand(int64(i+1), 3000+i*37, float64(40+i)))
	}
	plan, _ := Build(req, cands)
	if plan.Quantity > plan.MaxQuantity || plan.Pallets > 33 {
		t.Fatalf("scaled plan violates capacity: %+v", plan)
	}
	if plan.Quantity < plan.MaxQuantity*9/10 {
		t.Fatalf("scaled plan too poor: %d of %d", plan.Quantity, plan.MaxQuantity)
	}
}
