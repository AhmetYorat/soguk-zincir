package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/db"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/domain/excursion"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/httpx"
)

// ForwardNode is a unit reached from a lot through receive/split/merge edges.
type ForwardNode struct {
	UnitID       int64      `json:"unit_id"`
	UnitCode     string     `json:"unit_code"`
	UnitStatus   string     `json:"unit_status"`
	Depth        int        `json:"depth"`
	Quantity     int        `json:"quantity_now"`
	LocationName string     `json:"location"`
	ShipmentID   *int64     `json:"shipment_id"`
	ShipmentCode *string    `json:"shipment_code"`
	CustomerID   *int64     `json:"customer_id"`
	Customer     *string    `json:"customer"`
	Plate        *string    `json:"vehicle_plate"`
	DepartedAt   *time.Time `json:"departed_at"`
	DeliveredAt  *time.Time `json:"delivered_at"`
}

// forwardTraceSQL finds every unit a lot ever reached, following only edges
// of that lot. The path array guards against cycles; lineage is a DAG by
// construction, but a recall must never hang on bad data.
const forwardTraceSQL = `
WITH RECURSIVE tree (unit_id, depth, path) AS (
    SELECT child_unit_id, 0, ARRAY[child_unit_id]
    FROM unit_lineage WHERE op = 'receive' AND lot_id = $1
  UNION ALL
    SELECT l.child_unit_id, t.depth + 1, t.path || l.child_unit_id
    FROM unit_lineage l JOIN tree t ON l.parent_unit_id = t.unit_id
    WHERE l.lot_id = $1 AND NOT l.child_unit_id = ANY (t.path)
)
SELECT DISTINCT ON (t.unit_id)
       t.unit_id, u.code, u.status::text, t.depth, coalesce(uc.quantity, 0), loc.name,
       sh.id, sh.code, sh.customer_id, c.name, v.plate, sh.departed_at, sh.delivered_at
FROM tree t
JOIN units u ON u.id = t.unit_id
JOIN locations loc ON loc.id = u.location_id
LEFT JOIN unit_contents uc ON uc.unit_id = t.unit_id AND uc.lot_id = $1
LEFT JOIN shipment_items si ON si.unit_id = t.unit_id
LEFT JOIN shipments sh ON sh.id = si.shipment_id
LEFT JOIN customers c ON c.id = sh.customer_id
LEFT JOIN vehicles v ON v.id = sh.vehicle_id
ORDER BY t.unit_id, t.depth`

func forwardTrace(ctx context.Context, q Querier, lotID int64) ([]ForwardNode, error) {
	rows, err := q.Query(ctx, forwardTraceSQL, lotID)
	if err != nil {
		return nil, err
	}
	nodes, err := pgx.CollectRows(rows, pgx.RowToStructByPos[ForwardNode])
	if nodes == nil {
		nodes = []ForwardNode{}
	}
	return nodes, err
}

type affectedCustomer struct {
	CustomerID int64    `json:"customer_id"`
	Name       string   `json:"name"`
	Shipments  []string `json:"shipments"`
	Units      int      `json:"units"`
}

func customersOf(nodes []ForwardNode) []affectedCustomer {
	idx := map[int64]int{}
	out := []affectedCustomer{}
	for _, n := range nodes {
		if n.CustomerID == nil {
			continue
		}
		i, ok := idx[*n.CustomerID]
		if !ok {
			i = len(out)
			idx[*n.CustomerID] = i
			out = append(out, affectedCustomer{CustomerID: *n.CustomerID, Name: *n.Customer})
		}
		out[i].Units++
		if n.ShipmentCode != nil && !slices.Contains(out[i].Shipments, *n.ShipmentCode) {
			out[i].Shipments = append(out[i].Shipments, *n.ShipmentCode)
		}
	}
	return out
}

