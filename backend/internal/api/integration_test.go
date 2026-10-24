package api

// Integration tests against a real PostgreSQL. They need TEST_DATABASE_URL
// (a database they may wipe) and are skipped otherwise:
//
//	./scripts/test-integration.sh   # starts a throwaway postgres:16 container
//
// They cover what unit tests cannot: row locks under concurrency, the
// append-only triggers, the hash chain against a tampering DBA, and that no
// route is reachable without a token.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/config"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/db"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/domain/devicesig"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/ingest"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/jobs"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/seed"
)

var (
	env     *testEnv
	envOnce sync.Once
)

type testEnv struct {
	srv  *Server
	http *httptest.Server
	pool *pgxpool.Pool
	cfg  config.Config
	tok  map[string]string
	mu   sync.Mutex
}

func setup(t *testing.T) *testEnv {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; run ./scripts/test-integration.sh")
	}
	envOnce.Do(func() {
		ctx := context.Background()
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		pool, err := db.Open(ctx, url, log)
		if err != nil {
			panic(err)
		}
		if err := seed.Reset(ctx, pool); err != nil {
			panic(err)
		}
		pool.Reset()
		if err := db.Migrate(ctx, pool); err != nil {
			panic(err)
		}
		cfg := config.Config{
			DatabaseURL: url, JWTSecret: []byte(strings.Repeat("t", 32)), AccessTTL: time.Hour, RefreshTTL: time.Hour,
			DeviceKeyEncKey: bytes.Repeat([]byte{7}, 32), ProvisionSecret: []byte("integration-provisioning"),
			SignatureSkew: 2 * time.Minute, RequestTimeout: 30 * time.Second, RawRetention: 7 * 24 * time.Hour,
			EvalInterval: 30 * time.Second, EscalateAfter: 10 * time.Minute, IngestFlush: 50 * time.Millisecond,
		}
		kb, _ := devicesig.NewKeybox(cfg.DeviceKeyEncKey)
		if err := seed.Run(ctx, pool, seed.Options{Seed: 7, Sensors: 200, BackfillHours: 3, ProvisionSecret: cfg.ProvisionSecret,
			Keybox: kb, Now: time.Now(), Log: log}); err != nil {
			panic(err)
		}
		reg := prometheus.NewRegistry()
		pipe := ingest.NewPipeline(pool, log, reg, cfg.IngestFlush)
		srv, err := New(cfg, pool, log, reg, pipe)
		if err != nil {
			panic(err)
		}
		go pipe.Run(context.Background())
		env = &testEnv{srv: srv, http: httptest.NewServer(srv.Routes()), pool: pool, cfg: cfg, tok: map[string]string{}}
	})
	return env
}

type resp struct {
	status int
	body   map[string]any
	raw    []byte
}

