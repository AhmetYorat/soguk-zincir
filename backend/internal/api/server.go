// Package api wires HTTP routes to the database and domain packages.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/auth"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/config"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/db"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/domain/devicesig"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/httpx"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/ingest"
)

// Querier is satisfied by both *pgxpool.Pool and pgx.Tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Server holds dependencies of the HTTP handlers.
type Server struct {
	cfg     config.Config
	pool    *pgxpool.Pool
	log     *slog.Logger
	issuer  *auth.Issuer
	keybox  *devicesig.Keybox
	ingest  *ingest.Pipeline
	devices *ingest.KeyCache
	reg     *prometheus.Registry
	metrics *httpx.Metrics
	now     func() time.Time
}

// New builds the server.
func New(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger, reg *prometheus.Registry, pipe *ingest.Pipeline) (*Server, error) {
	kb, err := devicesig.NewKeybox(cfg.DeviceKeyEncKey)
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg:     cfg,
		pool:    pool,
		log:     log,
		issuer:  auth.NewIssuer(cfg.JWTSecret, cfg.AccessTTL),
		keybox:  kb,
		ingest:  pipe,
		devices: ingest.NewKeyCache(pool, kb),
		reg:     reg,
		metrics: httpx.NewMetrics(reg),
		now:     time.Now,
	}, nil
}

type principalKey struct{}

func principal(ctx context.Context) auth.Principal {
	p, _ := ctx.Value(principalKey{}).(auth.Principal)
	return p
}

