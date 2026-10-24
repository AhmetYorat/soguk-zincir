// Package httpx contains HTTP plumbing shared by all handlers: the error
// format, JSON helpers, keyset pagination and middleware.
package httpx

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Error is the single error shape returned by the API:
//
//	{"error":{"code":"…","message":"…","details":{…}}}
type Error struct {
	Status  int            `json:"-"`
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Errorf builds an API error.
func Errorf(status int, code, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// With adds details.
func (e *Error) With(k string, v any) *Error {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details[k] = v
	return e
}

// Common errors.
var (
	ErrUnauthorized = &Error{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "authentication required"}
	ErrForbidden    = &Error{Status: http.StatusForbidden, Code: "forbidden", Message: "your role is not allowed to do this"}
	ErrNotFound     = &Error{Status: http.StatusNotFound, Code: "not_found", Message: "resource not found"}
)

// Validation collects field errors.
type Validation map[string]string

// Add records a problem with a field.
func (v Validation) Add(field, msg string) { v[field] = msg }

// Check records msg when ok is false.
func (v Validation) Check(ok bool, field, msg string) {
	if !ok {
		v[field] = msg
	}
}

// Err returns a 422 error or nil.
func (v Validation) Err() error {
	if len(v) == 0 {
		return nil
	}
	fields := make(map[string]any, len(v))
	for k, m := range v {
		fields[k] = m
	}
	return &Error{Status: http.StatusUnprocessableEntity, Code: "validation_failed", Message: "request is invalid", Details: map[string]any{"fields": fields}}
}

// JSON writes v with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError converts any error into the API format. Unknown errors become
// a 500 without leaking internals; they are logged with the request id.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var e *Error
	if !errors.As(err, &e) {
		Logger(r.Context()).Error("unhandled error", "err", err)
		e = &Error{Status: http.StatusInternalServerError, Code: "internal", Message: "internal server error"}
	}
	JSON(w, e.Status, map[string]any{"error": e})
}

// Handler is an http.HandlerFunc that returns an error.
type Handler func(w http.ResponseWriter, r *http.Request) error

// Wrap adapts a Handler.
func Wrap(h Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			WriteError(w, r, err)
		}
	}
}

const maxBody = 1 << 20

// Decode reads a JSON body strictly: unknown fields and trailing data are
// rejected and the size is capped.
func Decode(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return Errorf(http.StatusBadRequest, "invalid_json", "request body is not valid JSON: %s", err.Error())
	}
	if dec.More() {
		return Errorf(http.StatusBadRequest, "invalid_json", "unexpected data after JSON body")
	}
	return nil
}

// PathID parses a numeric path parameter.
func PathID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, Errorf(http.StatusBadRequest, "invalid_id", "id must be a positive integer")
	}
	return id, nil
}

// Page holds keyset pagination parameters. Lists are ordered by id
// descending; the cursor is the last id of the previous page, so the query is
// `WHERE id < $cursor ORDER BY id DESC LIMIT $n` and stays O(log n) however
// deep the client pages, unlike OFFSET.
type Page struct {
	Limit int
	After int64 // 0 = first page
}

// ParsePage reads ?limit= and ?cursor=.
func ParsePage(r *http.Request, def, max int) (Page, error) {
	p := Page{Limit: def}
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > max {
			return p, Errorf(http.StatusBadRequest, "invalid_limit", "limit must be between 1 and %d", max)
		}
		p.Limit = n
	}
	if c := r.URL.Query().Get("cursor"); c != "" {
		b, err := base64.RawURLEncoding.DecodeString(c)
		if err != nil {
			return p, Errorf(http.StatusBadRequest, "invalid_cursor", "cursor is malformed")
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(string(b), "id:"), 10, 64)
		if err != nil || id <= 0 {
			return p, Errorf(http.StatusBadRequest, "invalid_cursor", "cursor is malformed")
		}
		p.After = id
	}
	return p, nil
}

// Cursor returns the next-page cursor when the page is full.
func (p Page) Cursor(n int, lastID int64) *string {
	if n < p.Limit {
		return nil
	}
	c := base64.RawURLEncoding.EncodeToString([]byte("id:" + strconv.FormatInt(lastID, 10)))
	return &c
}

// MaxID is the starting cursor value for the first page.
func (p Page) MaxID() int64 {
	if p.After == 0 {
		return 1<<63 - 1
	}
	return p.After
}

// List is the envelope for paginated responses.
type List[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// NewList builds the envelope; idOf extracts the cursor id from the last item.
func NewList[T any](items []T, p Page, idOf func(T) int64) List[T] {
	if items == nil {
		items = []T{}
	}
	l := List[T]{Items: items}
	if len(items) > 0 {
		l.NextCursor = p.Cursor(len(items), idOf(items[len(items)-1]))
	}
	return l
}
