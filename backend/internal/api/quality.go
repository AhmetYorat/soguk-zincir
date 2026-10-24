package api

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/AhmetYorat/soguk-zincir/backend/internal/db"
	"github.com/AhmetYorat/soguk-zincir/backend/internal/domain/chain"
	"github.com/AhmetYorat/soguk-zincir/backend/internal/httpx"
)

// ---- alarms ----

type alarmRow struct {
	ID              int64      `json:"id"`
	Rule            string     `json:"rule"`
	Status          string     `json:"status"`
	EscalationLevel int16      `json:"escalation_level"`
	SensorID        *int32     `json:"sensor_id"`
	SensorCode      *string    `json:"sensor_code"`
	LocationID      *int64     `json:"location_id"`
	LocationName    *string    `json:"location_name"`
	LotID           *int64     `json:"lot_id"`
	LotCode         *string    `json:"lot_code"`
	ProductID       *int64     `json:"product_id"`
	PeakCC          *int16     `json:"peak_cc"`
	ExcursionS      int        `json:"excursion_s"`
	Message         string     `json:"message"`
	OpenedAt        time.Time  `json:"opened_at"`
	LastSeenAt      time.Time  `json:"last_seen_at"`
	EscalatedAt     *time.Time `json:"escalated_at"`
	AcknowledgedAt  *time.Time `json:"acknowledged_at"`
	ClosedAt        *time.Time `json:"closed_at"`
}

const alarmSelect = `
	SELECT a.id, a.rule::text, a.status::text, a.escalation_level, a.sensor_id, s.code, a.location_id, l.name,
	       a.lot_id, lo.code, a.product_id, a.peak_cc, a.excursion_s, a.message, a.opened_at, a.last_seen_at,
	       a.escalated_at, a.acknowledged_at, a.closed_at
	FROM alarms a
	LEFT JOIN sensors s ON s.id = a.sensor_id
	LEFT JOIN locations l ON l.id = a.location_id
	LEFT JOIN lots lo ON lo.id = a.lot_id`

func (s *Server) listAlarms(w http.ResponseWriter, r *http.Request) error {
	page, err := httpx.ParsePage(r, 50, 200)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	// status: empty = all, "live" = not closed, otherwise an exact status
	rows, err := s.pool.Query(r.Context(), alarmSelect+`
		WHERE a.id < $1 AND ($2 = '' OR ($2 = 'live' AND a.status <> 'closed') OR a.status::text = $2)
		  AND ($3 = '' OR a.rule::text = $3) AND ($4 = '' OR a.lot_id::text = $4)
		ORDER BY a.id DESC LIMIT $5`, page.MaxID(), q.Get("status"), q.Get("rule"), q.Get("lot_id"), page.Limit)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[alarmRow])
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(items, page, func(a alarmRow) int64 { return a.ID }))
	return nil
}

func (s *Server) transitionAlarm(w http.ResponseWriter, r *http.Request, action, sql string) error {
	id, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	ctx := r.Context()
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var before string
		err := tx.QueryRow(ctx, `SELECT status::text FROM alarms WHERE id = $1 FOR UPDATE`, id).Scan(&before)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, sql, id, principal(ctx).UserID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.Errorf(http.StatusConflict, "invalid_transition", "alarm is %s; cannot %s", before, action)
		}
		if err := audit(ctx, tx, "alarm."+action, "alarm", id, map[string]string{"status": before}, nil); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, alarmSelect+` WHERE a.id = $1`, id)
		if err != nil {
			return err
		}
		a, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[alarmRow])
		if err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, a)
		return nil
	})
}

func (s *Server) ackAlarm(w http.ResponseWriter, r *http.Request) error {
	return s.transitionAlarm(w, r, "acknowledge", `UPDATE alarms SET status = 'acknowledged', acknowledged_at = now(), acknowledged_by = $2
		WHERE id = $1 AND status = 'open'`)
}

func (s *Server) closeAlarm(w http.ResponseWriter, r *http.Request) error {
	return s.transitionAlarm(w, r, "close", `UPDATE alarms SET status = 'closed', closed_at = now(),
		acknowledged_at = coalesce(acknowledged_at, now()), acknowledged_by = coalesce(acknowledged_by, $2)
		WHERE id = $1 AND status <> 'closed'`)
}

