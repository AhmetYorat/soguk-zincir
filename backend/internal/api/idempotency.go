package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/AhmetYorat/soguk-zincir/backend/internal/httpx"
)

// bufferedWriter records a response so it can be stored and replayed.
type bufferedWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedWriter) Header() http.Header         { return b.header }
func (b *bufferedWriter) Write(p []byte) (int, error) { return b.body.Write(p) }
func (b *bufferedWriter) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}

// idempotent implements the Idempotency-Key header for POST requests:
//
//   - first request with a key: the key is reserved (INSERT ... ON CONFLICT DO
//     NOTHING), the handler runs, the response is stored;
//   - same key, same request: the stored response is returned unchanged and
//     the handler does not run again;
//   - same key, different body/path: 422, the key cannot be reused;
//   - same key while the first is still running: 409.
//
// Keys are scoped per user. Requests without the header run normally.
// Only 2xx responses are stored; on errors the reservation is released so
// the client can retry with the same key after fixing the cause.
func (s *Server) idempotent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			next.ServeHTTP(w, r)
			return
		}
		if len(key) < 8 || len(key) > 128 {
			httpx.WriteError(w, r, httpx.Errorf(http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must be 8-128 characters"))
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			httpx.WriteError(w, r, httpx.Errorf(http.StatusBadRequest, "invalid_body", "cannot read body"))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		h := sha256.New()
		h.Write([]byte(r.Method + " " + r.URL.Path + "\n"))
		h.Write(body)
		reqHash := h.Sum(nil)
		user := principal(r.Context()).UserID
		ctx := r.Context()

		tag, err := s.pool.Exec(ctx, `
			INSERT INTO idempotency_keys (user_id, key, method, path, request_hash)
			VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`,
			user, key, r.Method, r.URL.Path, reqHash)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if tag.RowsAffected() == 0 {
			var storedHash, storedBody []byte
			var status *int
			err := s.pool.QueryRow(ctx, `
				SELECT request_hash, status_code, response_body FROM idempotency_keys
				WHERE user_id = $1 AND key = $2`, user, key).Scan(&storedHash, &status, &storedBody)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				// released between our insert and select; ask the client to retry
				httpx.WriteError(w, r, httpx.Errorf(http.StatusConflict, "idempotency_retry", "please retry the request"))
			case err != nil:
				httpx.WriteError(w, r, err)
			case !bytes.Equal(storedHash, reqHash):
				httpx.WriteError(w, r, httpx.Errorf(http.StatusUnprocessableEntity, "idempotency_key_reused",
					"this Idempotency-Key was already used for a different request"))
			case status == nil:
				httpx.WriteError(w, r, httpx.Errorf(http.StatusConflict, "idempotency_in_progress",
					"a request with this Idempotency-Key is still being processed"))
			default:
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("Idempotent-Replayed", "true")
				w.WriteHeader(*status)
				_, _ = w.Write(storedBody)
			}
			return
		}

		buf := &bufferedWriter{header: w.Header()}
		next.ServeHTTP(buf, r)
		if buf.status == 0 {
			buf.status = http.StatusOK
		}
		// the request context may already be cancelled; use a detached one
		dctx := context.WithoutCancel(ctx)
		if buf.status >= 200 && buf.status < 300 {
			_, err = s.pool.Exec(dctx, `UPDATE idempotency_keys SET status_code = $3, response_body = $4
				WHERE user_id = $1 AND key = $2`, user, key, buf.status, buf.body.Bytes())
		} else {
			_, err = s.pool.Exec(dctx, `DELETE FROM idempotency_keys WHERE user_id = $1 AND key = $2`, user, key)
		}
		if err != nil {
			httpx.Logger(ctx).Error("idempotency bookkeeping failed", "err", err)
		}
		w.WriteHeader(buf.status)
		_, _ = w.Write(buf.body.Bytes())
	})
}