// authenticate requires a valid Bearer access token.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		tok, ok := strings.CutPrefix(h, "Bearer ")
		if !ok || tok == "" {
			httpx.WriteError(w, r, httpx.ErrUnauthorized)
			return
		}
		p, err := s.issuer.Parse(tok)
		if err != nil {
			httpx.WriteError(w, r, httpx.Errorf(http.StatusUnauthorized, "invalid_token", "access token is invalid or expired"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

// require enforces a permission. It runs on every protected route, so a
// route without a permission cannot be added by accident: see Routes.
func require(perm auth.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !auth.Can(principal(r.Context()).Role, perm) {
				httpx.WriteError(w, r, httpx.ErrForbidden.With("required_permission", string(perm)))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Routes builds the router.
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.Observe(s.log, s.metrics), httpx.SecurityHeaders, httpx.CORS(s.cfg.CORSOrigin), httpx.Timeout(s.cfg.RequestTimeout))
	r.NotFound(func(w http.ResponseWriter, r *http.Request) { httpx.WriteError(w, r, httpx.ErrNotFound) })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed"))
	})

	r.Get("/health", s.health)
	r.Handle("/metrics", promhttp.HandlerFor(s.reg, promhttp.HandlerOpts{}))
	r.Get("/openapi.yaml", serveOpenAPI)
	r.Get("/docs", serveDocs)

	w := httpx.Wrap
	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/auth/login", w(s.login))
		r.Post("/auth/refresh", w(s.refresh))
		r.Post("/auth/logout", w(s.logout))
		// devices authenticate with HMAC signatures instead of user tokens
		r.Post("/ingest/readings", w(s.ingestReadings))

		r.Group(func(r chi.Router) {
			r.Use(s.authenticate)
			r.Get("/auth/me", w(s.me))

			// a route = permission + handler; the helper keeps that pairing
			// visible in one line per endpoint
			get := func(p auth.Permission, path string, h httpx.Handler) { r.With(require(p)).Get(path, w(h)) }
			post := func(p auth.Permission, path string, h httpx.Handler) { r.With(require(p)).Post(path, w(h)) }
			// state-changing critical operations also honour Idempotency-Key
			postIdem := func(p auth.Permission, path string, h httpx.Handler) {
				r.With(require(p), s.idempotent).Post(path, w(h))
			}

			get(auth.PermLiveRead, "/live", s.liveStats)

			get(auth.PermCatalogRead, "/products", s.listProducts)
			post(auth.PermCatalogWrite, "/products", s.createProduct)
			get(auth.PermCatalogRead, "/products/{id}", s.getProduct)
			get(auth.PermCatalogRead, "/lots", s.listLots)
			postIdem(auth.PermCatalogWrite, "/lots", s.createLot)
			get(auth.PermCatalogRead, "/lots/{id}", s.getLot)
			get(auth.PermCatalogRead, "/lots/{id}/evaluation", s.lotEvaluation)
			get(auth.PermCatalogRead, "/locations", s.listLocations)
			get(auth.PermCatalogRead, "/vehicles", s.listVehicles)
			get(auth.PermCatalogRead, "/units", s.listUnits)
			get(auth.PermCatalogRead, "/units/{id}", s.getUnit)
			postIdem(auth.PermUnitsOperate, "/units/{id}/split", s.splitUnit)
			postIdem(auth.PermUnitsOperate, "/units/merge", s.mergeUnits)
			postIdem(auth.PermUnitsOperate, "/units/{id}/move", s.moveUnit)

			get(auth.PermSensorsRead, "/sensors", s.listSensors)
			get(auth.PermSensorsRead, "/sensors/health", s.sensorHealth)
			get(auth.PermSensorsRead, "/timeseries/stats", s.timeseriesStats)
			get(auth.PermSensorsRead, "/sensors/{id}", s.getSensor)
			get(auth.PermSensorsRead, "/sensors/{id}/readings", s.sensorReadings)
			get(auth.PermSensorsRead, "/sensors/{id}/evaluation", s.sensorEvaluation)
			get(auth.PermSensorsRead, "/locations/{id}/readings", s.locationReadings)

			get(auth.PermAlarmsRead, "/alarms", s.listAlarms)
			post(auth.PermAlarmsHandle, "/alarms/{id}/ack", s.ackAlarm)
			post(auth.PermAlarmsHandle, "/alarms/{id}/close", s.closeAlarm)
			get(auth.PermAlarmsRead, "/nonconformities", s.listNonconformities)
			post(auth.PermAlarmsHandle, "/nonconformities", s.createNonconformity)

			postIdem(auth.PermQualityDecide, "/lots/{id}/decisions", s.decideLot)
			get(auth.PermCatalogRead, "/lots/{id}/decisions", s.listDecisions)
			postIdem(auth.PermReportsWrite, "/lots/{id}/excursion-reports", s.createExcursionReport)
			get(auth.PermReportsRead, "/excursion-reports", s.listExcursionReports)
			get(auth.PermReportsRead, "/excursion-reports/verify", s.verifyExcursionReports)
			get(auth.PermReportsRead, "/excursion-reports/{seq}", s.getExcursionReport)

			get(auth.PermHACCPRead, "/haccp/checklists", s.listChecklists)
			get(auth.PermHACCPRead, "/haccp/records", s.listHACCPRecords)
			postIdem(auth.PermHACCPRecord, "/haccp/records", s.createHACCPRecord)

			get(auth.PermShipmentsRead, "/shipments", s.listShipments)
			postIdem(auth.PermShipmentsPlan, "/shipments", s.createShipment)
			get(auth.PermShipmentsRead, "/shipments/{id}", s.getShipment)
			post(auth.PermShipmentsPlan, "/shipments/{id}/plan", s.planShipment)
			postIdem(auth.PermShipmentsPlan, "/shipments/{id}/load", s.loadShipment)
			postIdem(auth.PermShipmentsPlan, "/shipments/{id}/depart", s.departShipment)

			get(auth.PermDriverApp, "/driver/shipments", s.driverShipments)
			post(auth.PermDriverApp, "/driver/sync", s.driverSync)

			get(auth.PermTrace, "/trace/units/{id}", s.traceUnit)
			get(auth.PermTrace, "/trace/lots/{id}", s.traceLot)
			postIdem(auth.PermRecall, "/recalls", s.createRecall)
			get(auth.PermTrace, "/recalls", s.listRecalls)
			get(auth.PermTrace, "/recalls/{id}", s.getRecall)

			get(auth.PermCustomerPortal, "/customer/shipments", s.customerShipments)
			get(auth.PermCustomerPortal, "/customer/shipments/{id}", s.customerShipment)

			get(auth.PermAuditRead, "/audit", s.listAudit)
		})
	})
	return r
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	status, dbStatus, code := "ok", "ok", http.StatusOK
	version, err := db.MigrationVersion(ctx, s.pool)
	if err != nil {
		status, dbStatus, code = "degraded", "unreachable", http.StatusServiceUnavailable
	}
	httpx.JSON(w, code, map[string]any{
		"status":            status,
		"database":          dbStatus,
		"migration_version": version,
		"ingest_buffered":   s.ingest.Buffered(),
		"time":              s.now().UTC(),
	})
}
