package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/AhmetYorat/soguk-zincir/backend/internal/db"
	"github.com/AhmetYorat/soguk-zincir/backend/internal/httpx"
)

// ---- products ----

func (s *Server) listProducts(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.pool.Query(r.Context(), `SELECT `+productCols+` FROM products ORDER BY sku`)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Product])
	if err != nil {
		return err
	}
	if items == nil {
		items = []Product{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

func (s *Server) getProduct(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	p, err := loadProduct(r.Context(), s.pool, id)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, p)
	return nil
}

func (s *Server) createProduct(w http.ResponseWriter, r *http.Request) error {
	var in Product
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	v := httpx.Validation{}
	v.Check(len(in.SKU) >= 2 && len(in.SKU) <= 40, "sku", "must be 2-40 characters")
	v.Check(strings.TrimSpace(in.Name) != "", "name", "is required")
	v.Check(in.TempMinCC < in.TempMaxCC, "temp_min_cc", "must be below temp_max_cc")
	v.Check(in.TempMinCC >= -6000 && in.TempMaxCC <= 8000, "temp_max_cc", "limits must be within -60..80 °C")
	v.Check(in.WindowS > 0, "window_s", "must be positive")
	v.Check(in.WindowMaxExcursionS >= 0 && in.WindowMaxExcursionS <= in.WindowS, "window_max_excursion_s", "must be between 0 and window_s")
	v.Check(in.MaxCumulativeExcursionS >= 0, "max_cumulative_excursion_s", "must not be negative")
	v.Check(in.ShelfLifeH > 0, "shelf_life_h", "must be positive")
	v.Check(in.UnitWeightG > 0, "unit_weight_g", "must be positive")
	v.Check(in.ListPriceKurus >= 0, "list_price_kurus", "must not be negative")
	if in.GapPolicy == "" {
		in.GapPolicy = "uncertain"
	}
	v.Check(in.GapPolicy == "uncertain" || in.GapPolicy == "interpolate", "gap_policy", "must be uncertain or interpolate")
	if in.MaxGapS == 0 {
		in.MaxGapS = 120
	}
	if in.ActivationEnergyJMol == 0 {
		in.ActivationEnergyJMol = 83144
	}
	v.Check(in.MaxGapS > 0, "max_gap_s", "must be positive")
	if err := v.Err(); err != nil {
		return err
	}
	return db.InTx(r.Context(), s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(r.Context(), `
			INSERT INTO products (sku, name, temp_min_cc, temp_max_cc, max_cumulative_excursion_s, window_s,
				window_max_excursion_s, shelf_life_h, activation_energy_j_mol, gap_policy, max_gap_s, unit_weight_g, list_price_kurus)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id, created_at`,
			in.SKU, in.Name, in.TempMinCC, in.TempMaxCC, in.MaxCumulativeExcursionS, in.WindowS, in.WindowMaxExcursionS,
			in.ShelfLifeH, in.ActivationEnergyJMol, in.GapPolicy, in.MaxGapS, in.UnitWeightG, in.ListPriceKurus).
			Scan(&in.ID, &in.CreatedAt)
		if db.IsUniqueViolation(err, "") {
			return httpx.Errorf(http.StatusConflict, "duplicate_sku", "a product with this SKU already exists")
		}
		if err != nil {
			return err
		}
		if err := audit(r.Context(), tx, "product.create", "product", in.ID, nil, in); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, in)
		return nil
	})
}

// ---- lots ----

type lotRow struct {
	ID                  int64      `json:"id"`
	Code                string     `json:"code"`
	ProductID           int64      `json:"product_id"`
	ProductSKU          string     `json:"product_sku"`
	ProductName         string     `json:"product_name"`
	ProducedAt          time.Time  `json:"produced_at"`
	Quantity            int        `json:"quantity"`
	Status              string     `json:"status"`
	DiscountPct         int16      `json:"discount_pct"`
	RemainingShelfLifeS *int64     `json:"remaining_shelf_life_s"`
	ShelfLifeComputedAt *time.Time `json:"shelf_life_computed_at"`
	OnHand              int64      `json:"on_hand"`
}

