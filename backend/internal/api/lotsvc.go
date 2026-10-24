package api

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AhmetYorat/soguk-zincir/backend/internal/domain/excursion"
	"github.com/AhmetYorat/soguk-zincir/backend/internal/domain/shelflife"
	"github.com/AhmetYorat/soguk-zincir/backend/internal/httpx"
)

// Product mirrors the products table.
type Product struct {
	ID                      int64     `json:"id"`
	SKU                     string    `json:"sku"`
	Name                    string    `json:"name"`
	TempMinCC               int       `json:"temp_min_cc"`
	TempMaxCC               int       `json:"temp_max_cc"`
	MaxCumulativeExcursionS int       `json:"max_cumulative_excursion_s"`
	WindowS                 int       `json:"window_s"`
	WindowMaxExcursionS     int       `json:"window_max_excursion_s"`
	ShelfLifeH              int       `json:"shelf_life_h"`
	ActivationEnergyJMol    int       `json:"activation_energy_j_mol"`
	GapPolicy               string    `json:"gap_policy"`
	MaxGapS                 int       `json:"max_gap_s"`
	UnitWeightG             int       `json:"unit_weight_g"`
	ListPriceKurus          int64     `json:"list_price_kurus"`
	CreatedAt               time.Time `json:"created_at"`
}

const productCols = `id, sku, name, temp_min_cc, temp_max_cc, max_cumulative_excursion_s, window_s,
	window_max_excursion_s, shelf_life_h, activation_energy_j_mol, gap_policy, max_gap_s, unit_weight_g,
	list_price_kurus, created_at`

// Rules converts product limits for the excursion engine.
func (p Product) Rules() excursion.Rules {
	return excursion.Rules{
		MinCC: p.TempMinCC, MaxCC: p.TempMaxCC,
		MaxCumulative:    time.Duration(p.MaxCumulativeExcursionS) * time.Second,
		Window:           time.Duration(p.WindowS) * time.Second,
		WindowMax:        time.Duration(p.WindowMaxExcursionS) * time.Second,
		MaxGap:           time.Duration(p.MaxGapS) * time.Second,
		Policy:           excursion.GapPolicy(p.GapPolicy),
		ActivationEnergy: float64(p.ActivationEnergyJMol),
	}
}

// ShelfModel converts product data for the shelf-life model.
func (p Product) ShelfModel() shelflife.Product {
	return shelflife.Product{
		ShelfLife:        time.Duration(p.ShelfLifeH) * time.Hour,
		RefCC:            p.TempMaxCC,
		ActivationEnergy: float64(p.ActivationEnergyJMol),
	}
}

func loadProduct(ctx context.Context, q Querier, id int64) (Product, error) {
	rows, err := q.Query(ctx, `SELECT `+productCols+` FROM products WHERE id = $1`, id)
	if err != nil {
		return Product{}, err
	}
	p, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Product])
	if errors.Is(err, pgx.ErrNoRows) {
		return p, httpx.ErrNotFound
	}
	return p, err
}

// Exposure is a stretch of time a unit (or its ancestor) spent somewhere.
type Exposure struct {
	UnitID       int64     `json:"unit_id"`
	UnitCode     string    `json:"unit_code"`
	LocationID   int64     `json:"location_id"`
	LocationCode string    `json:"location_code"`
	LocationName string    `json:"location_name"`
	LocationKind string    `json:"location_kind"`
	From         time.Time `json:"from"`
	To           time.Time `json:"to"`
	Depth        int       `json:"depth"` // 0 = the unit itself, 1 = parent, …
}

// unitHistory walks the lineage backwards for one lot: the unit's own
// custody plus that of every ancestor up to the moment the lot left it.
// Following only edges of the given lot matters after merges: a mixed pallet
// has several parents but each lot came through exactly one path.
const unitHistorySQL = `
WITH RECURSIVE path (unit_id, until, depth) AS (
    SELECT $1::bigint, 'infinity'::timestamptz, 0
  UNION ALL
    SELECT l.parent_unit_id, l.at, p.depth + 1
    FROM unit_lineage l JOIN path p ON l.child_unit_id = p.unit_id
    WHERE l.lot_id = $2 AND l.parent_unit_id IS NOT NULL AND p.depth < 64
)
SELECT c.unit_id, u.code, c.location_id, loc.code, loc.name, loc.kind::text,
       c.from_ts, least(coalesce(c.to_ts, $3), p.until, $3) AS to_ts, p.depth
FROM path p
JOIN custody_events c ON c.unit_id = p.unit_id AND c.from_ts < least(p.until, $3)
JOIN units u ON u.id = c.unit_id
JOIN locations loc ON loc.id = c.location_id
ORDER BY c.from_ts`