type nonconformity struct {
	ID          int64      `json:"id"`
	LotID       int64      `json:"lot_id"`
	LotCode     string     `json:"lot_code"`
	AlarmID     *int64     `json:"alarm_id"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	OpenedBy    int64      `json:"opened_by"`
	OpenedAt    time.Time  `json:"opened_at"`
	ClosedAt    *time.Time `json:"closed_at"`
}

func (s *Server) listNonconformities(w http.ResponseWriter, r *http.Request) error {
	page, err := httpx.ParsePage(r, 50, 200)
	if err != nil {
		return err
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT n.id, n.lot_id, l.code, n.alarm_id, n.description, n.status, n.opened_by, n.opened_at, n.closed_at
		FROM nonconformities n JOIN lots l ON l.id = n.lot_id
		WHERE n.id < $1 AND ($2 = '' OR n.lot_id::text = $2) ORDER BY n.id DESC LIMIT $3`,
		page.MaxID(), r.URL.Query().Get("lot_id"), page.Limit)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[nonconformity])
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(items, page, func(n nonconformity) int64 { return n.ID }))
	return nil
}

// createNonconformity records a deviation and puts a released lot on hold
// until quality decides.
func (s *Server) createNonconformity(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		LotID       int64  `json:"lot_id"`
		AlarmID     *int64 `json:"alarm_id"`
		Description string `json:"description"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if len(in.Description) < 3 {
		return httpx.Validation{"description": "must be at least 3 characters"}.Err()
	}
	ctx := r.Context()
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id int64
		err := tx.QueryRow(ctx, `INSERT INTO nonconformities (lot_id, alarm_id, description, opened_by)
			VALUES ($1, $2, $3, $4) RETURNING id`, in.LotID, in.AlarmID, in.Description, principal(ctx).UserID).Scan(&id)
		if db.IsConstraintViolation(err) {
			return httpx.Validation{"lot_id": "unknown lot or alarm"}.Err()
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE lots SET status = 'on_hold' WHERE id = $1 AND status = 'released'`, in.LotID); err != nil {
			return err
		}
		if err := audit(ctx, tx, "nonconformity.create", "lot", in.LotID, nil, in); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"id": id, "lot_id": in.LotID})
		return nil
	})
}

// ---- quality decisions ----

// allowedDecisions is the lot state machine: decisions may only make a lot's
// fate stricter, never undo a destruction or recall.
var allowedDecisions = map[string][]string{
	"released":   {"accept", "discount", "destroy"},
	"on_hold":    {"accept", "discount", "destroy"},
	"accepted":   {"discount", "destroy"},
	"discounted": {"destroy"},
}

var decisionStatus = map[string]string{"accept": "accepted", "discount": "discounted", "destroy": "destroyed"}