const lotSelect = `
	SELECT l.id, l.code, l.product_id, p.sku, p.name, l.produced_at, l.quantity, l.status::text, l.discount_pct,
	       l.remaining_shelf_life_s, l.shelf_life_computed_at,
	       coalesce((SELECT sum(c.quantity) FROM unit_contents c JOIN units u ON u.id = c.unit_id
	                 WHERE c.lot_id = l.id AND u.status = 'active'), 0)
	FROM lots l JOIN products p ON p.id = l.product_id`

func (s *Server) listLots(w http.ResponseWriter, r *http.Request) error {
	page, err := httpx.ParsePage(r, 50, 200)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	rows, err := s.pool.Query(r.Context(), lotSelect+`
		WHERE l.id < $1 AND ($2 = '' OR l.status::text = $2) AND ($3 = '' OR l.code ILIKE '%' || $3 || '%' OR p.name ILIKE '%' || $3 || '%')
		  AND ($4 = '' OR l.product_id::text = $4)
		ORDER BY l.id DESC LIMIT $5`, page.MaxID(), q.Get("status"), q.Get("q"), q.Get("product_id"), page.Limit)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[lotRow])
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(items, page, func(l lotRow) int64 { return l.ID }))
	return nil
}

func (s *Server) getLot(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	rows, err := s.pool.Query(r.Context(), lotSelect+` WHERE l.id = $1`, id)
	if err != nil {
		return err
	}
	lot, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[lotRow])
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	units, err := s.queryUnits(r.Context(), `WHERE u.id IN (SELECT unit_id FROM unit_contents WHERE lot_id = $1 AND quantity > 0)`, id)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"lot": lot, "units": units})
	return nil
}

func (s *Server) lotEvaluation(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	ev, err := s.evaluateLot(r.Context(), s.pool, id)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, ev)
	return nil
}

