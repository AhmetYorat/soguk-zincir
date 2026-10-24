package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/db"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/domain/loadplan"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/httpx"
)

// DefaultSafetyMargin is the shelf life a market must still receive on top of
// the transit time.
const DefaultSafetyMargin = 24 * time.Hour

type shipmentRow struct {
	ID               int64      `json:"id"`
	Code             string     `json:"code"`
	Status           string     `json:"status"`
	VehicleID        int64      `json:"vehicle_id"`
	Plate            string     `json:"plate"`
	CustomerID       int64      `json:"customer_id"`
	Customer         string     `json:"customer"`
	OriginID         int64      `json:"origin_id"`
	Origin           string     `json:"origin"`
	PlannedDeparture time.Time  `json:"planned_departure"`
	TransitS         int        `json:"transit_s"`
	DepartedAt       *time.Time `json:"departed_at"`
	DeliveredAt      *time.Time `json:"delivered_at"`
	Pallets          int        `json:"pallets"`
	CreatedAt        time.Time  `json:"created_at"`
}

const shipmentSelect = `
	SELECT sh.id, sh.code, sh.status::text, sh.vehicle_id, v.plate, sh.customer_id, c.name, sh.origin_id, o.name,
	       sh.planned_departure, sh.transit_s, sh.departed_at, sh.delivered_at,
	       (SELECT count(*) FROM shipment_items si WHERE si.shipment_id = sh.id)::int, sh.created_at
	FROM shipments sh
	JOIN vehicles v ON v.id = sh.vehicle_id
	JOIN customers c ON c.id = sh.customer_id
	JOIN locations o ON o.id = sh.origin_id`

func (s *Server) listShipments(w http.ResponseWriter, r *http.Request) error {
	page, err := httpx.ParsePage(r, 50, 200)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	rows, err := s.pool.Query(r.Context(), shipmentSelect+`
		WHERE sh.id < $1 AND ($2 = '' OR sh.status::text = $2) AND ($3 = '' OR sh.customer_id::text = $3)
		ORDER BY sh.id DESC LIMIT $4`, page.MaxID(), q.Get("status"), q.Get("customer_id"), page.Limit)
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

func (s *Server) loadShipmentRow(ctx context.Context, q Querier, id int64) (shipmentRow, error) {
	rows, err := q.Query(ctx, shipmentSelect+` WHERE sh.id = $1`, id)
	if err != nil {
		return shipmentRow{}, err
	}
	sh, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[shipmentRow])
	if errors.Is(err, pgx.ErrNoRows) {
		return sh, httpx.ErrNotFound
	}
	return sh, err
}

func (s *Server) getShipment(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	sh, err := s.loadShipmentRow(r.Context(), s.pool, id)
	if err != nil {
		return err
	}
	units, err := s.queryUnits(r.Context(), `WHERE u.id IN (SELECT unit_id FROM shipment_items WHERE shipment_id = $1)`, id)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"shipment": sh, "units": units})
	return nil
}

func (s *Server) createShipment(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		VehicleID        int64     `json:"vehicle_id"`
		CustomerID       int64     `json:"customer_id"`
		OriginID         int64     `json:"origin_id"`
		PlannedDeparture time.Time `json:"planned_departure"`
		TransitMinutes   int       `json:"transit_minutes"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	v := httpx.Validation{}
	v.Check(in.TransitMinutes > 0 && in.TransitMinutes <= 72*60, "transit_minutes", "must be 1-4320")
	v.Check(!in.PlannedDeparture.IsZero(), "planned_departure", "is required")
	if err := v.Err(); err != nil {
		return err
	}
	ctx := r.Context()
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var kind string
		if err := tx.QueryRow(ctx, `SELECT kind::text FROM locations WHERE id = $1`, in.OriginID).Scan(&kind); err != nil || kind != "cold_room" {
			return httpx.Validation{"origin_id": "must be a cold room"}.Err()
		}
		var id int64
		code := fmt.Sprintf("SHP-%s-%s", s.now().Format("060102"), uuid.NewString()[:6])
		err := tx.QueryRow(ctx, `INSERT INTO shipments (code, vehicle_id, customer_id, origin_id, planned_departure, transit_s, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			code, in.VehicleID, in.CustomerID, in.OriginID, in.PlannedDeparture, in.TransitMinutes*60, principal(ctx).UserID).Scan(&id)
		if db.IsConstraintViolation(err) {
			return httpx.Validation{"vehicle_id": "unknown vehicle or customer"}.Err()
		}
		if err != nil {
			return err
		}
		if err := audit(ctx, tx, "shipment.create", "shipment", id, nil, in); err != nil {
			return err
		}
		sh, err := s.loadShipmentRow(ctx, tx, id)
		if err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, sh)
		return nil
	})
}

