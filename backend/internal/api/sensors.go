package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/domain/devicesig"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/domain/excursion"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/httpx"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/ingest"
)

type ingestBody struct {
	BatteryPct *int `json:"battery_pct"`
	Readings   []struct {
		TS         time.Time `json:"ts"`
		TempCC     int       `json:"temp_cc"`
		HumidityPM int       `json:"humidity_pm"`
	} `json:"readings"`
}

// maxBacklog is how old a reading may be: devices buffer during outages and
// send the backlog later, but never older than the raw retention allows.
const maxBacklog = 6 * time.Hour

// ingestReadings accepts signed measurements from a device.
//
// Order of checks: headers -> device key -> HMAC + clock skew -> payload ->
// atomic sequence advance (replay guard) -> buffer. The sequence is advanced
// with a conditional UPDATE, so two copies of the same message racing each
// other cannot both pass.
func (s *Server) ingestReadings(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	reject := func(reason string, status int, code, msg string) error {
		s.ingest.Reject(reason)
		return httpx.Errorf(status, code, "%s", msg)
	}
	h, err := devicesig.Parse(r.Header.Get)
	if errors.Is(err, devicesig.ErrMissing) {
		return reject("unsigned", http.StatusUnauthorized, "device_unauthenticated", "device signature headers are required")
	}
	if err != nil {
		return reject("malformed", http.StatusUnauthorized, "device_unauthenticated", err.Error())
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 256<<10))
	if err != nil {
		return reject("malformed", http.StatusBadRequest, "invalid_body", "cannot read body")
	}
	sensorID, key, err := s.devices.Get(ctx, h.DeviceID)
	if errors.Is(err, ingest.ErrUnknownDevice) {
		return reject("unknown_device", http.StatusUnauthorized, "device_unauthenticated", "unknown device")
	}
	if err != nil {
		return err
	}
	now := s.now()
	if err := devicesig.Verify(key, h, body, now, s.cfg.SignatureSkew); err != nil {
		reason := "bad_signature"
		if errors.Is(err, devicesig.ErrSkew) {
			reason = "clock_skew"
		}
		s.markRejected(r, sensorID)
		return reject(reason, http.StatusUnauthorized, "device_unauthenticated", err.Error())
	}

	var in ingestBody
	if err := json.Unmarshal(body, &in); err != nil {
		return reject("malformed", http.StatusBadRequest, "invalid_json", "body is not valid JSON")
	}
	v := httpx.Validation{}
	v.Check(len(in.Readings) > 0 && len(in.Readings) <= 1000, "readings", "must contain 1-1000 readings")
	batch := make([]ingest.Reading, 0, len(in.Readings))
	for _, rd := range in.Readings {
		switch {
		case rd.TS.After(now.Add(s.cfg.SignatureSkew)) || rd.TS.Before(now.Add(-maxBacklog)):
			v.Add("readings.ts", "must be within the last 6 hours")
		case rd.TempCC < -6000 || rd.TempCC > 8000:
			v.Add("readings.temp_cc", "must be between -6000 and 8000")
		case rd.HumidityPM < 0 || rd.HumidityPM > 1000:
			v.Add("readings.humidity_pm", "must be between 0 and 1000")
		default:
			batch = append(batch, ingest.Reading{SensorID: sensorID, TS: rd.TS.Truncate(time.Millisecond), TempCC: int16(rd.TempCC), HumidityPM: int16(rd.HumidityPM)})
		}
	}
	if in.BatteryPct != nil {
		v.Check(*in.BatteryPct >= 0 && *in.BatteryPct <= 100, "battery_pct", "must be 0-100")
	}
	if err := v.Err(); err != nil {
		s.ingest.Reject("invalid_payload")
		return err
	}

	tag, err := s.pool.Exec(ctx, `
		UPDATE sensors SET last_seq = $2, last_seen_at = $3, battery_pct = COALESCE($4, battery_pct)
		WHERE id = $1 AND last_seq < $2`, sensorID, int64(h.Seq), now, in.BatteryPct)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		s.markRejected(r, sensorID)
		return reject("replay", http.StatusConflict, "replay_detected", devicesig.ErrReplay.Error())
	}
	if err := s.ingest.Add(batch); err != nil {
		// the sequence was consumed; the device must re-sign with a new one
		w.Header().Set("Retry-After", "5")
		return reject("backpressure", http.StatusServiceUnavailable, "ingest_unavailable", "ingest buffer is full, retry later")
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{"accepted": len(batch), "seq": h.Seq})
	return nil
}

