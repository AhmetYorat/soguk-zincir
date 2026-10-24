package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestErrorFormat(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest("GET", "/", nil), Errorf(http.StatusConflict, "already_loaded", "unit %d", 7).With("unit_id", 7))
	var body map[string]map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != 409 || body["error"]["code"] != "already_loaded" || body["error"]["message"] != "unit 7" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestUnknownErrorsDoNotLeak(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest("GET", "/", nil), errors.New("pq: password authentication failed for user x"))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("internal error leaked: %s", rec.Body)
	}
}

func TestValidationCollectsFields(t *testing.T) {
	v := Validation{}
	v.Check(false, "quantity", "must be positive")
	v.Check(true, "code", "never")
	err := v.Err()
	var e *Error
	if !errors.As(err, &e) || e.Status != 422 || len(e.Details["fields"].(map[string]any)) != 1 {
		t.Fatalf("%+v", err)
	}
	if (Validation{}).Err() != nil {
		t.Fatal("empty validation must be nil")
	}
}

func TestDecodeRejectsUnknownFieldsAndTrailingData(t *testing.T) {
	var dst struct {
		A int `json:"a"`
	}
	for _, body := range []string{`{"a":1,"b":2}`, `{"a":1}{"a":2}`, `not json`} {
		if err := Decode(httptest.NewRequest("POST", "/", strings.NewReader(body)), &dst); err == nil {
			t.Errorf("%s accepted", body)
		}
	}
	if err := Decode(httptest.NewRequest("POST", "/", strings.NewReader(`{"a":1}`)), &dst); err != nil || dst.A != 1 {
		t.Fatal(err)
	}
}

func TestKeysetPagination(t *testing.T) {
	p, err := ParsePage(httptest.NewRequest("GET", "/?limit=2", nil), 50, 100)
	if err != nil || p.Limit != 2 || p.MaxID() != 1<<63-1 {
		t.Fatalf("%+v %v", p, err)
	}
	l := NewList([]int64{9, 8}, p, func(x int64) int64 { return x })
	if l.NextCursor == nil {
		t.Fatal("full page must have a cursor")
	}
	p2, err := ParsePage(httptest.NewRequest("GET", "/?limit=2&cursor="+*l.NextCursor, nil), 50, 100)
	if err != nil || p2.After != 8 {
		t.Fatalf("cursor round trip: %+v %v", p2, err)
	}
	if NewList([]int64{3}, p, func(x int64) int64 { return x }).NextCursor != nil {
		t.Fatal("short page must not have a cursor")
	}
	for _, q := range []string{"/?limit=0", "/?limit=1000", "/?cursor=***", "/?cursor=bm9wZQ"} {
		if _, err := ParsePage(httptest.NewRequest("GET", q, nil), 50, 100); err == nil {
			t.Errorf("%s accepted", q)
		}
	}
}

func TestPathID(t *testing.T) {
	if _, err := PathID("0"); err == nil {
		t.Fatal("0 accepted")
	}
	if id, err := PathID("42"); err != nil || id != 42 {
		t.Fatal(err)
	}
}

func TestCORSOnlyForConfiguredOrigin(t *testing.T) {
	h := CORS("http://localhost:3000")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for origin, want := range map[string]string{"http://localhost:3000": "http://localhost:3000", "https://evil.example": ""} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != want {
			t.Errorf("%s: %q", origin, got)
		}
	}
}

func TestTimeoutSetsDeadline(t *testing.T) {
	var deadline time.Time
	h := Timeout(time.Second)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { deadline, _ = r.Context().Deadline() }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if time.Until(deadline) > time.Second || deadline.IsZero() {
		t.Fatalf("deadline %v", deadline)
	}
}