// candidate is a pallet at the origin with exactly one product on it.
type candidate struct {
	loadplan.Candidate
	ProductID   int64
	LotStatus   string
	UnitWeightG int
}

// lotShelfLife returns the remaining shelf life of each lot now, using the
// cached evaluator result aged by the time since it was computed; lots never
// evaluated are evaluated on the spot.
func (s *Server) lotShelfLife(ctx context.Context, q Querier, lotIDs []int64) (map[int64]time.Duration, error) {
	out := map[int64]time.Duration{}
	rows, err := q.Query(ctx, `SELECT id, remaining_shelf_life_s, shelf_life_computed_at FROM lots WHERE id = ANY($1)`, lotIDs)
	if err != nil {
		return nil, err
	}
	type cached struct {
		id  int64
		rem *int64
		at  *time.Time
	}
	cs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (cached, error) {
		var c cached
		return c, r.Scan(&c.id, &c.rem, &c.at)
	})
	if err != nil {
		return nil, err
	}
	now := s.now()
	for _, c := range cs {
		if c.rem != nil && c.at != nil && now.Sub(*c.at) < 30*time.Minute {
			out[c.id] = time.Duration(*c.rem)*time.Second - now.Sub(*c.at)
			continue
		}
		ev, err := s.evaluateLot(ctx, q, c.id)
		if err != nil {
			return nil, err
		}
		if ev.Worst != nil {
			out[c.id] = ev.Worst.estimate.Remaining
		} else {
			out[c.id] = time.Duration(ev.Product.ShelfLifeH)*time.Hour - now.Sub(ev.ProducedAt)
		}
	}
	return out, nil
}

func (s *Server) candidates(ctx context.Context, q Querier, originID int64, productIDs []int64) ([]candidate, error) {
	rows, err := q.Query(ctx, `
		SELECT u.id, u.code, lo.id, lo.code, c.quantity, lo.product_id, lo.status::text, p.unit_weight_g
		FROM units u
		JOIN unit_contents c ON c.unit_id = u.id AND c.quantity > 0
		JOIN lots lo ON lo.id = c.lot_id
		JOIN products p ON p.id = lo.product_id
		WHERE u.location_id = $1 AND u.status = 'active' AND lo.product_id = ANY($2)
		  AND NOT EXISTS (SELECT 1 FROM unit_contents c2 WHERE c2.unit_id = u.id AND c2.quantity > 0 AND c2.lot_id <> c.lot_id)
		ORDER BY u.id`, originID, productIDs)
	if err != nil {
		return nil, err
	}
	cs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (candidate, error) {
		var c candidate
		return c, r.Scan(&c.UnitID, &c.UnitCode, &c.LotID, &c.LotCode, &c.Quantity, &c.ProductID, &c.LotStatus, &c.UnitWeightG)
	})
	if err != nil || len(cs) == 0 {
		return cs, err
	}
	var lotIDs []int64
	for _, c := range cs {
		lotIDs = append(lotIDs, c.LotID)
	}
	rem, err := s.lotShelfLife(ctx, q, lotIDs)
	if err != nil {
		return nil, err
	}
	now := s.now()
	for i := range cs {
		cs[i].Remaining = rem[cs[i].LotID]
		cs[i].ExpiresAt = now.Add(cs[i].Remaining)
	}
	return cs, nil
}

// shippable lot statuses: on_hold, destroyed and recalled lots never leave.
var shippable = map[string]bool{"released": true, "accepted": true, "discounted": true}

type capacity struct{ kg, pallets int }

func remainingCapacity(ctx context.Context, q Querier, shipmentID, vehicleID int64) (capacity, error) {
	var c capacity
	var usedG int64
	var usedPallets int
	err := q.QueryRow(ctx, `
		SELECT v.capacity_kg, v.capacity_pallets,
		       coalesce((SELECT sum(uc.quantity * p.unit_weight_g) FROM shipment_items si
		                 JOIN unit_contents uc ON uc.unit_id = si.unit_id JOIN lots lo ON lo.id = uc.lot_id
		                 JOIN products p ON p.id = lo.product_id WHERE si.shipment_id = $1), 0),
		       (SELECT count(*) FROM shipment_items WHERE shipment_id = $1)::int
		FROM vehicles v WHERE v.id = $2`, shipmentID, vehicleID).Scan(&c.kg, &c.pallets, &usedG, &usedPallets)
	c.kg -= int((usedG + 999) / 1000)
	c.pallets -= usedPallets
	return c, err
}