func (s *Server) markRejected(r *http.Request, sensorID int32) {
	if _, err := s.pool.Exec(r.Context(), `UPDATE sensors SET rejected_count = rejected_count + 1 WHERE id = $1`, sensorID); err != nil {
		httpx.Logger(r.Context()).Warn("cannot count rejected device message", "err", err)
	}
}

type sensorRow struct {
	ID            int32      `json:"id"`
	Code          string     `json:"code"`
	LocationID    int64      `json:"location_id"`
	LocationCode  string     `json:"location_code"`
	LocationName  string     `json:"location_name"`
	LocationKind  string     `json:"location_kind"`
	LastSeenAt    *time.Time `json:"last_seen_at"`
	BatteryPct    *int16     `json:"battery_pct"`
	RejectedCount int64      `json:"rejected_count"`
	LastTempCC    *int16     `json:"last_temp_cc"`
}

const sensorSelect = `
	SELECT s.id, s.code, s.location_id, l.code, l.name, l.kind::text, s.last_seen_at, s.battery_pct, s.rejected_count,
	       (SELECT temp_cc FROM readings r WHERE r.sensor_id = s.id AND r.ts > now() - interval '15 minutes'
	        ORDER BY ts DESC LIMIT 1)
	FROM sensors s JOIN locations l ON l.id = s.location_id`

func (s *Server) listSensors(w http.ResponseWriter, r *http.Request) error {
	page, err := httpx.ParsePage(r, 50, 500)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	rows, err := s.pool.Query(r.Context(), sensorSelect+`
		WHERE s.id < $1::bigint AND ($2 = '' OR l.kind::text = $2) AND ($3 = '' OR s.code ILIKE '%' || $3 || '%' OR l.name ILIKE '%' || $3 || '%')
		ORDER BY s.id DESC LIMIT $4`, page.MaxID(), q.Get("kind"), q.Get("q"), page.Limit)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[sensorRow])
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(items, page, func(x sensorRow) int64 { return int64(x.ID) }))
	return nil
}

func (s *Server) getSensor(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	rows, err := s.pool.Query(r.Context(), sensorSelect+` WHERE s.id = $1`, id)
	if err != nil {
		return err
	}
	item, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[sensorRow])
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, item)
	return nil
}

// Point is one value of a temperature series. For raw data min = max = avg.
type Point struct {
	TS       time.Time `json:"ts"`
	AvgC     float64   `json:"avg_c"`
	MinC     float64   `json:"min_c"`
	MaxC     float64   `json:"max_c"`
	Humidity float64   `json:"humidity_pct"`
	N        int       `json:"n"`
}

// resolutionFor picks the coarsest level that still gives a useful chart.
func resolutionFor(span time.Duration) string {
	switch {
	case span <= 3*time.Hour:
		return "raw"
	case span <= 48*time.Hour:
		return "1m"
	case span <= 30*24*time.Hour:
		return "15m"
	default:
		return "1h"
	}
}

func parseRange(r *http.Request, now time.Time) (time.Time, time.Time, string, error) {
	q := r.URL.Query()
	to, from := now, now.Add(-24*time.Hour)
	var err error
	if s := q.Get("to"); s != "" {
		if to, err = time.Parse(time.RFC3339, s); err != nil {
			return from, to, "", httpx.Errorf(http.StatusBadRequest, "invalid_range", "to must be RFC 3339")
		}
	}
	if s := q.Get("from"); s != "" {
		if from, err = time.Parse(time.RFC3339, s); err != nil {
			return from, to, "", httpx.Errorf(http.StatusBadRequest, "invalid_range", "from must be RFC 3339")
		}
	} else if s := q.Get("to"); s != "" {
		from = to.Add(-24 * time.Hour)
	}
	if !from.Before(to) || to.Sub(from) > 800*24*time.Hour {
		return from, to, "", httpx.Errorf(http.StatusBadRequest, "invalid_range", "from must be before to and the span at most 800 days")
	}
	res := q.Get("resolution")
	switch res {
	case "", "auto":
		res = resolutionFor(to.Sub(from))
	case "raw", "1m", "15m", "1h":
	default:
		return from, to, "", httpx.Errorf(http.StatusBadRequest, "invalid_resolution", "resolution must be auto, raw, 1m, 15m or 1h")
	}
	if res == "raw" && to.Sub(from) > 25*time.Hour {
		return from, to, "", httpx.Errorf(http.StatusBadRequest, "invalid_range", "raw resolution is limited to 25 hours")
	}
	return from, to, res, nil
}