func (s *Server) traceLot(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	ctx := r.Context()
	var code string
	if err := s.pool.QueryRow(ctx, `SELECT code FROM lots WHERE id = $1`, id).Scan(&code); errors.Is(err, pgx.ErrNoRows) {
		return httpx.ErrNotFound
	} else if err != nil {
		return err
	}
	start := time.Now()
	nodes, err := forwardTrace(ctx, s.pool, id)
	if err != nil {
		return err
	}
	elapsed := time.Since(start)
	httpx.JSON(w, http.StatusOK, map[string]any{
		"lot_id": id, "lot_code": code, "units": nodes, "customers": customersOf(nodes),
		"query_ms": float64(elapsed.Microseconds()) / 1000,
	})
	return nil
}

// ExposureSummary adds measured temperatures to an exposure.
type ExposureSummary struct {
	Exposure
	Readings int      `json:"readings"`
	MinC     *float64 `json:"min_c"`
	MaxC     *float64 `json:"max_c"`
	MKTC     *float64 `json:"mkt_c"`
}

// traceUnit answers "where did this pallet's goods come from, where were they
// and at what temperature, and where did they go".
func (s *Server) traceUnit(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	ctx := r.Context()
	units, err := s.queryUnits(ctx, `WHERE u.id = $1`, id)
	if err != nil {
		return err
	}
	if len(units) == 0 {
		return httpx.ErrNotFound
	}
	// every lot that ever passed through this unit, even if it has left since
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT lo.id, lo.code, lo.product_id FROM unit_lineage l JOIN lots lo ON lo.id = l.lot_id
		WHERE l.child_unit_id = $1 OR l.parent_unit_id = $1 ORDER BY lo.id`, id)
	if err != nil {
		return err
	}
	type lotRef struct {
		id, product int64
		code        string
	}
	lots, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (lotRef, error) {
		var l lotRef
		return l, r.Scan(&l.id, &l.code, &l.product)
	})
	if err != nil {
		return err
	}
	type lotTrace struct {
		LotID      int64             `json:"lot_id"`
		LotCode    string            `json:"lot_code"`
		History    []ExposureSummary `json:"history"`
		Downstream []ForwardNode     `json:"downstream"`
		Excursion  ExcursionSummary  `json:"excursion"`
	}
	start := time.Now()
	now := s.now()
	var out []lotTrace
	for _, l := range lots {
		product, err := loadProduct(ctx, s.pool, l.product)
		if err != nil {
			return err
		}
		exps, err := unitHistory(ctx, s.pool, id, l.id, now)
		if err != nil {
			return err
		}
		hist := make([]ExposureSummary, 0, len(exps))
		var all []excursion.Reading
		for _, e := range exps {
			rs, err := s.exposureReadings(ctx, s.pool, []Exposure{e})
			if err != nil {
				return err
			}
			all = append(all, rs...)
			hist = append(hist, summariseExposure(e, rs, product))
		}
		res := excursion.Evaluate(all, product.Rules(), measuredUntil(exps, now))
		// downstream: the part of the lot's forward tree below this unit
		nodes, err := forwardTrace(ctx, s.pool, l.id)
		if err != nil {
			return err
		}
		below := descendantsOf(ctx, s.pool, id, l.id)
		var down []ForwardNode
		for _, n := range nodes {
			if below[n.UnitID] {
				down = append(down, n)
			}
		}
		if down == nil {
			down = []ForwardNode{}
		}
		out = append(out, lotTrace{LotID: l.id, LotCode: l.code, History: hist, Downstream: down, Excursion: summarise(res, product)})
	}
	if out == nil {
		out = []lotTrace{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"unit": units[0], "lots": out, "query_ms": float64(time.Since(start).Microseconds()) / 1000})
	return nil
}

func summariseExposure(e Exposure, rs []excursion.Reading, p Product) ExposureSummary {
	sum := ExposureSummary{Exposure: e, Readings: len(rs)}
	if len(rs) == 0 {
		return sum
	}
	minV, maxV := rs[0].TempCC, rs[0].TempCC
	temps := make([]float64, len(rs))
	for i, r := range rs {
		minV, maxV = min(minV, r.TempCC), max(maxV, r.TempCC)
		temps[i] = float64(r.TempCC) / 100
	}
	mn, mx := float64(minV)/100, float64(maxV)/100
	mkt := excursion.MKT(temps, float64(p.ActivationEnergyJMol))
	sum.MinC, sum.MaxC, sum.MKTC = &mn, &mx, &mkt
	return sum
}

// descendantsOf returns the unit and everything split or merged from it for
// one lot.
func descendantsOf(ctx context.Context, q Querier, unitID, lotID int64) map[int64]bool {
	rows, err := q.Query(ctx, `
		WITH RECURSIVE d (unit_id, path) AS (
		    SELECT $1::bigint, ARRAY[$1::bigint]
		  UNION ALL
		    SELECT l.child_unit_id, d.path || l.child_unit_id FROM unit_lineage l JOIN d ON l.parent_unit_id = d.unit_id
		    WHERE l.lot_id = $2 AND NOT l.child_unit_id = ANY (d.path)
		) SELECT DISTINCT unit_id FROM d`, unitID, lotID)
	out := map[int64]bool{unitID: true}
	if err != nil {
		return out
	}
	ids, _ := pgx.CollectRows(rows, pgx.RowTo[int64])
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// ---- recalls ----

func (s *Server) createRecall(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		LotID  int64  `json:"lot_id"`
		Reason string `json:"reason"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if len(in.Reason) < 3 {
		return httpx.Validation{"reason": "must be at least 3 characters"}.Err()
	}
	ctx := r.Context()
	var out map[string]any
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status, code string
		err := tx.QueryRow(ctx, `SELECT status::text, code FROM lots WHERE id = $1 FOR UPDATE`, in.LotID).Scan(&status, &code)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.Validation{"lot_id": "unknown lot"}.Err()
		}
		if err != nil {
			return err
		}
		if status == "recalled" {
			return httpx.Errorf(http.StatusConflict, "already_recalled", "lot %s is already recalled", code)
		}
		start := time.Now()
		nodes, err := forwardTrace(ctx, tx, in.LotID)
		if err != nil {
			return err
		}
		elapsed := time.Since(start)
		customers := customersOf(nodes)
		var id int64
		recallCode := fmt.Sprintf("RCL-%s-%d", code, s.now().Unix()%100000)
		if err := tx.QueryRow(ctx, `INSERT INTO recalls (code, lot_id, reason, initiated_by, affected_units, affected_customers, query_ms)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, recallCode, in.LotID, in.Reason, principal(ctx).UserID,
			len(nodes), len(customers), int(elapsed.Milliseconds())).Scan(&id); err != nil {
			return err
		}
		for _, n := range nodes {
			if _, err := tx.Exec(ctx, `INSERT INTO recall_items (recall_id, unit_id, shipment_id, customer_id) VALUES ($1, $2, $3, $4)`,
				id, n.UnitID, n.ShipmentID, n.CustomerID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE lots SET status = 'recalled' WHERE id = $1`, in.LotID); err != nil {
			return err
		}
		if err := audit(ctx, tx, "lot.recall", "lot", in.LotID, map[string]string{"status": status},
			map[string]any{"status": "recalled", "recall_id": id, "reason": in.Reason}); err != nil {
			return err
		}
		out = map[string]any{"id": id, "code": recallCode, "lot_id": in.LotID, "affected_units": len(nodes),
			"affected_customers": customers, "units": nodes, "query_ms": float64(elapsed.Microseconds()) / 1000}
		return nil
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, out)
	return nil
}