// planShipment proposes pallets per order line (FEFO + knapsack). It does not
// change anything; loading is a separate, locked operation.
func (s *Server) planShipment(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	var in struct {
		Lines []struct {
			ProductID int64 `json:"product_id"`
			Quantity  int   `json:"quantity"`
		} `json:"lines"`
		SafetyMarginHours *float64 `json:"safety_margin_hours"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if len(in.Lines) == 0 || len(in.Lines) > 20 {
		return httpx.Validation{"lines": "must contain 1-20 lines"}.Err()
	}
	margin := DefaultSafetyMargin
	if in.SafetyMarginHours != nil {
		if *in.SafetyMarginHours < 0 || *in.SafetyMarginHours > 24*30 {
			return httpx.Validation{"safety_margin_hours": "must be 0-720"}.Err()
		}
		margin = time.Duration(*in.SafetyMarginHours * float64(time.Hour))
	}
	ctx := r.Context()
	sh, err := s.loadShipmentRow(ctx, s.pool, id)
	if err != nil {
		return err
	}
	if sh.Status != "planned" && sh.Status != "loading" {
		return httpx.Errorf(http.StatusConflict, "shipment_closed", "shipment is %s", sh.Status)
	}
	capLeft, err := remainingCapacity(ctx, s.pool, id, sh.VehicleID)
	if err != nil {
		return err
	}
	var productIDs []int64
	for _, l := range in.Lines {
		productIDs = append(productIDs, l.ProductID)
	}
	cands, err := s.candidates(ctx, s.pool, sh.OriginID, productIDs)
	if err != nil {
		return err
	}
	type linePlan struct {
		ProductID int64 `json:"product_id"`
		loadplan.Plan
	}
	var plans []linePlan
	used := map[int64]bool{}
	for _, l := range in.Lines {
		var pool []loadplan.Candidate
		var rejected []loadplan.Rejection
		unitWeight := 0
		for _, c := range cands {
			if c.ProductID != l.ProductID || used[c.UnitID] {
				continue
			}
			unitWeight = c.UnitWeightG
			if !shippable[c.LotStatus] {
				rejected = append(rejected, loadplan.Rejection{Candidate: c.Candidate, Reason: "lot status " + c.LotStatus})
				continue
			}
			pool = append(pool, c.Candidate)
		}
		if unitWeight == 0 {
			p, err := loadProduct(ctx, s.pool, l.ProductID)
			if err != nil {
				return err
			}
			unitWeight = p.UnitWeightG
		}
		plan, err := loadplan.Build(loadplan.Request{
			Demand: l.Quantity, UnitWeightG: unitWeight, CapacityKG: capLeft.kg, CapacityPallets: capLeft.pallets,
			Transit: time.Duration(sh.TransitS) * time.Second, SafetyMargin: margin,
		}, pool)
		if err != nil {
			return httpx.Validation{"lines": err.Error()}.Err()
		}
		plan.Rejected = append(plan.Rejected, rejected...)
		for _, c := range plan.Selected {
			used[c.UnitID] = true
		}
		capLeft.kg -= int(plan.WeightKG + 0.999)
		capLeft.pallets -= plan.Pallets
		if plan.Selected == nil {
			plan.Selected = []loadplan.Candidate{}
		}
		if plan.Rejected == nil {
			plan.Rejected = []loadplan.Rejection{}
		}
		plans = append(plans, linePlan{ProductID: l.ProductID, Plan: plan})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"shipment_id": id, "transit_s": sh.TransitS, "safety_margin_s": int(margin.Seconds()),
		"lines": plans, "capacity_left": map[string]int{"kg": capLeft.kg, "pallets": capLeft.pallets},
	})
	return nil
}

// loadShipment loads pallets onto the vehicle. Everything happens under row
// locks (vehicle, shipment, each unit) so that:
//   - a pallet cannot be loaded onto two shipments (unit lock + UNIQUE unit_id),
//   - two concurrent loads cannot together exceed the vehicle capacity,
//   - a pallet whose lot would expire in transit is refused even if the
//     client skipped the plan step.
func (s *Server) loadShipment(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	var in struct {
		UnitIDs           []int64  `json:"unit_ids"`
		SafetyMarginHours *float64 `json:"safety_margin_hours"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if len(in.UnitIDs) == 0 || len(in.UnitIDs) > 66 {
		return httpx.Validation{"unit_ids": "must list 1-66 units"}.Err()
	}
	margin := DefaultSafetyMargin
	if in.SafetyMarginHours != nil && *in.SafetyMarginHours >= 0 {
		margin = time.Duration(*in.SafetyMarginHours * float64(time.Hour))
	}
	ctx := r.Context()
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		var vehicleID, originID int64
		var transitS int
		err := tx.QueryRow(ctx, `SELECT status::text, vehicle_id, origin_id, transit_s FROM shipments WHERE id = $1 FOR UPDATE`, id).
			Scan(&status, &vehicleID, &originID, &transitS)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != "planned" && status != "loading" {
			return httpx.Errorf(http.StatusConflict, "shipment_closed", "shipment is %s", status)
		}
		var vehicleLoc int64
		if err := tx.QueryRow(ctx, `SELECT l.id FROM vehicles v JOIN locations l ON l.vehicle_id = v.id WHERE v.id = $1 FOR UPDATE OF v`, vehicleID).Scan(&vehicleLoc); err != nil {
			return err
		}
		var busy int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM shipments WHERE vehicle_id = $1 AND id <> $2 AND status IN ('loading', 'in_transit')`,
			vehicleID, id).Scan(&busy); err != nil {
			return err
		}
		if busy > 0 {
			return httpx.Errorf(http.StatusConflict, "vehicle_busy", "vehicle is already loading or on the road with another shipment")
		}
		ids := in.UnitIDs
		for _, uid := range ids {
			loc, err := lockActiveUnit(ctx, tx, uid)
			if err != nil {
				return httpx.Errorf(http.StatusConflict, "unit_unavailable", "unit %d: %s", uid, err.Error())
			}
			if loc != originID {
				return httpx.Errorf(http.StatusConflict, "wrong_location", "unit %d is not at the shipment origin", uid)
			}
		}
		// lot checks: status and shelf life against transit + margin
		rows, err := tx.Query(ctx, `SELECT DISTINCT lo.id, lo.code, lo.status::text FROM unit_contents c JOIN lots lo ON lo.id = c.lot_id
			WHERE c.unit_id = ANY($1) AND c.quantity > 0`, ids)
		if err != nil {
			return err
		}
		type lotInfo struct {
			id           int64
			code, status string
		}
		lots, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (lotInfo, error) {
			var l lotInfo
			return l, r.Scan(&l.id, &l.code, &l.status)
		})
		if err != nil {
			return err
		}
		var lotIDs []int64
		for _, l := range lots {
			if !shippable[l.status] {
				return httpx.Errorf(http.StatusConflict, "lot_not_shippable", "lot %s is %s", l.code, l.status)
			}
			lotIDs = append(lotIDs, l.id)
		}
		rem, err := s.lotShelfLife(ctx, tx, lotIDs)
		if err != nil {
			return err
		}
		need := time.Duration(transitS)*time.Second + margin
		for _, l := range lots {
			if rem[l.id] < need {
				return httpx.Errorf(http.StatusConflict, "shelf_life_too_short",
					"lot %s has %.1f h shelf life left; transit + margin needs %.1f h", l.code, rem[l.id].Hours(), need.Hours()).
					With("lot_id", l.id)
			}
		}
		capLeft, err := remainingCapacity(ctx, tx, id, vehicleID)
		if err != nil {
			return err
		}
		var addG int64
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(c.quantity * p.unit_weight_g), 0) FROM unit_contents c
			JOIN lots lo ON lo.id = c.lot_id JOIN products p ON p.id = lo.product_id WHERE c.unit_id = ANY($1)`, ids).Scan(&addG); err != nil {
			return err
		}
		if int((addG+999)/1000) > capLeft.kg || len(ids) > capLeft.pallets {
			return httpx.Errorf(http.StatusConflict, "capacity_exceeded",
				"vehicle has %d kg / %d pallets left, request needs %d kg / %d pallets", capLeft.kg, capLeft.pallets, (addG+999)/1000, len(ids))
		}
		now := s.now()
		for _, uid := range ids {
			if _, err := tx.Exec(ctx, `INSERT INTO shipment_items (shipment_id, unit_id, loaded_by, loaded_at) VALUES ($1, $2, $3, $4)`,
				id, uid, principal(ctx).UserID, now); err != nil {
				if db.IsUniqueViolation(err, "") {
					return httpx.Errorf(http.StatusConflict, "already_loaded", "unit %d is already on a shipment", uid)
				}
				return err
			}
			if err := moveCustody(ctx, tx, uid, vehicleLoc, now); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE units SET status = 'loaded' WHERE id = $1`, uid); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE shipments SET status = 'loading' WHERE id = $1`, id); err != nil {
			return err
		}
		if err := audit(ctx, tx, "shipment.load", "shipment", id, nil, map[string]any{"unit_ids": ids}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"shipment_id": id, "loaded": ids, "capacity_left": map[string]int{
			"kg": capLeft.kg - int((addG+999)/1000), "pallets": capLeft.pallets - len(ids)}})
		return nil
	})
}