func (e *testEnv) call(t *testing.T, role, method, path string, body any, headers ...string) resp {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.http.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if role != "" {
		req.Header.Set("Authorization", "Bearer "+e.token(t, role))
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, raw: raw}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func (e *testEnv) token(t *testing.T, role string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if tok, ok := e.tok[role]; ok {
		return tok
	}
	b, _ := json.Marshal(map[string]string{"email": role + "@sogukzincir.test", "password": seed.DemoPassword})
	res, err := http.Post(e.http.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	if out.AccessToken == "" {
		t.Fatalf("login %s failed: %d", role, res.StatusCode)
	}
	e.tok[role] = out.AccessToken
	return out.AccessToken
}

func (e *testEnv) room(t *testing.T) int64 {
	var id int64
	if err := e.pool.QueryRow(context.Background(), `SELECT id FROM locations WHERE kind = 'cold_room' ORDER BY id LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *testEnv) newLot(t *testing.T, pallets ...int) (lotID int64, units []int64) {
	t.Helper()
	r := e.call(t, "depo", "POST", "/api/v1/lots", map[string]any{
		"code": fmt.Sprintf("IT-%d", time.Now().UnixNano()), "product_id": 1, "location_id": e.room(t),
		"produced_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "pallets": pallets,
	})
	if r.status != 201 {
		t.Fatalf("create lot: %d %s", r.status, r.raw)
	}
	for _, u := range r.body["unit_ids"].([]any) {
		units = append(units, int64(u.(float64)))
	}
	return int64(r.body["id"].(float64)), units
}

func (e *testEnv) newShipment(t *testing.T) int64 {
	t.Helper()
	var vehicle int64
	// a vehicle with no active shipment and no demo driver
	if err := e.pool.QueryRow(context.Background(), `
		SELECT v.id FROM vehicles v WHERE v.driver_id IS NULL
		  AND NOT EXISTS (SELECT 1 FROM shipments s WHERE s.vehicle_id = v.id AND s.status IN ('planned', 'loading', 'in_transit'))
		ORDER BY v.id LIMIT 1`).Scan(&vehicle); err != nil {
		t.Fatal(err)
	}
	r := e.call(t, "operasyon", "POST", "/api/v1/shipments", map[string]any{"vehicle_id": vehicle, "customer_id": 3,
		"origin_id": e.room(t), "planned_departure": time.Now().UTC().Format(time.RFC3339), "transit_minutes": 60})
	if r.status != 201 {
		t.Fatalf("create shipment: %d %s", r.status, r.raw)
	}
	return int64(r.body["id"].(float64))
}

// parallel starts n calls at the same moment and returns their statuses.
func parallel(n int, fn func(i int) int) []int {
	out := make([]int, n)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate
			out[i] = fn(i)
		}(i)
	}
	close(gate)
	wg.Wait()
	return out
}

func count(xs []int, v int) int {
	n := 0
	for _, x := range xs {
		if x == v {
			n++
		}
	}
	return n
}

// ---- concurrency ----

func TestRaceSamePalletOnTenShipments(t *testing.T) {
	e := setup(t)
	_, units := e.newLot(t, 40)
	shipments := make([]int64, 10)
	for i := range shipments {
		shipments[i] = e.newShipment(t)
	}
	codes := parallel(10, func(i int) int {
		return e.call(t, "operasyon", "POST", fmt.Sprintf("/api/v1/shipments/%d/load", shipments[i]), map[string]any{"unit_ids": []int64{units[0]}}).status
	})
	if count(codes, 200) != 1 || count(codes, 409) != 9 {
		t.Fatalf("want exactly one load to win, got %v", codes)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM shipment_items WHERE unit_id = $1`, units[0]).Scan(&n)
	if n != 1 {
		t.Fatalf("pallet is on %d shipments", n)
	}
}

func TestRaceConcurrentSplitsNeverOversell(t *testing.T) {
	e := setup(t)
	lot, units := e.newLot(t, 100)
	codes := parallel(12, func(int) int {
		return e.call(t, "depo", "POST", fmt.Sprintf("/api/v1/units/%d/split", units[0]), map[string]any{"quantity": 15}).status
	})
	// 100 -> must keep > 0 on the parent: at most 6 splits of 15 (90)
	if count(codes, 201) != 6 {
		t.Fatalf("splits: %v", codes)
	}
	var total int
	_ = e.pool.QueryRow(context.Background(), `SELECT sum(quantity) FROM unit_contents WHERE lot_id = $1`, lot).Scan(&total)
	if total != 100 {
		t.Fatalf("quantity not conserved: %d", total)
	}
}

func TestRaceQualityDecisionsSerialised(t *testing.T) {
	e := setup(t)
	lot, _ := e.newLot(t, 10)
	codes := parallel(10, func(int) int {
		return e.call(t, "kalite", "POST", fmt.Sprintf("/api/v1/lots/%d/decisions", lot), map[string]any{"decision": "accept", "reason": "paralel"}).status
	})
	if count(codes, 201) != 1 || count(codes, 409) != 9 {
		t.Fatalf("accept twice is impossible, got %v", codes)
	}
}

func TestIdempotencyKeyUnderConcurrency(t *testing.T) {
	e := setup(t)
	code := fmt.Sprintf("IDEM-%d", time.Now().UnixNano())
	body := map[string]any{"code": code, "product_id": 1, "location_id": e.room(t),
		"produced_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "pallets": []int{10}}
	codes := parallel(8, func(int) int {
		return e.call(t, "depo", "POST", "/api/v1/lots", body, "Idempotency-Key", "same-key-"+code).status
	})
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM lots WHERE code = $1`, code).Scan(&n)
	if n != 1 {
		t.Fatalf("%d lots created", n)
	}
	for _, c := range codes {
		if c != 201 && c != 409 {
			t.Fatalf("unexpected status in %v", codes)
		}
	}
	again := e.call(t, "depo", "POST", "/api/v1/lots", body, "Idempotency-Key", "same-key-"+code)
	if again.status != 201 {
		t.Fatalf("replay: %d", again.status)
	}
}

func TestReplayRaceSameSequence(t *testing.T) {
	e := setup(t)
	key := devicesig.DeriveKey(e.cfg.ProvisionSecret, "S-00001")
	body, _ := json.Marshal(map[string]any{"readings": []map[string]any{{"ts": time.Now().UTC().Format(time.RFC3339), "temp_cc": 400, "humidity_pm": 800}}})
	seq := uint64(time.Now().UnixMicro())
	ts := time.Now().UnixMilli()
	sig := devicesig.Sign(key, "S-00001", seq, ts, body)
	codes := parallel(6, func(int) int {
		req, _ := http.NewRequest("POST", e.http.URL+"/api/v1/ingest/readings", bytes.NewReader(body))
		req.Header.Set(devicesig.HeaderDevice, "S-00001")
		req.Header.Set(devicesig.HeaderSeq, strconv.FormatUint(seq, 10))
		req.Header.Set(devicesig.HeaderTimestamp, strconv.FormatInt(ts, 10))
		req.Header.Set(devicesig.HeaderSignature, sig)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return -1
		}
		res.Body.Close()
		return res.StatusCode
	})
	if count(codes, 202) != 1 || count(codes, 409) != 5 {
		t.Fatalf("one copy of a message may pass, got %v", codes)
	}
}

// ---- integrity ----

func TestAuditLogIsAppendOnly(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	// row triggers only fire when there are rows: make sure both tables have some
	lot, _ := e.newLot(t, 5)
	if r := e.call(t, "kalite", "POST", fmt.Sprintf("/api/v1/lots/%d/excursion-reports", lot), nil); r.status != 201 {
		t.Fatalf("report: %d", r.status)
	}
	for _, sql := range []string{
		`UPDATE audit_log SET action = 'x'`,
		`DELETE FROM audit_log`,
		`TRUNCATE audit_log`,
		`UPDATE excursion_reports SET payload = '{}'`,
		`DELETE FROM excursion_reports`,
		`TRUNCATE excursion_reports CASCADE`,
	} {
		if _, err := e.pool.Exec(ctx, sql); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: expected append-only error, got %v", sql, err)
		}
	}
}

func TestHashChainDetectsTamperingByDBA(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	lot, _ := e.newLot(t, 5)
	for i := 0; i < 3; i++ {
		if r := e.call(t, "kalite", "POST", fmt.Sprintf("/api/v1/lots/%d/excursion-reports", lot), nil); r.status != 201 {
			t.Fatalf("report: %d %s", r.status, r.raw)
		}
	}
	if v := e.call(t, "denetci", "GET", "/api/v1/excursion-reports/verify", nil); v.body["valid"] != true {
		t.Fatalf("fresh chain invalid: %s", v.raw)
	}
	var seq int64
	_ = e.pool.QueryRow(ctx, `SELECT max(seq) - 1 FROM excursion_reports`).Scan(&seq)
	// a superuser can switch the trigger off; the chain still exposes the edit
	for _, sql := range []string{
		`ALTER TABLE excursion_reports DISABLE TRIGGER excursion_reports_append_only`,
		fmt.Sprintf(`UPDATE excursion_reports SET payload = jsonb_set(payload, '{lot,status}', '"accepted"') WHERE seq = %d`, seq),
		`ALTER TABLE excursion_reports ENABLE TRIGGER excursion_reports_append_only`,
	} {
		if _, err := e.pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	v := e.call(t, "denetci", "GET", "/api/v1/excursion-reports/verify", nil)
	problem, _ := v.body["problem"].(map[string]any)
	if v.body["valid"] != false || problem == nil || int64(problem["seq"].(float64)) != seq {
		t.Fatalf("tampering at seq %d not detected: %s", seq, v.raw)
	}
}

// ---- jobs ----

func TestEscalationRaisesLevelsOverTime(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	var id int64
	err := e.pool.QueryRow(ctx, `INSERT INTO alarms (dedup_key, sensor_id, location_id, rule, message, opened_at, last_seen_at)
		SELECT 'it-escalate-' || now()::text, s.id, s.location_id, 'limit_high', 'test', now() - interval '25 minutes', now()
		FROM sensors s LIMIT 1 RETURNING id`).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	ev := &Evaluator{s: e.srv, raised: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "x"}, []string{"rule"}), escalated: prometheus.NewCounter(prometheus.CounterOpts{Name: "y"})}
	if err := ev.Escalate(ctx); err != nil {
		t.Fatal(err)
	}
	var lvl int
	_ = e.pool.QueryRow(ctx, `SELECT escalation_level FROM alarms WHERE id = $1`, id).Scan(&lvl)
	if lvl != 2 {
		t.Fatalf("25 min with a 10 min step must be level 2, got %d", lvl)
	}
	// acknowledged alarms stop escalating
	_, _ = e.pool.Exec(ctx, `UPDATE alarms SET status = 'acknowledged', acknowledged_at = now(), opened_at = now() - interval '2 hours' WHERE id = $1`, id)
	_ = ev.Escalate(ctx)
	_ = e.pool.QueryRow(ctx, `SELECT escalation_level FROM alarms WHERE id = $1`, id).Scan(&lvl)
	if lvl != 2 {
		t.Fatalf("acknowledged alarm escalated to %d", lvl)
	}
}

func TestSensorPassDeduplicatesAlarms(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	ev := &Evaluator{s: e.srv, raised: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "x"}, []string{"rule"}), escalated: prometheus.NewCounter(prometheus.CounterOpts{Name: "y"})}
	for i := 0; i < 3; i++ {
		if err := ev.SensorPass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var dups int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT dedup_key FROM alarms WHERE status <> 'closed' GROUP BY 1 HAVING count(*) > 1) d`).Scan(&dups)
	if dups != 0 {
		t.Fatalf("%d duplicated live alarms", dups)
	}
}

func TestRollupLevelsAreConsistent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	now := time.Now()
	if err := jobs.Rollup(ctx, e.pool, now, nil); err != nil {
		t.Fatal(err)
	}
	from := now.Add(-2 * time.Hour).Truncate(time.Hour)
	to := now.Add(-time.Hour).Truncate(time.Hour)
	var raw, m1, m15, h1 int64
	q := func(sql string, dst *int64) {
		if err := e.pool.QueryRow(ctx, sql, from, to).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	q(`SELECT count(*) FROM readings WHERE ts >= $1 AND ts < $2`, &raw)
	q(`SELECT coalesce(sum(n), 0) FROM readings_1m WHERE bucket >= $1 AND bucket < $2`, &m1)
	q(`SELECT coalesce(sum(n), 0) FROM readings_15m WHERE bucket >= $1 AND bucket < $2`, &m15)
	q(`SELECT coalesce(sum(n), 0) FROM readings_1h WHERE bucket >= $1 AND bucket < $2`, &h1)
	if raw == 0 || raw != m1 || m1 != m15 || m15 != h1 {
		t.Fatalf("raw %d, 1m %d, 15m %d, 1h %d must match", raw, m1, m15, h1)
	}
}

// ---- authorisation ----

func TestEveryAPIRouteRequiresAuthentication(t *testing.T) {
	e := setup(t)
	public := map[string]bool{
		"POST /api/v1/auth/login": true, "POST /api/v1/auth/refresh": true, "POST /api/v1/auth/logout": true,
		"POST /api/v1/ingest/readings": true, // HMAC instead of a user token, tested separately
	}
	n := 0
	err := chi.Walk(e.srv.Routes().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/v1/") || public[method+" "+route] {
			return nil
		}
		path := strings.NewReplacer("{id}", "1", "{seq}", "1").Replace(route)
		if r := e.call(t, "", method, path, map[string]any{}); r.status != 401 {
			t.Errorf("%s %s without token: %d", method, route, r.status)
		}
		n++
		return nil
	})
	if err != nil || n < 50 {
		t.Fatalf("walked %d routes, err %v", n, err)
	}
}

func TestRecallTraceUnderOneSecond(t *testing.T) {
	e := setup(t)
	var lot int64
	_ = e.pool.QueryRow(context.Background(), `SELECT id FROM lots WHERE code = 'LOT-TRACE-001'`).Scan(&lot)
	start := time.Now()
	r := e.call(t, "kalite", "GET", fmt.Sprintf("/api/v1/trace/lots/%d", lot), nil)
	if r.status != 200 || time.Since(start) > time.Second {
		t.Fatalf("trace: %d in %s", r.status, time.Since(start))
	}
	if n := len(r.body["customers"].([]any)); n != 4 {
		t.Fatalf("customers = %d", n)
	}
}