// createLot books a received lot onto one or more new pallets at a cold room.
func (s *Server) createLot(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Code       string    `json:"code"`
		ProductID  int64     `json:"product_id"`
		ProducedAt time.Time `json:"produced_at"`
		LocationID int64     `json:"location_id"`
		Pallets    []int     `json:"pallets"` // quantity per pallet
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	v := httpx.Validation{}
	v.Check(len(in.Code) >= 3 && len(in.Code) <= 40, "code", "must be 3-40 characters")
	v.Check(in.ProductID > 0, "product_id", "is required")
	v.Check(in.LocationID > 0, "location_id", "is required")
	v.Check(!in.ProducedAt.IsZero() && !in.ProducedAt.After(s.now()), "produced_at", "must be a past timestamp")
	v.Check(len(in.Pallets) >= 1 && len(in.Pallets) <= 66, "pallets", "must list 1-66 pallet quantities")
	total := 0
	for _, q := range in.Pallets {
		v.Check(q > 0, "pallets", "quantities must be positive")
		total += q
	}
	if err := v.Err(); err != nil {
		return err
	}
	ctx := r.Context()
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var kind string
		err := tx.QueryRow(ctx, `SELECT kind::text FROM locations WHERE id = $1`, in.LocationID).Scan(&kind)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && kind != "cold_room") {
			return httpx.Validation{"location_id": "must be a cold room"}.Err()
		}
		if err != nil {
			return err
		}
		var lotID int64
		err = tx.QueryRow(ctx, `INSERT INTO lots (code, product_id, produced_at, quantity) VALUES ($1,$2,$3,$4) RETURNING id`,
			in.Code, in.ProductID, in.ProducedAt, total).Scan(&lotID)
		switch {
		case db.IsUniqueViolation(err, ""):
			return httpx.Errorf(http.StatusConflict, "duplicate_lot", "a lot with this code already exists")
		case db.IsConstraintViolation(err):
			return httpx.Validation{"product_id": "unknown product"}.Err()
		case err != nil:
			return err
		}
		now := s.now()
		var unitIDs []int64
		for i, q := range in.Pallets {
			id, err := createUnit(ctx, tx, fmt.Sprintf("P-%s-%02d", in.Code, i+1), in.LocationID, now)
			if err != nil {
				return err
			}
			if err := addContents(ctx, tx, id, lotID, q); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO unit_lineage (parent_unit_id, child_unit_id, op, lot_id, quantity, at)
				VALUES (NULL, $1, 'receive', $2, $3, $4)`, id, lotID, q, now); err != nil {
				return err
			}
			unitIDs = append(unitIDs, id)
		}
		if err := audit(ctx, tx, "lot.create", "lot", lotID, nil, in); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"id": lotID, "code": in.Code, "quantity": total, "unit_ids": unitIDs})
		return nil
	})
}

func createUnit(ctx context.Context, q Querier, code string, locationID int64, at time.Time) (int64, error) {
	var id int64
	if err := q.QueryRow(ctx, `INSERT INTO units (code, location_id, created_at) VALUES ($1, $2, $3) RETURNING id`,
		code, locationID, at).Scan(&id); err != nil {
		return 0, err
	}
	_, err := q.Exec(ctx, `INSERT INTO custody_events (unit_id, location_id, from_ts) VALUES ($1, $2, $3)`, id, locationID, at)
	return id, err
}

func addContents(ctx context.Context, q Querier, unitID, lotID int64, qty int) error {
	_, err := q.Exec(ctx, `
		INSERT INTO unit_contents (unit_id, lot_id, quantity) VALUES ($1, $2, $3)
		ON CONFLICT (unit_id, lot_id) DO UPDATE SET quantity = unit_contents.quantity + EXCLUDED.quantity`, unitID, lotID, qty)
	return err
}

// moveCustody closes the open custody interval and opens a new one.
func moveCustody(ctx context.Context, q Querier, unitID, locationID int64, at time.Time) error {
	if _, err := q.Exec(ctx, `UPDATE custody_events SET to_ts = $2 WHERE unit_id = $1 AND to_ts IS NULL`, unitID, at); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `UPDATE units SET location_id = $2 WHERE id = $1`, unitID, locationID); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `INSERT INTO custody_events (unit_id, location_id, from_ts) VALUES ($1, $2, $3)`, unitID, locationID, at)
	return err
}

// ---- units ----

// Content is one lot on a unit.
type Content struct {
	LotID     int64  `json:"lot_id"`
	LotCode   string `json:"lot_code"`
	ProductID int64  `json:"product_id"`
	Product   string `json:"product"`
	Quantity  int    `json:"quantity"`
	LotStatus string `json:"lot_status"`
}

// Unit is a pallet with its contents.
type Unit struct {
	ID           int64     `json:"id"`
	Code         string    `json:"code"`
	Status       string    `json:"status"`
	LocationID   int64     `json:"location_id"`
	LocationCode string    `json:"location_code"`
	LocationName string    `json:"location_name"`
	LocationKind string    `json:"location_kind"`
	CreatedAt    time.Time `json:"created_at"`
	Contents     []Content `json:"contents"`
}

func (s *Server) queryUnits(ctx context.Context, where string, args ...any) ([]Unit, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.code, u.status::text, u.location_id, l.code, l.name, l.kind::text, u.created_at,
		       coalesce(json_agg(json_build_object('lot_id', c.lot_id, 'lot_code', lo.code, 'product_id', p.id,
		           'product', p.name, 'quantity', c.quantity, 'lot_status', lo.status) ORDER BY c.lot_id)
		           FILTER (WHERE c.quantity > 0), '[]')
		FROM units u
		JOIN locations l ON l.id = u.location_id
		LEFT JOIN unit_contents c ON c.unit_id = u.id
		LEFT JOIN lots lo ON lo.id = c.lot_id
		LEFT JOIN products p ON p.id = lo.product_id
		`+where+`
		GROUP BY u.id, l.id ORDER BY u.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	units, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Unit])
	if units == nil {
		units = []Unit{}
	}
	return units, err
}

func (s *Server) listUnits(w http.ResponseWriter, r *http.Request) error {
	page, err := httpx.ParsePage(r, 50, 200)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	units, err := s.queryUnits(r.Context(), `
		WHERE u.id IN (SELECT u2.id FROM units u2
		    WHERE u2.id < $1 AND ($2 = '' OR u2.status::text = $2) AND ($3 = '' OR u2.location_id::text = $3)
		      AND ($4 = '' OR u2.id IN (SELECT unit_id FROM unit_contents WHERE lot_id::text = $4 AND quantity > 0))
		      AND ($5 = '' OR u2.code ILIKE '%' || $5 || '%')
		    ORDER BY u2.id DESC LIMIT $6)`,
		page.MaxID(), q.Get("status"), q.Get("location_id"), q.Get("lot_id"), q.Get("q"), page.Limit)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(units, page, func(u Unit) int64 { return u.ID }))
	return nil
}

func (s *Server) getUnit(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	units, err := s.queryUnits(r.Context(), `WHERE u.id = $1`, id)
	if err != nil {
		return err
	}
	if len(units) == 0 {
		return httpx.ErrNotFound
	}
	httpx.JSON(w, http.StatusOK, units[0])
	return nil
}

var errUnitNotActive = httpx.Errorf(http.StatusConflict, "unit_not_active", "unit is not active at a cold room (already loaded, delivered or consumed)")

// lockActiveUnit locks the unit row so concurrent split/merge/load
// operations on the same pallet are serialised.
func lockActiveUnit(ctx context.Context, tx pgx.Tx, id int64) (locationID int64, err error) {
	var status string
	err = tx.QueryRow(ctx, `SELECT status::text, location_id FROM units WHERE id = $1 FOR UPDATE`, id).Scan(&status, &locationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, httpx.ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if status != "active" {
		return 0, errUnitNotActive
	}
	return locationID, nil
}

// splitUnit moves `quantity` of a lot from a pallet onto a new pallet.
func (s *Server) splitUnit(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	var in struct {
		LotID    int64 `json:"lot_id"`
		Quantity int   `json:"quantity"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Quantity <= 0 {
		return httpx.Validation{"quantity": "must be positive"}.Err()
	}
	ctx := r.Context()
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		loc, err := lockActiveUnit(ctx, tx, id)
		if err != nil {
			return err
		}
		if in.LotID == 0 {
			// single-lot pallets do not need the lot spelled out
			rows, err := tx.Query(ctx, `SELECT lot_id FROM unit_contents WHERE unit_id = $1 AND quantity > 0`, id)
			if err != nil {
				return err
			}
			lots, err := pgx.CollectRows(rows, pgx.RowTo[int64])
			if err != nil {
				return err
			}
			if len(lots) != 1 {
				return httpx.Validation{"lot_id": "required for pallets with several lots"}.Err()
			}
			in.LotID = lots[0]
		}
		// conditional update: never lets the quantity go negative
		tag, err := tx.Exec(ctx, `UPDATE unit_contents SET quantity = quantity - $3
			WHERE unit_id = $1 AND lot_id = $2 AND quantity > $3`, id, in.LotID, in.Quantity)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.Errorf(http.StatusConflict, "insufficient_quantity",
				"pallet does not hold more than %d units of this lot; split must leave something behind", in.Quantity)
		}
		now := s.now()
		var parentCode string
		if err := tx.QueryRow(ctx, `SELECT code FROM units WHERE id = $1`, id).Scan(&parentCode); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM unit_lineage WHERE parent_unit_id = $1`, id).Scan(&n); err != nil {
			return err
		}
		child, err := createUnit(ctx, tx, fmt.Sprintf("%s.%d", parentCode, n+1), loc, now)
		if err != nil {
			return err
		}
		if err := addContents(ctx, tx, child, in.LotID, in.Quantity); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO unit_lineage (parent_unit_id, child_unit_id, op, lot_id, quantity, at)
			VALUES ($1, $2, 'split', $3, $4, $5)`, id, child, in.LotID, in.Quantity, now); err != nil {
			return err
		}
		if err := audit(ctx, tx, "unit.split", "unit", id, nil, map[string]any{"child_unit_id": child, "lot_id": in.LotID, "quantity": in.Quantity}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"parent_unit_id": id, "child_unit_id": child, "lot_id": in.LotID, "quantity": in.Quantity})
		return nil
	})
}