func (s *Server) departShipment(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	ctx := r.Context()
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE shipments SET status = 'in_transit', departed_at = $2 WHERE id = $1 AND status = 'loading'`, id, s.now())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.Errorf(http.StatusConflict, "invalid_transition", "only a shipment that is loading can depart")
		}
		if err := audit(ctx, tx, "shipment.depart", "shipment", id, map[string]string{"status": "loading"}, map[string]string{"status": "in_transit"}); err != nil {
			return err
		}
		sh, err := s.loadShipmentRow(ctx, tx, id)
		if err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, sh)
		return nil
	})
}

// ---- driver app ----

func (s *Server) driverShipments(w http.ResponseWriter, r *http.Request) error {
	p := principal(r.Context())
	rows, err := s.pool.Query(r.Context(), shipmentSelect+`
		WHERE v.driver_id = $1 AND (sh.status IN ('loading', 'in_transit') OR sh.delivered_at > now() - interval '2 days')
		ORDER BY (sh.status = 'in_transit') DESC, sh.planned_departure DESC LIMIT 50`, p.UserID)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[shipmentRow])
	if err != nil {
		return err
	}
	type withUnits struct {
		shipmentRow
		Units []Unit `json:"units"`
	}
	out := []withUnits{}
	for _, sh := range items {
		units, err := s.queryUnits(r.Context(), `WHERE u.id IN (SELECT unit_id FROM shipment_items WHERE shipment_id = $1)`, sh.ID)
		if err != nil {
			return err
		}
		out = append(out, withUnits{sh, units})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
	return nil
}

type driverEvent struct {
	ClientEventID uuid.UUID `json:"client_event_id"`
	ShipmentID    int64     `json:"shipment_id"`
	Kind          string    `json:"kind"`
	RecordedAt    time.Time `json:"recorded_at"`
	TempCC        *int      `json:"temp_cc"`
	ReceiverName  *string   `json:"receiver_name"`
	Note          string    `json:"note"`
}

type syncResult struct {
	ClientEventID uuid.UUID `json:"client_event_id"`
	Outcome       string    `json:"outcome"` // applied | duplicate_delivery | rejected | already_synced
	Reason        string    `json:"reason,omitempty"`
}

// driverSync applies events captured offline by the driver app.
//
// Each event carries a client-generated UUID. The UNIQUE constraint on
// client_event_id makes the whole sync idempotent: a phone that lost the
// response and re-sends the same queue gets "already_synced" back and nothing
// is applied twice. Events are applied in recorded order, one transaction
// each, so a bad event does not block the rest of the queue.
func (s *Server) driverSync(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Events []driverEvent `json:"events"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if len(in.Events) == 0 || len(in.Events) > 200 {
		return httpx.Validation{"events": "must contain 1-200 events"}.Err()
	}
	ctx := r.Context()
	p := principal(ctx)
	now := s.now()
	sortEvents(in.Events)
	results := make([]syncResult, 0, len(in.Events))
	for _, ev := range in.Events {
		res, err := s.applyDriverEvent(ctx, p.UserID, ev, now)
		if err != nil {
			return err
		}
		results = append(results, res)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"results": results, "server_time": now})
	return nil
}

