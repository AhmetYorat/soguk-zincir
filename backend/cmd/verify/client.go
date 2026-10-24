package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/AhmetYorat/soguk-zincir/backend/internal/domain/devicesig"
)

// client is a tiny JSON HTTP client bound to one user.
type client struct {
	base  string
	token string
	http  *http.Client
}

type response struct {
	Status  int
	Body    []byte
	Header  http.Header
	Elapsed time.Duration
}

// JSON decodes the body into a generic map.
func (r response) JSON() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(r.Body, &m)
	return m
}

// ErrCode returns error.code of an error response.
func (r response) ErrCode() string {
	if e, ok := r.JSON()["error"].(map[string]any); ok {
		s, _ := e["code"].(string)
		return s
	}
	return ""
}

func (r response) String() string {
	b := r.Body
	if len(b) > 300 {
		b = append(b[:300:300], []byte("…")...)
	}
	return fmt.Sprintf("HTTP %d %s", r.Status, bytes.TrimSpace(b))
}

func (c *client) do(method, path string, body any, headers map[string]string) response {
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case []byte:
			rd = bytes.NewReader(b)
		default:
			raw, _ := json.Marshal(b)
			rd = bytes.NewReader(raw)
		}
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return response{Status: -1, Body: []byte(err.Error())}
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return response{Status: -1, Body: []byte(err.Error()), Elapsed: time.Since(start)}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return response{Status: resp.StatusCode, Body: b, Header: resp.Header, Elapsed: time.Since(start)}
}

func (c *client) get(path string) response { return c.do(http.MethodGet, path, nil, nil) }
func (c *client) post(path string, body any) response {
	return c.do(http.MethodPost, path, body, nil)
}
func (c *client) postIdem(path, key string, body any) response {
	return c.do(http.MethodPost, path, body, map[string]string{"Idempotency-Key": key})
}

func (c *client) as(token string) *client { return &client{base: c.base, token: token, http: c.http} }

// signedIngest sends readings as a device.
func (c *client) signedIngest(device string, key []byte, seq uint64, ts time.Time, body []byte) response {
	ms := ts.UnixMilli()
	return c.do(http.MethodPost, "/api/v1/ingest/readings", body, map[string]string{
		devicesig.HeaderDevice:    device,
		devicesig.HeaderSeq:       strconv.FormatUint(seq, 10),
		devicesig.HeaderTimestamp: strconv.FormatInt(ms, 10),
		devicesig.HeaderSignature: devicesig.Sign(key, device, seq, ms, body),
	})
}

// small helpers for walking decoded JSON
func m(v any) map[string]any { x, _ := v.(map[string]any); return x }
func arr(v any) []any        { x, _ := v.([]any); return x }
func num(v any) float64      { x, _ := v.(float64); return x }
func str(v any) string       { x, _ := v.(string); return x }
func id(v any) int64         { return int64(num(v)) }