func seriesQuery(res string) string {
	if res == "raw" {
		return `SELECT ts, temp_cc / 100.0, temp_cc / 100.0, temp_cc / 100.0, humidity_pm / 10.0, 1
			FROM readings WHERE sensor_id = ANY($1) AND ts >= $2 AND ts < $3 ORDER BY ts`
	}
	return `SELECT bucket, temp_sum_cc::float8 / n / 100, temp_min_cc / 100.0, temp_max_cc / 100.0, hum_sum_pm::float8 / n / 10, n
		FROM readings_` + res + ` WHERE sensor_id = ANY($1) AND bucket >= $2 AND bucket < $3 ORDER BY bucket`
}

func (s *Server) series(r *http.Request, sensorIDs []int32) ([]Point, string, time.Time, time.Time, error) {
	from, to, res, err := parseRange(r, s.now())
	if err != nil {
		return nil, "", from, to, err
	}
	rows, err := s.pool.Query(r.Context(), seriesQuery(res), sensorIDs, from, to)
	if err != nil {
		return nil, "", from, to, err
	}
	pts, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Point])
	if pts == nil {
		pts = []Point{}
	}
	return pts, res, from, to, err
}

func (s *Server) sensorReadings(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	pts, res, from, to, err := s.series(r, []int32{int32(id)})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"sensor_id": id, "resolution": res, "from": from, "to": to, "points": pts})
	return nil
}

func (s *Server) locationReadings(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	rows, err := s.pool.Query(r.Context(), `SELECT id FROM sensors WHERE location_id = $1`, id)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int32])
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return httpx.ErrNotFound.With("reason", "location has no sensors")
	}
	pts, res, from, to, err := s.series(r, ids)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"location_id": id, "sensor_ids": ids, "resolution": res, "from": from, "to": to, "points": pts})
	return nil
}

// sensorHealth reports battery and connectivity (bonus requirement).
func (s *Server) sensorHealth(w http.ResponseWriter, r *http.Request) error {
	var out struct {
		Total      int         `json:"total"`
		Online     int         `json:"online"`      // seen in the last 2 minutes
		Stale      int         `json:"stale"`       // 2-15 minutes
		Offline    int         `json:"offline"`     // > 15 minutes or never
		LowBattery int         `json:"low_battery"` // < 20 %
		Rejected   int64       `json:"rejected_messages"`
		Worst      []sensorRow `json:"attention"`
	}
	err := s.pool.QueryRow(r.Context(), `
		SELECT count(*),
		       count(*) FILTER (WHERE last_seen_at > now() - interval '2 minutes'),
		       count(*) FILTER (WHERE last_seen_at <= now() - interval '2 minutes' AND last_seen_at > now() - interval '15 minutes'),
		       count(*) FILTER (WHERE last_seen_at IS NULL OR last_seen_at <= now() - interval '15 minutes'),
		       count(*) FILTER (WHERE battery_pct < 20),
		       coalesce(sum(rejected_count), 0)
		FROM sensors WHERE active`).Scan(&out.Total, &out.Online, &out.Stale, &out.Offline, &out.LowBattery, &out.Rejected)
	if err != nil {
		return err
	}
	rows, err := s.pool.Query(r.Context(), sensorSelect+`
		WHERE s.active AND (s.battery_pct < 20 OR s.last_seen_at IS NULL OR s.last_seen_at < now() - interval '2 minutes' OR s.rejected_count > 0)
		ORDER BY s.last_seen_at NULLS FIRST, s.battery_pct LIMIT 25`)
	if err != nil {
		return err
	}
	if out.Worst, err = pgx.CollectRows(rows, pgx.RowToStructByPos[sensorRow]); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) liveStats(w http.ResponseWriter, r *http.Request) error {
	var openAlarms, escalated, activeLots, inTransit int
	var lastReading *time.Time
	err := s.pool.QueryRow(r.Context(), `
		SELECT (SELECT count(*) FROM alarms WHERE status <> 'closed'),
		       (SELECT count(*) FROM alarms WHERE status = 'open' AND escalation_level > 0),
		       (SELECT count(*) FROM lots WHERE status IN ('released', 'on_hold', 'accepted', 'discounted')),
		       (SELECT count(*) FROM shipments WHERE status = 'in_transit'),
		       (SELECT max(last_seen_at) FROM sensors)`).Scan(&openAlarms, &escalated, &activeLots, &inTransit, &lastReading)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"ingest":               s.ingest.Stats(),
		"open_alarms":          openAlarms,
		"escalated_alarms":     escalated,
		"active_lots":          activeLots,
		"shipments_in_transit": inTransit,
		"last_reading_at":      lastReading,
		"server_time":          s.now().UTC(),
	})
	return nil
}