func sortEvents(evs []driverEvent) {
	slices.SortStableFunc(evs, func(a, b driverEvent) int { return a.RecordedAt.Compare(b.RecordedAt) })
}

// classifyDriverEvent decides what to do with an event for a shipment in the
// given state. It is pure so the rules are easy to read and test.
func classifyDriverEvent(ev driverEvent, status string, departed *time.Time, now time.Time) (outcome, reason string) {
	switch {
	case ev.Kind != "delivery" && ev.Kind != "temperature_check":
		return "rejected", "unknown kind"
	case ev.RecordedAt.IsZero() || ev.RecordedAt.After(now.Add(5*time.Minute)):
		return "rejected", "recorded_at is missing or in the future"
	case ev.Kind == "temperature_check" && ev.TempCC == nil:
		return "rejected", "temp_cc is required"
	case ev.Kind == "temperature_check":
		return "applied", ""
	case status == "delivered":
		return "duplicate_delivery", "shipment was already delivered"
	case status != "in_transit":
		return "rejected", "shipment is " + status
	case departed != nil && ev.RecordedAt.Before(*departed):
		return "rejected", "delivery recorded before departure"
	}
	return "applied", ""
}

func (s *Server) applyDriverEvent(ctx context.Context, driverID int64, ev driverEvent, now time.Time) (syncResult, error) {
	res := syncResult{ClientEventID: ev.ClientEventID}
	if ev.ClientEventID == uuid.Nil {
		res.Outcome, res.Reason = "rejected", "client_event_id is required"
		return res, nil
	}
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM driver_events WHERE client_event_id = $1)`, ev.ClientEventID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			res.Outcome = "already_synced"
			return nil
		}
		var status string
		var departed *time.Time
		var customerLoc int64
		err := tx.QueryRow(ctx, `
			SELECT sh.status::text, sh.departed_at, cl.id FROM shipments sh
			JOIN vehicles v ON v.id = sh.vehicle_id
			JOIN locations cl ON cl.customer_id = sh.customer_id
			WHERE sh.id = $1 AND v.driver_id = $2 FOR UPDATE OF sh`, ev.ShipmentID, driverID).Scan(&status, &departed, &customerLoc)
		if errors.Is(err, pgx.ErrNoRows) {
			// unknown shipment or another driver's: same answer, nothing stored
			res.Outcome, res.Reason = "rejected", "shipment not assigned to you"
			return nil
		}
		if err != nil {
			return err
		}
		outcome, reason := classifyDriverEvent(ev, status, departed, now)
		if outcome == "applied" && ev.Kind == "delivery" {
			if err := s.deliver(ctx, tx, ev, status, customerLoc); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO driver_events (client_event_id, driver_id, shipment_id, kind, recorded_at, temp_cc,
			receiver_name, note, outcome, outcome_reason) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			ev.ClientEventID, driverID, ev.ShipmentID, ev.Kind, ev.RecordedAt, ev.TempCC, ev.ReceiverName, ev.Note, outcome, reason)
		if db.IsUniqueViolation(err, "") {
			return errAlreadySynced
		}
		if err != nil {
			return err
		}
		res.Outcome, res.Reason = outcome, reason
		return nil
	})
	if errors.Is(err, errAlreadySynced) {
		// a concurrent sync of the same queue won the race; its result stands
		return syncResult{ClientEventID: ev.ClientEventID, Outcome: "already_synced"}, nil
	}
	return res, err
}

// deliver hands every pallet of the shipment over to the customer at the
// time recorded on the phone, not the time of the sync.
func (s *Server) deliver(ctx context.Context, tx pgx.Tx, ev driverEvent, before string, customerLoc int64) error {
	at := ev.RecordedAt
	if _, err := tx.Exec(ctx, `UPDATE shipments SET status = 'delivered', delivered_at = $2 WHERE id = $1`, ev.ShipmentID, at); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT unit_id FROM shipment_items WHERE shipment_id = $1`, ev.ShipmentID)
	if err != nil {
		return err
	}
	units, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, u := range units {
		if err := moveCustody(ctx, tx, u, customerLoc, at); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE units SET status = 'delivered' WHERE id = ANY($1)`, units); err != nil {
		return err
	}
	return audit(ctx, tx, "shipment.deliver", "shipment", ev.ShipmentID, map[string]string{"status": before},
		map[string]any{"status": "delivered", "recorded_at": at, "client_event_id": ev.ClientEventID})
}

var errAlreadySynced = errors.New("already synced")