type recallRow struct {
	ID                int64     `json:"id"`
	Code              string    `json:"code"`
	LotID             int64     `json:"lot_id"`
	LotCode           string    `json:"lot_code"`
	Reason            string    `json:"reason"`
	InitiatedAt       time.Time `json:"initiated_at"`
	AffectedUnits     int       `json:"affected_units"`
	AffectedCustomers int       `json:"affected_customers"`
	QueryMS           int       `json:"query_ms"`
}

const recallSelect = `SELECT r.id, r.code, r.lot_id, l.code, r.reason, r.initiated_at, r.affected_units, r.affected_customers, r.query_ms
	FROM recalls r JOIN lots l ON l.id = r.lot_id`

func (s *Server) listRecalls(w http.ResponseWriter, r *http.Request) error {
	page, err := httpx.ParsePage(r, 50, 200)
	if err != nil {
		return err
	}
	rows, err := s.pool.Query(r.Context(), recallSelect+` WHERE r.id < $1 ORDER BY r.id DESC LIMIT $2`, page.MaxID(), page.Limit)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[recallRow])
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(items, page, func(x recallRow) int64 { return x.ID }))
	return nil
}

func (s *Server) getRecall(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	rows, err := s.pool.Query(r.Context(), recallSelect+` WHERE r.id = $1`, id)
	if err != nil {
		return err
	}
	rc, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[recallRow])
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	type item struct {
		UnitID     int64   `json:"unit_id"`
		UnitCode   string  `json:"unit_code"`
		Shipment   *string `json:"shipment"`
		CustomerID *int64  `json:"customer_id"`
		Customer   *string `json:"customer"`
	}
	irows, err := s.pool.Query(r.Context(), `SELECT ri.unit_id, u.code, sh.code, ri.customer_id, c.name FROM recall_items ri
		JOIN units u ON u.id = ri.unit_id LEFT JOIN shipments sh ON sh.id = ri.shipment_id LEFT JOIN customers c ON c.id = ri.customer_id
		WHERE ri.recall_id = $1 ORDER BY ri.unit_id`, id)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(irows, pgx.RowToStructByPos[item])
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"recall": rc, "items": items})
	return nil
}