// timeseriesStats shows partitions, rollup watermarks and sizes (T-2 evidence).
func (s *Server) timeseriesStats(w http.ResponseWriter, r *http.Request) error {
	type part struct {
		Table     string `json:"table"`
		Partition string `json:"partition"`
		Bounds    string `json:"bounds"`
		EstRows   int64  `json:"estimated_rows"`
		SizeBytes int64  `json:"size_bytes"`
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT p.relname, c.relname, pg_get_expr(c.relpartbound, c.oid), greatest(c.reltuples, 0)::bigint,
		       pg_total_relation_size(c.oid)
		FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid JOIN pg_class p ON p.oid = i.inhparent
		WHERE p.relname IN ('readings', 'readings_1m', 'readings_15m', 'readings_1h')
		ORDER BY p.relname, c.relname`)
	if err != nil {
		return err
	}
	parts, err := pgx.CollectRows(rows, pgx.RowToStructByPos[part])
	if err != nil {
		return err
	}
	type wm struct {
		Level string    `json:"level"`
		Upto  time.Time `json:"upto"`
	}
	rows, err = s.pool.Query(r.Context(), `SELECT level, upto FROM rollup_watermarks ORDER BY upto`)
	if err != nil {
		return err
	}
	wms, err := pgx.CollectRows(rows, pgx.RowToStructByPos[wm])
	if err != nil {
		return err
	}
	var last24h int64
	if err := s.pool.QueryRow(r.Context(), `SELECT count(*) FROM readings WHERE ts > now() - interval '24 hours'`).Scan(&last24h); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"partitions": parts, "watermarks": wms, "raw_rows_last_24h": last24h,
		"retention": map[string]string{"raw": s.cfg.RawRetention.String(), "1m": "720h", "15m": "4320h", "1h": "17520h"},
	})
	return nil
}

// sensorEvaluation runs the excursion engine on one sensor's raw series
// against a product's rules. Useful to compare gap policies on the same data:
// ?product_id=…&policy=uncertain|interpolate&hours=24.
func (s *Server) sensorEvaluation(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	q := r.URL.Query()
	pid, err := httpx.PathID(q.Get("product_id"))
	if err != nil {
		return httpx.Validation{"product_id": "is required"}.Err()
	}
	product, err := loadProduct(r.Context(), s.pool, pid)
	if err != nil {
		return err
	}
	switch q.Get("policy") {
	case "":
	case "uncertain", "interpolate":
		product.GapPolicy = q.Get("policy")
	default:
		return httpx.Validation{"policy": "must be uncertain or interpolate"}.Err()
	}
	hours := 24
	if h := q.Get("hours"); h != "" {
		if hours, err = strconv.Atoi(h); err != nil || hours < 1 || hours > 168 {
			return httpx.Validation{"hours": "must be 1-168"}.Err()
		}
	}
	now := s.now()
	rows, err := s.pool.Query(r.Context(), `SELECT ts, temp_cc FROM readings WHERE sensor_id = $1 AND ts >= $2 ORDER BY ts`,
		id, now.Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		return err
	}
	rs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (excursion.Reading, error) {
		var rd excursion.Reading
		return rd, row.Scan(&rd.TS, &rd.TempCC)
	})
	if err != nil {
		return err
	}
	res := excursion.Evaluate(rs, product.Rules(), now)
	httpx.JSON(w, http.StatusOK, map[string]any{
		"sensor_id": id, "product": product.SKU, "readings": len(rs), "evaluation": summarise(res, product),
	})
	return nil
}