func unitHistory(ctx context.Context, q Querier, unitID, lotID int64, now time.Time) ([]Exposure, error) {
	rows, err := q.Query(ctx, unitHistorySQL, unitID, lotID, now)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Exposure])
}

// exposureReadings returns one value per 30 s across the exposures: the
// highest reading of all sensors at the location (worst sensor wins, which
// is the conservative choice for a cold chain). Data older than the raw
// retention falls back to the 1-minute rollup maximum.
const exposureReadingsSQL = `
WITH e AS (
    SELECT * FROM unnest($1::bigint[], $2::timestamptz[], $3::timestamptz[]) AS e(loc, f, t)
)
SELECT b, max(v)::int FROM (
    SELECT date_bin('30 seconds', r.ts, timestamptz '2000-01-01') AS b, r.temp_cc AS v
    FROM e JOIN sensors s ON s.location_id = e.loc
    JOIN readings r ON r.sensor_id = s.id AND r.ts >= greatest(e.f, $4) AND r.ts < e.t
  UNION ALL
    SELECT r.bucket, r.temp_max_cc
    FROM e JOIN sensors s ON s.location_id = e.loc
    JOIN readings_1m r ON r.sensor_id = s.id AND r.bucket >= e.f AND r.bucket < least(e.t, $4)
) x GROUP BY b ORDER BY b`

func (s *Server) exposureReadings(ctx context.Context, q Querier, exps []Exposure) ([]excursion.Reading, error) {
	var locs []int64
	var froms, tos []time.Time
	for _, e := range exps {
		if e.LocationKind == "customer" || !e.To.After(e.From) {
			continue // not our custody any more / empty interval
		}
		locs = append(locs, e.LocationID)
		froms = append(froms, e.From)
		tos = append(tos, e.To)
	}
	if len(locs) == 0 {
		return nil, nil
	}
	rawCutoff := s.now().Add(-s.cfg.RawRetention + time.Hour)
	rows, err := q.Query(ctx, exposureReadingsSQL, locs, froms, tos, rawCutoff)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (excursion.Reading, error) {
		var r excursion.Reading
		err := row.Scan(&r.TS, &r.TempCC)
		return r, err
	})
}

// UnitEvaluation is the cold-chain verdict for one lot on one unit.
type UnitEvaluation struct {
	UnitID     int64            `json:"unit_id"`
	UnitCode   string           `json:"unit_code"`
	UnitStatus string           `json:"unit_status"`
	Quantity   int              `json:"quantity"`
	Exposures  []Exposure       `json:"exposures"`
	Readings   int              `json:"readings"`
	Excursion  ExcursionSummary `json:"excursion"`
	ShelfLife  ShelfLifeSummary `json:"shelf_life"`
	result     excursion.Result
	estimate   shelflife.Estimate
}

// ExcursionSummary is the JSON form of an excursion.Result.
type ExcursionSummary struct {
	From             time.Time          `json:"from"`
	To               time.Time          `json:"to"`
	HighS            int64              `json:"high_s"`
	LowS             int64              `json:"low_s"`
	UncertainS       int64              `json:"uncertain_s"`
	CumulativeS      int64              `json:"cumulative_excursion_s"`
	CumulativeLimitS int                `json:"cumulative_limit_s"`
	MaxWindowS       int64              `json:"max_window_excursion_s"`
	MaxWindowStart   *time.Time         `json:"max_window_start"`
	WindowLimitS     int                `json:"window_limit_s"`
	PeakC            float64            `json:"peak_c"`
	MinC             float64            `json:"min_c"`
	MKTC             *float64           `json:"mkt_c"`
	Gaps             []excursion.Gap    `json:"gaps"`
	Breaches         []excursion.Breach `json:"breaches"`
	GapPolicy        string             `json:"gap_policy"`
}

// ShelfLifeSummary is the JSON form of a shelflife.Estimate.
type ShelfLifeSummary struct {
	NominalH   float64   `json:"nominal_h"`
	ElapsedH   float64   `json:"elapsed_h"`
	PenaltyH   float64   `json:"penalty_h"`
	RemainingH float64   `json:"remaining_h"`
	ExpiresAt  time.Time `json:"expires_at"`
	NominalEnd time.Time `json:"nominal_expires_at"`
	Expired    bool      `json:"expired"`
}