// mergeUnits combines pallets at the same location into a new pallet.
func (s *Server) mergeUnits(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		UnitIDs []int64 `json:"unit_ids"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, id := range in.UnitIDs {
		seen[id] = true
	}
	if len(in.UnitIDs) < 2 || len(seen) != len(in.UnitIDs) || len(in.UnitIDs) > 20 {
		return httpx.Validation{"unit_ids": "must list 2-20 distinct units"}.Err()
	}
	ctx := r.Context()
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		// lock in id order so two merges over overlapping sets cannot deadlock
		ids := slices.Sorted(slices.Values(in.UnitIDs))
		var loc int64
		for i, id := range ids {
			l, err := lockActiveUnit(ctx, tx, id)
			if err != nil {
				return err
			}
			if i > 0 && l != loc {
				return httpx.Errorf(http.StatusConflict, "different_locations", "units must be at the same location to merge")
			}
			loc = l
		}
		now := s.now()
		child, err := createUnit(ctx, tx, fmt.Sprintf("M-%d-%d", ids[0], now.UnixMilli()%100000), loc, now)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT unit_id, lot_id, quantity FROM unit_contents WHERE unit_id = ANY($1) AND quantity > 0`, ids)
		if err != nil {
			return err
		}
		type part struct {
			unit, lot int64
			qty       int
		}
		parts, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (part, error) {
			var p part
			return p, r.Scan(&p.unit, &p.lot, &p.qty)
		})
		if err != nil {
			return err
		}
		for _, p := range parts {
			if err := addContents(ctx, tx, child, p.lot, p.qty); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO unit_lineage (parent_unit_id, child_unit_id, op, lot_id, quantity, at)
				VALUES ($1, $2, 'merge', $3, $4, $5)`, p.unit, child, p.lot, p.qty, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE unit_contents SET quantity = 0 WHERE unit_id = ANY($1)`, ids); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE units SET status = 'consumed' WHERE id = ANY($1)`, ids); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE custody_events SET to_ts = $2 WHERE unit_id = ANY($1) AND to_ts IS NULL`, ids, now); err != nil {
			return err
		}
		if err := audit(ctx, tx, "unit.merge", "unit", child, map[string]any{"parents": ids}, map[string]any{"unit_id": child}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"unit_id": child, "parents": ids})
		return nil
	})
}