// ---- customer portal ----

func (s *Server) customerShipments(w http.ResponseWriter, r *http.Request) error {
	p := principal(r.Context())
	page, err := httpx.ParsePage(r, 50, 200)
	if err != nil {
		return err
	}
	rows, err := s.pool.Query(r.Context(), shipmentSelect+`
		WHERE sh.customer_id = $1 AND sh.id < $2 AND sh.status IN ('in_transit', 'delivered')
		ORDER BY sh.id DESC LIMIT $3`, p.CustomerID, page.MaxID(), page.Limit)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[shipmentRow])
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(items, page, func(x shipmentRow) int64 { return x.ID }))
	return nil
}

// customerShipment is the temperature certificate of one delivery. Another
// customer's shipment answers 404, not 403, so ids cannot be probed.
func (s *Server) customerShipment(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	ctx := r.Context()
	sh, err := s.loadShipmentRow(ctx, s.pool, id)
	if err != nil {
		return err
	}
	if sh.CustomerID != principal(ctx).CustomerID || (sh.Status != "in_transit" && sh.Status != "delivered") {
		return httpx.ErrNotFound
	}
	units, err := s.queryUnits(ctx, `WHERE u.id IN (SELECT unit_id FROM shipment_items WHERE shipment_id = $1)`, id)
	if err != nil {
		return err
	}
	type cert struct {
		UnitCode string           `json:"unit_code"`
		LotCode  string           `json:"lot_code"`
		Product  string           `json:"product"`
		Quantity int              `json:"quantity"`
		Limits   [2]float64       `json:"limits_c"`
		Chain    ExcursionSummary `json:"cold_chain"`
		Verdict  string           `json:"verdict"`
	}
	var certs []cert
	now := s.now()
	for _, u := range units {
		for _, c := range u.Contents {
			product, err := loadProduct(ctx, s.pool, c.ProductID)
			if err != nil {
				return err
			}
			exps, err := unitHistory(ctx, s.pool, u.ID, c.LotID, now)
			if err != nil {
				return err
			}
			rs, err := s.exposureReadings(ctx, s.pool, exps)
			if err != nil {
				return err
			}
			res := excursion.Evaluate(rs, product.Rules(), measuredUntil(exps, now))
			verdict := "compliant"
			if len(res.Breaches) > 0 {
				verdict = "deviation"
			}
			certs = append(certs, cert{UnitCode: u.Code, LotCode: c.LotCode, Product: c.Product, Quantity: c.Quantity,
				Limits: [2]float64{float64(product.TempMinCC) / 100, float64(product.TempMaxCC) / 100},
				Chain:  summarise(res, product), Verdict: verdict})
		}
	}
	if certs == nil {
		certs = []cert{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"shipment": sh, "certificates": certs, "issued_at": now})
	return nil
}