func (s *Server) decideLot(w http.ResponseWriter, r *http.Request) error {
	lotID, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	var in struct {
		Decision        string `json:"decision"`
		DiscountPct     int    `json:"discount_pct"`
		Reason          string `json:"reason"`
		NonconformityID *int64 `json:"nonconformity_id"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	v := httpx.Validation{}
	_, known := decisionStatus[in.Decision]
	v.Check(known, "decision", "must be accept, discount or destroy")
	v.Check(len(in.Reason) >= 3, "reason", "must be at least 3 characters")
	if in.Decision == "discount" {
		v.Check(in.DiscountPct >= 1 && in.DiscountPct <= 99, "discount_pct", "must be 1-99 for a discount")
	} else {
		v.Check(in.DiscountPct == 0, "discount_pct", "only allowed for a discount")
	}
	if err := v.Err(); err != nil {
		return err
	}
	ctx := r.Context()
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The row lock serialises concurrent decisions on the same lot: the
		// second one re-reads the new status and is checked against it.
		var status string
		err := tx.QueryRow(ctx, `SELECT status::text FROM lots WHERE id = $1 FOR UPDATE`, lotID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		if !slices.Contains(allowedDecisions[status], in.Decision) {
			return httpx.Errorf(http.StatusConflict, "invalid_transition", "lot is %s; %s is not allowed", status, in.Decision).
				With("allowed", allowedDecisions[status])
		}
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO quality_decisions (lot_id, nonconformity_id, decision, discount_pct, reason, decided_by)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			lotID, in.NonconformityID, in.Decision, in.DiscountPct, in.Reason, principal(ctx).UserID).Scan(&id); err != nil {
			if db.IsConstraintViolation(err) {
				return httpx.Validation{"nonconformity_id": "unknown nonconformity"}.Err()
			}
			return err
		}
		newStatus := decisionStatus[in.Decision]
		if _, err := tx.Exec(ctx, `UPDATE lots SET status = $2, discount_pct = $3 WHERE id = $1`, lotID, newStatus, in.DiscountPct); err != nil {
			return err
		}
		if in.Decision == "destroy" {
			// take the lot off every pallet still in our custody
			if _, err := tx.Exec(ctx, `UPDATE unit_contents c SET quantity = 0 FROM units u
				WHERE u.id = c.unit_id AND c.lot_id = $1 AND u.status = 'active'`, lotID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE units u SET status = 'destroyed' WHERE u.status = 'active'
				AND NOT EXISTS (SELECT 1 FROM unit_contents c WHERE c.unit_id = u.id AND c.quantity > 0)
				AND EXISTS (SELECT 1 FROM unit_contents c WHERE c.unit_id = u.id AND c.lot_id = $1)`, lotID); err != nil {
				return err
			}
		}
		if in.NonconformityID != nil {
			if _, err := tx.Exec(ctx, `UPDATE nonconformities SET status = 'closed', closed_at = now() WHERE id = $1 AND lot_id = $2`,
				*in.NonconformityID, lotID); err != nil {
				return err
			}
		}
		if err := audit(ctx, tx, "lot.decision", "lot", lotID, map[string]any{"status": status},
			map[string]any{"status": newStatus, "decision": in.Decision, "discount_pct": in.DiscountPct, "reason": in.Reason}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"id": id, "lot_id": lotID, "decision": in.Decision, "lot_status": newStatus})
		return nil
	})
}

func (s *Server) listDecisions(w http.ResponseWriter, r *http.Request) error {
	lotID, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	type decision struct {
		ID          int64     `json:"id"`
		Decision    string    `json:"decision"`
		DiscountPct int16     `json:"discount_pct"`
		Reason      string    `json:"reason"`
		DecidedBy   string    `json:"decided_by"`
		DecidedAt   time.Time `json:"decided_at"`
	}
	rows, err := s.pool.Query(r.Context(), `SELECT d.id, d.decision::text, d.discount_pct, d.reason, u.full_name, d.decided_at
		FROM quality_decisions d JOIN users u ON u.id = d.decided_by WHERE d.lot_id = $1 ORDER BY d.id`, lotID)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[decision])
	if err != nil {
		return err
	}
	if items == nil {
		items = []decision{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

// ---- excursion reports (hash chain) ----

type reportRow struct {
	Seq       int64           `json:"seq"`
	LotID     int64           `json:"lot_id"`
	LotCode   string          `json:"lot_code"`
	CreatedBy int64           `json:"created_by"`
	CreatedAt time.Time       `json:"created_at"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	PrevHash  string          `json:"prev_hash"`
	Hash      string          `json:"hash"`
}

// createExcursionReport freezes the current evaluation of a lot into an
// immutable, chained report.
func (s *Server) createExcursionReport(w http.ResponseWriter, r *http.Request) error {
	lotID, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	ctx := r.Context()
	ev, err := s.evaluateLot(ctx, s.pool, lotID)
	if err != nil {
		return err
	}
	p := principal(ctx)
	payload := map[string]any{
		"schema":       "excursion-report/v1",
		"lot":          map[string]any{"id": ev.LotID, "code": ev.LotCode, "status": ev.Status, "produced_at": ev.ProducedAt},
		"product":      map[string]any{"id": ev.Product.ID, "sku": ev.Product.SKU, "temp_min_cc": ev.Product.TempMinCC, "temp_max_cc": ev.Product.TempMaxCC},
		"evaluated_at": ev.EvaluatedAt,
		"units":        ev.Units,
		"author":       map[string]any{"id": p.UserID, "email": p.Email, "role": p.Role},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	canon, err := chain.Canonical(raw)
	if err != nil {
		return err
	}
	var out reportRow
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var seq int64
		var prev []byte
		// the head row lock serialises appenders
		if err := tx.QueryRow(ctx, `SELECT seq, hash FROM report_chain_head WHERE id = 1 FOR UPDATE`).Scan(&seq, &prev); err != nil {
			return err
		}
		seq++
		at := s.now().UTC().Truncate(time.Microsecond)
		hash := chain.Link(prev, seq, at, canon)
		if _, err := tx.Exec(ctx, `INSERT INTO excursion_reports (seq, lot_id, created_by, created_at, payload, prev_hash, hash)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, seq, lotID, p.UserID, at, canon, prev, hash); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE report_chain_head SET seq = $1, hash = $2 WHERE id = 1`, seq, hash); err != nil {
			return err
		}
		if err := audit(ctx, tx, "excursion_report.create", "lot", lotID, nil, map[string]any{"seq": seq, "hash": hex.EncodeToString(hash)}); err != nil {
			return err
		}
		out = reportRow{Seq: seq, LotID: lotID, LotCode: ev.LotCode, CreatedBy: p.UserID, CreatedAt: at,
			PrevHash: hex.EncodeToString(prev), Hash: hex.EncodeToString(hash), Payload: canon}
		return nil
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, out)
	return nil
}

func scanReport(row pgx.CollectableRow) (reportRow, error) {
	var rr reportRow
	var prev, hash []byte
	var payload *string
	err := row.Scan(&rr.Seq, &rr.LotID, &rr.LotCode, &rr.CreatedBy, &rr.CreatedAt, &payload, &prev, &hash)
	rr.PrevHash, rr.Hash = hex.EncodeToString(prev), hex.EncodeToString(hash)
	if payload != nil {
		rr.Payload = json.RawMessage(*payload)
	}
	return rr, err
}

func (s *Server) listExcursionReports(w http.ResponseWriter, r *http.Request) error {
	page, err := httpx.ParsePage(r, 50, 200)
	if err != nil {
		return err
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT e.seq, e.lot_id, l.code, e.created_by, e.created_at, NULL::text, e.prev_hash, e.hash
		FROM excursion_reports e JOIN lots l ON l.id = e.lot_id
		WHERE e.seq < $1 AND ($2 = '' OR e.lot_id::text = $2) ORDER BY e.seq DESC LIMIT $3`,
		page.MaxID(), r.URL.Query().Get("lot_id"), page.Limit)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, scanReport)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(items, page, func(x reportRow) int64 { return x.Seq }))
	return nil
}

