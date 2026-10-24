package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/httpx"
)

// audit appends to the audit log inside the caller's transaction, so the
// record exists if and only if the change was committed.
func audit(ctx context.Context, q Querier, action, entity string, id int64, oldV, newV any) error {
	p := principal(ctx)
	var actor *int64
	var role *string
	if p.UserID != 0 {
		actor = &p.UserID
		r := string(p.Role)
		role = &r
	}
	_, err := q.Exec(ctx, `
		INSERT INTO audit_log (actor_id, actor_role, action, entity, entity_id, old_value, new_value, request_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		actor, role, action, entity, strconv.FormatInt(id, 10), jsonOrNil(oldV), jsonOrNil(newV), httpx.RequestID(ctx))
	return err
}

func jsonOrNil(v any) []byte {
	if v == nil {
		return nil
	}
	b, _ := json.Marshal(v)
	return b
}

type auditEntry struct {
	ID        int64           `json:"id"`
	At        time.Time       `json:"at"`
	ActorID   *int64          `json:"actor_id"`
	ActorRole *string         `json:"actor_role"`
	Action    string          `json:"action"`
	Entity    string          `json:"entity"`
	EntityID  string          `json:"entity_id"`
	OldValue  json.RawMessage `json:"old_value"`
	NewValue  json.RawMessage `json:"new_value"`
	RequestID string          `json:"request_id"`
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) error {
	page, err := httpx.ParsePage(r, 50, 200)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	rows, err := s.pool.Query(r.Context(), `
		SELECT id, at, actor_id, actor_role, action, entity, entity_id, old_value, new_value, request_id
		FROM audit_log
		WHERE id < $1 AND ($2 = '' OR entity = $2) AND ($3 = '' OR entity_id = $3)
		ORDER BY id DESC LIMIT $4`, page.MaxID(), q.Get("entity"), q.Get("entity_id"), page.Limit)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[auditEntry])
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(items, page, func(a auditEntry) int64 { return a.ID }))
	return nil
}
