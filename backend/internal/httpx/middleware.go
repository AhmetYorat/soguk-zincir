package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
)

type ctxKey int

const (
	keyLogger ctxKey = iota
	keyRequestID
)

// Logger returns the request-scoped logger.
func Logger(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(keyLogger).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// RequestID returns the request id.
func RequestID(ctx context.Context) string {
	s, _ := ctx.Value(keyRequestID).(string)
	return s
}

// statusRecorder captures the status code for logs and metrics.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Metrics are the HTTP Prometheus collectors.
type Metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewMetrics registers HTTP metrics.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total", Help: "HTTP requests by route and status.",
		}, []string{"method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "http_request_duration_seconds", Help: "HTTP request latency.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
		}, []string{"method", "route"}),
	}
	reg.MustRegister(m.requests, m.duration)
	return m
}

// Observe is the outermost middleware: request id, structured access log,
// panic recovery and metrics. Tokens and passwords are never logged: only
// method, route pattern, status and timing are.
func Observe(base *slog.Logger, m *Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			id := r.Header.Get("X-Request-Id")
			if id == "" || len(id) > 64 {
				b := make([]byte, 8)
				_, _ = rand.Read(b)
				id = hex.EncodeToString(b)
			}
			w.Header().Set("X-Request-Id", id)
			log := base.With("request_id", id)
			ctx := context.WithValue(context.WithValue(r.Context(), keyLogger, log), keyRequestID, id)
			rec := &statusRecorder{ResponseWriter: w}

			defer func() {
				if p := recover(); p != nil {
					log.Error("panic", "panic", p, "stack", string(debug.Stack()))
					if rec.status == 0 {
						WriteError(rec, r.WithContext(ctx), Errorf(http.StatusInternalServerError, "internal", "internal server error"))
					}
				}
				route := chi.RouteContext(ctx).RoutePattern()
				if route == "" {
					route = "unmatched"
				}
				elapsed := time.Since(start)
				m.requests.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Inc()
				m.duration.WithLabelValues(r.Method, route).Observe(elapsed.Seconds())
				level := slog.LevelInfo
				if rec.status >= 500 {
					level = slog.LevelError
				} else if route == "/health" || route == "/metrics" || route == "/api/v1/ingest/readings" {
					level = slog.LevelDebug
				}
				log.Log(ctx, level, "http request", "method", r.Method, "route", route,
					"status", rec.status, "bytes", rec.bytes, "duration_ms", elapsed.Milliseconds())
			}()
			next.ServeHTTP(rec, r.WithContext(ctx))
		})
	}
}

// Timeout bounds the request context; handlers pass it to every query.
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// CORS allows the configured frontend origin.
func CORS(origin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Origin") == origin {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Vary", "Origin")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, X-Request-Id")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
				h.Set("Access-Control-Max-Age", "600")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders sets conservative defaults for an API.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