func summarise(res excursion.Result, p Product) ExcursionSummary {
	sum := ExcursionSummary{
		From: res.Start, To: res.End,
		HighS: int64(res.HighDuration.Seconds()), LowS: int64(res.LowDuration.Seconds()),
		UncertainS: int64(res.UncertainDuration.Seconds()), CumulativeS: int64(res.ExcursionDuration.Seconds()),
		CumulativeLimitS: p.MaxCumulativeExcursionS,
		MaxWindowS:       int64(res.MaxWindowExcursion.Seconds()), WindowLimitS: p.WindowMaxExcursionS,
		PeakC: float64(res.PeakCC) / 100, MinC: float64(res.MinCC) / 100,
		Gaps: res.Gaps, Breaches: res.Breaches, GapPolicy: p.GapPolicy,
	}
	if res.MaxWindowExcursion > 0 {
		t := res.MaxWindowStart
		sum.MaxWindowStart = &t
	}
	if res.HasMKT {
		m := res.MKTCelsius
		sum.MKTC = &m
	}
	if sum.Gaps == nil {
		sum.Gaps = []excursion.Gap{}
	}
	if sum.Breaches == nil {
		sum.Breaches = []excursion.Breach{}
	}
	return sum
}

func shelfSummary(e shelflife.Estimate, p Product) ShelfLifeSummary {
	h := func(d time.Duration) float64 { return float64(int64(d.Hours()*100)) / 100 }
	return ShelfLifeSummary{
		NominalH: float64(p.ShelfLifeH), ElapsedH: h(e.Elapsed), PenaltyH: h(e.Penalty), RemainingH: h(e.Remaining),
		ExpiresAt: e.ExpiresAt, NominalEnd: e.NominalEnd, Expired: e.Expired,
	}
}

// LotEvaluation combines all units carrying the lot. The lot is only as good
// as its worst part, so the lot-level figures are the worst unit's.
type LotEvaluation struct {
	LotID       int64            `json:"lot_id"`
	LotCode     string           `json:"lot_code"`
	Status      string           `json:"status"`
	ProducedAt  time.Time        `json:"produced_at"`
	Product     Product          `json:"product"`
	Units       []UnitEvaluation `json:"units"`
	Worst       *UnitEvaluation  `json:"worst"`
	EvaluatedAt time.Time        `json:"evaluated_at"`
}

func (s *Server) evaluateLot(ctx context.Context, q Querier, lotID int64) (LotEvaluation, error) {
	var ev LotEvaluation
	var productID int64
	err := q.QueryRow(ctx, `SELECT id, code, status::text, produced_at, product_id FROM lots WHERE id = $1`, lotID).
		Scan(&ev.LotID, &ev.LotCode, &ev.Status, &ev.ProducedAt, &productID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ev, httpx.ErrNotFound
	}
	if err != nil {
		return ev, err
	}
	if ev.Product, err = loadProduct(ctx, q, productID); err != nil {
		return ev, err
	}
	now := s.now()
	ev.EvaluatedAt = now
	rows, err := q.Query(ctx, `
		SELECT u.id, u.code, u.status::text, c.quantity FROM unit_contents c JOIN units u ON u.id = c.unit_id
		WHERE c.lot_id = $1 AND c.quantity > 0 AND u.status <> 'consumed' ORDER BY u.id`, lotID)
	if err != nil {
		return ev, err
	}
	type holder struct {
		id       int64
		code     string
		status   string
		quantity int
	}
	holders, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (holder, error) {
		var h holder
		return h, r.Scan(&h.id, &h.code, &h.status, &h.quantity)
	})
	if err != nil {
		return ev, err
	}
	rules := ev.Product.Rules()
	for _, h := range holders {
		exps, err := unitHistory(ctx, q, h.id, lotID, now)
		if err != nil {
			return ev, err
		}
		readings, err := s.exposureReadings(ctx, q, exps)
		if err != nil {
			return ev, err
		}
		until := measuredUntil(exps, now)
		res := excursion.Evaluate(readings, rules, until)
		est := shelflife.Compute(ev.Product.ShelfModel(), ev.ProducedAt, now, res.Segments)
		if exps == nil {
			exps = []Exposure{}
		}
		ev.Units = append(ev.Units, UnitEvaluation{
			UnitID: h.id, UnitCode: h.code, UnitStatus: h.status, Quantity: h.quantity,
			Exposures: exps, Readings: len(readings),
			Excursion: summarise(res, ev.Product), ShelfLife: shelfSummary(est, ev.Product),
			result: res, estimate: est,
		})
	}
	for i := range ev.Units {
		u := &ev.Units[i]
		if ev.Worst == nil || u.estimate.Remaining < ev.Worst.estimate.Remaining ||
			(u.estimate.Remaining == ev.Worst.estimate.Remaining && u.result.ExcursionDuration > ev.Worst.result.ExcursionDuration) {
			ev.Worst = u
		}
	}
	if ev.Units == nil {
		ev.Units = []UnitEvaluation{}
	}
	return ev, nil
}

// measuredUntil is the end of the last stretch the cold chain was ours.
func measuredUntil(exps []Exposure, now time.Time) time.Time {
	var until time.Time
	for _, e := range exps {
		if e.LocationKind != "customer" && e.To.After(until) {
			until = e.To
		}
	}
	if until.IsZero() || until.After(now) {
		return now
	}
	return until
}