func (s *Server) moveUnit(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	var in struct {
		LocationID int64 `json:"location_id"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		from, err := lockActiveUnit(ctx, tx, id)
		if err != nil {
			return err
		}
		var kind string
		err = tx.QueryRow(ctx, `SELECT kind::text FROM locations WHERE id = $1`, in.LocationID).Scan(&kind)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && kind != "cold_room") {
			// vehicles are entered through shipment loading, customers through delivery
			return httpx.Validation{"location_id": "must be a cold room"}.Err()
		}
		if err != nil {
			return err
		}
		if from == in.LocationID {
			return httpx.Errorf(http.StatusConflict, "same_location", "unit is already there")
		}
		if err := moveCustody(ctx, tx, id, in.LocationID, s.now()); err != nil {
			return err
		}
		if err := audit(ctx, tx, "unit.move", "unit", id, map[string]any{"location_id": from}, map[string]any{"location_id": in.LocationID}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"unit_id": id, "location_id": in.LocationID})
		return nil
	})
}

// ---- locations and vehicles ----

func (s *Server) listLocations(w http.ResponseWriter, r *http.Request) error {
	type loc struct {
		ID          int64   `json:"id"`
		Code        string  `json:"code"`
		Name        string  `json:"name"`
		Kind        string  `json:"kind"`
		Warehouse   *string `json:"warehouse"`
		VehicleID   *int64  `json:"vehicle_id"`
		CustomerID  *int64  `json:"customer_id"`
		Sensors     int     `json:"sensors"`
		ActiveUnits int     `json:"active_units"`
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT l.id, l.code, l.name, l.kind::text, w.name, l.vehicle_id, l.customer_id,
		       (SELECT count(*) FROM sensors s WHERE s.location_id = l.id),
		       (SELECT count(*) FROM units u WHERE u.location_id = l.id AND u.status IN ('active', 'loaded'))
		FROM locations l LEFT JOIN warehouses w ON w.id = l.warehouse_id
		WHERE ($1 = '' OR l.kind::text = $1)
		ORDER BY l.kind, l.code`, r.URL.Query().Get("kind"))
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[loc])
	if err != nil {
		return err
	}
	if items == nil {
		items = []loc{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

func (s *Server) listVehicles(w http.ResponseWriter, r *http.Request) error {
	type veh struct {
		ID              int64   `json:"id"`
		Plate           string  `json:"plate"`
		CapacityKG      int     `json:"capacity_kg"`
		CapacityPallets int     `json:"capacity_pallets"`
		DriverID        *int64  `json:"driver_id"`
		Driver          *string `json:"driver"`
		LocationID      int64   `json:"location_id"`
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT v.id, v.plate, v.capacity_kg, v.capacity_pallets, v.driver_id, u.full_name, l.id
		FROM vehicles v JOIN locations l ON l.vehicle_id = v.id LEFT JOIN users u ON u.id = v.driver_id
		ORDER BY v.plate`)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[veh])
	if err != nil {
		return err
	}
	if items == nil {
		items = []veh{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}