func (s *Server) getExcursionReport(w http.ResponseWriter, r *http.Request) error {
	seq, err := strconv.ParseInt(chi.URLParam(r, "seq"), 10, 64)
	if err != nil || seq <= 0 {
		return httpx.Errorf(http.StatusBadRequest, "invalid_id", "seq must be a positive integer")
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT e.seq, e.lot_id, l.code, e.created_by, e.created_at, e.payload::text, e.prev_hash, e.hash
		FROM excursion_reports e JOIN lots l ON l.id = e.lot_id WHERE e.seq = $1`, seq)
	if err != nil {
		return err
	}
	rr, err := pgx.CollectExactlyOneRow(rows, scanReport)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, rr)
	return nil
}

// verifyExcursionReports recomputes the whole chain and compares it with the
// stored head.
func (s *Server) verifyExcursionReports(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	rows, err := s.pool.Query(ctx, `SELECT seq, created_at, payload::text, prev_hash, hash FROM excursion_reports ORDER BY seq`)
	if err != nil {
		return err
	}
	entries, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (chain.Entry, error) {
		var e chain.Entry
		var payload string
		err := row.Scan(&e.Seq, &e.CreatedAt, &payload, &e.PrevHash, &e.Hash)
		e.Payload = []byte(payload)
		return e, err
	})
	if err != nil {
		return err
	}
	var headSeq int64
	var headHash []byte
	if err := s.pool.QueryRow(ctx, `SELECT seq, hash FROM report_chain_head WHERE id = 1`).Scan(&headSeq, &headHash); err != nil {
		return err
	}
	problem := chain.Verify(entries)
	if problem == nil && (headSeq != int64(len(entries)) || (len(entries) > 0 && hex.EncodeToString(headHash) != hex.EncodeToString(entries[len(entries)-1].Hash))) {
		problem = &chain.Problem{Seq: headSeq, Reason: "chain head does not match the last report (entries removed?)"}
	}
	resp := map[string]any{"valid": problem == nil, "entries": len(entries), "head_seq": headSeq, "head_hash": hex.EncodeToString(headHash)}
	if problem != nil {
		resp["problem"] = problem
	}
	httpx.JSON(w, http.StatusOK, resp)
	return nil
}
