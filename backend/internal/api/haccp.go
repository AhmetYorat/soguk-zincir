package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AhmetYorat/soguk-zincir/backend/internal/db"
	"github.com/AhmetYorat/soguk-zincir/backend/internal/httpx"
)

type haccpItem struct {
	ID       int64  `json:"id"`
	Position int    `json:"position"`
	CCPCode  string `json:"ccp_code"`
	Question string `json:"question"`
	Kind     string `json:"kind"`
	MinCC    *int   `json:"min_cc"`
	MaxCC    *int   `json:"max_cc"`
}

type checklist struct {
	ID        int64       `json:"id"`
	Code      string      `json:"code"`
	Name      string      `json:"name"`
	Frequency string      `json:"frequency"`
	Items     []haccpItem `json:"items"`
}

func (s *Server) listChecklists(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.pool.Query(r.Context(), `
		SELECT c.id, c.code, c.name, c.frequency,
		       coalesce(json_agg(json_build_object('id', i.id, 'position', i.position, 'ccp_code', i.ccp_code,
		           'question', i.question, 'kind', i.kind, 'min_cc', i.min_cc, 'max_cc', i.max_cc) ORDER BY i.position)
		           FILTER (WHERE i.id IS NOT NULL), '[]')
		FROM haccp_checklists c LEFT JOIN haccp_items i ON i.checklist_id = c.id
		GROUP BY c.id ORDER BY c.code`)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[checklist])
	if err != nil {
		return err
	}
	if items == nil {
		items = []checklist{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

type haccpAnswerIn struct {
	ItemID           int64   `json:"item_id"`
	BoolValue        *bool   `json:"bool_value"`
	TempCC           *int    `json:"temp_cc"`
	CorrectiveAction *string `json:"corrective_action"`
}

// createHACCPRecord stores a completed checklist. Every item must be
// answered; a failed control point needs a corrective action.
func (s *Server) createHACCPRecord(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		ChecklistID int64           `json:"checklist_id"`
		LocationID  int64           `json:"location_id"`
		Notes       string          `json:"notes"`
		Answers     []haccpAnswerIn `json:"answers"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	rows, err := s.pool.Query(ctx, `SELECT id, position, ccp_code, question, kind, min_cc, max_cc FROM haccp_items WHERE checklist_id = $1`, in.ChecklistID)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[haccpItem])
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return httpx.Validation{"checklist_id": "unknown checklist"}.Err()
	}
	byID := map[int64]haccpItem{}
	for _, it := range items {
		byID[it.ID] = it
	}
	v := httpx.Validation{}
	answered := map[int64]bool{}
	type result struct {
		in haccpAnswerIn
		ok bool
	}
	var results []result
	allOK := true
	for _, a := range in.Answers {
		it, known := byID[a.ItemID]
		if !known || answered[a.ItemID] {
			v.Add("answers", "contains unknown or duplicate items")
			continue
		}
		answered[a.ItemID] = true
		var ok bool
		switch it.Kind {
		case "boolean":
			if a.BoolValue == nil {
				v.Add("answers."+it.CCPCode, "needs bool_value")
				continue
			}
			ok = *a.BoolValue
		case "temperature":
			if a.TempCC == nil {
				v.Add("answers."+it.CCPCode, "needs temp_cc")
				continue
			}
			ok = *a.TempCC >= *it.MinCC && *a.TempCC <= *it.MaxCC
		}
		if !ok && (a.CorrectiveAction == nil || len(*a.CorrectiveAction) < 3) {
			v.Add("answers."+it.CCPCode, "failed control point requires a corrective_action")
		}
		allOK = allOK && ok
		results = append(results, result{a, ok})
	}
	v.Check(len(answered) == len(items), "answers", "every item of the checklist must be answered")
	if err := v.Err(); err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id int64
		err := tx.QueryRow(ctx, `INSERT INTO haccp_records (checklist_id, location_id, performed_by, all_ok, notes)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`, in.ChecklistID, in.LocationID, principal(ctx).UserID, allOK, in.Notes).Scan(&id)
		if db.IsConstraintViolation(err) {
			return httpx.Validation{"location_id": "unknown location"}.Err()
		}
		if err != nil {
			return err
		}
		for _, res := range results {
			if _, err := tx.Exec(ctx, `INSERT INTO haccp_answers (record_id, item_id, bool_value, temp_cc, ok, corrective_action)
				VALUES ($1, $2, $3, $4, $5, $6)`, id, res.in.ItemID, res.in.BoolValue, res.in.TempCC, res.ok, res.in.CorrectiveAction); err != nil {
				return err
			}
		}
		if err := audit(ctx, tx, "haccp.record", "haccp_record", id, nil, in); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"id": id, "all_ok": allOK})
		return nil
	})
}

func (s *Server) listHACCPRecords(w http.ResponseWriter, r *http.Request) error {
	page, err := httpx.ParsePage(r, 50, 200)
	if err != nil {
		return err
	}
	type rec struct {
		ID          int64           `json:"id"`
		Checklist   string          `json:"checklist"`
		Location    string          `json:"location"`
		PerformedBy string          `json:"performed_by"`
		PerformedAt time.Time       `json:"performed_at"`
		AllOK       bool            `json:"all_ok"`
		Notes       string          `json:"notes"`
		Answers     json.RawMessage `json:"answers"`
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT h.id, c.name, l.name, u.full_name, h.performed_at, h.all_ok, h.notes,
		       (SELECT json_agg(json_build_object('ccp_code', i.ccp_code, 'question', i.question, 'ok', a.ok,
		            'bool_value', a.bool_value, 'temp_cc', a.temp_cc, 'corrective_action', a.corrective_action) ORDER BY i.position)
		        FROM haccp_answers a JOIN haccp_items i ON i.id = a.item_id WHERE a.record_id = h.id)
		FROM haccp_records h
		JOIN haccp_checklists c ON c.id = h.checklist_id
		JOIN locations l ON l.id = h.location_id
		JOIN users u ON u.id = h.performed_by
		WHERE h.id < $1 AND ($2 = '' OR h.all_ok::text = $2)
		ORDER BY h.id DESC LIMIT $3`, page.MaxID(), r.URL.Query().Get("all_ok"), page.Limit)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[rec])
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(items, page, func(x rec) int64 { return x.ID }))
	return nil
}
