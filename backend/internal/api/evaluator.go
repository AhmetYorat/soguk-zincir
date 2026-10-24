package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/domain/excursion"
)

// Evaluator turns readings into alarms.
//
//   - Sensor pass (every EvalInterval): each sensor at a location that holds
//     goods is evaluated over the last window against the strictest product
//     stored there. Live conditions (reading out of range now, window rule,
//     sensor silent) raise alarms.
//   - Lot pass (every 2 minutes): every lot still in our custody is evaluated
//     over its whole history; cumulative excursions put the lot on hold and the
//     remaining shelf life is cached for planning.
//   - Escalation: open alarms nobody acknowledged climb one level every
//     EscalateAfter (1 = quality manager, 2 = operations manager, 3 = all).
//
// Alarms are de-duplicated by dedup_key with a partial unique index over
// non-closed alarms: while an alarm is open or acknowledged, the same
// condition only refreshes it. After it is closed, a new occurrence opens a
// new alarm.
type Evaluator struct {
	s         *Server
	raised    *prometheus.CounterVec
	escalated prometheus.Counter
}

// NewEvaluator registers the evaluator metrics.
func (s *Server) NewEvaluator() *Evaluator {
	e := &Evaluator{
		s: s,
		raised: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "alarms_raised_total", Help: "New alarms by rule.",
		}, []string{"rule"}),
		escalated: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "alarms_escalated_total", Help: "Alarm escalation steps.",
		}),
	}
	s.reg.MustRegister(e.raised, e.escalated)
	return e
}

type alarmUpsert struct {
	key        string
	sensorID   *int32
	locationID *int64
	lotID      *int64
	productID  int64
	rule       string
	peakCC     *int
	excursionS int
	message    string
	at         time.Time
}

func (e *Evaluator) upsert(ctx context.Context, alarms []alarmUpsert) error {
	if len(alarms) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, a := range alarms {
		b.Queue(`
			INSERT INTO alarms (dedup_key, sensor_id, location_id, lot_id, product_id, rule, peak_cc, excursion_s, message, opened_at, last_seen_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)
			ON CONFLICT (dedup_key) WHERE status <> 'closed' DO UPDATE SET
			    last_seen_at = EXCLUDED.last_seen_at,
			    peak_cc = GREATEST(alarms.peak_cc, EXCLUDED.peak_cc),
			    excursion_s = GREATEST(alarms.excursion_s, EXCLUDED.excursion_s)
			RETURNING (xmax = 0)`, // true when the row was inserted, i.e. a new alarm
			a.key, a.sensorID, a.locationID, a.lotID, a.productID, a.rule, a.peakCC, a.excursionS, a.message, a.at)
	}
	br := e.s.pool.SendBatch(ctx, b)
	defer br.Close()
	for _, a := range alarms {
		var inserted bool
		if err := br.QueryRow().Scan(&inserted); err != nil {
			return err
		}
		if inserted {
			e.raised.WithLabelValues(a.rule).Inc()
		}
	}
	return nil
}

// governing finds, per location, the product with the tightest upper limit
// among the goods currently there.
func (e *Evaluator) governing(ctx context.Context) (map[int64]Product, error) {
	rows, err := e.s.pool.Query(ctx, `
		SELECT DISTINCT ON (u.location_id) u.location_id, `+prefixed("p", productCols)+`
		FROM units u
		JOIN unit_contents c ON c.unit_id = u.id AND c.quantity > 0
		JOIN lots lo ON lo.id = c.lot_id
		JOIN products p ON p.id = lo.product_id
		WHERE u.status IN ('active', 'loaded')
		ORDER BY u.location_id, p.temp_max_cc, p.temp_min_cc DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]Product{}
	for rows.Next() {
		var loc int64
		var p Product
		if err := rows.Scan(&loc, &p.ID, &p.SKU, &p.Name, &p.TempMinCC, &p.TempMaxCC, &p.MaxCumulativeExcursionS, &p.WindowS,
			&p.WindowMaxExcursionS, &p.ShelfLifeH, &p.ActivationEnergyJMol, &p.GapPolicy, &p.MaxGapS, &p.UnitWeightG,
			&p.ListPriceKurus, &p.CreatedAt); err != nil {
			return nil, err
		}
		out[loc] = p
	}
	return out, rows.Err()
}

// SensorPass evaluates live conditions.
func (e *Evaluator) SensorPass(ctx context.Context) error {
	gov, err := e.governing(ctx)
	if err != nil {
		return err
	}
	if len(gov) == 0 {
		return nil
	}
	var locs []int64
	maxWindow := time.Hour
	for loc, p := range gov {
		locs = append(locs, loc)
		if w := time.Duration(p.WindowS) * time.Second; w > maxWindow {
			maxWindow = w
		}
	}
	if maxWindow > 6*time.Hour {
		maxWindow = 6 * time.Hour
	}
	now := e.s.now()
	since := now.Add(-maxWindow)
	rows, err := e.s.pool.Query(ctx, `
		SELECT s.id, s.code, s.location_id, r.ts, r.temp_cc
		FROM sensors s LEFT JOIN readings r ON r.sensor_id = s.id AND r.ts > $2
		WHERE s.active AND s.location_id = ANY($1)
		ORDER BY s.id, r.ts`, locs, since)
	if err != nil {
		return err
	}
	type series struct {
		code string
		loc  int64
		rs   []excursion.Reading
	}
	bySensor := map[int32]*series{}
	var order []int32
	for rows.Next() {
		var id int32
		var code string
		var loc int64
		var ts *time.Time
		var temp *int16
		if err := rows.Scan(&id, &code, &loc, &ts, &temp); err != nil {
			rows.Close()
			return err
		}
		sr, ok := bySensor[id]
		if !ok {
			sr = &series{code: code, loc: loc}
			bySensor[id] = sr
			order = append(order, id)
		}
		if ts != nil {
			sr.rs = append(sr.rs, excursion.Reading{TS: *ts, TempCC: int(*temp)})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	var out []alarmUpsert
	var reporting []int32 // sensors whose data flows again
	for _, id := range order {
		sr := bySensor[id]
		p := gov[sr.loc]
		rules := p.Rules()
		sid, loc := id, sr.loc
		base := alarmUpsert{sensorID: &sid, locationID: &loc, productID: p.ID, at: now}
		key := func(rule string) string { return fmt.Sprintf("sensor:%d:%s:%d", id, rule, p.ID) }

		if len(sr.rs) == 0 {
			a := base
			a.key, a.rule = key(excursion.RuleGap), excursion.RuleGap
			a.message = fmt.Sprintf("%s: %s boyunca hiç ölçüm yok", sr.code, dur(maxWindow))
			out = append(out, a)
			continue
		}
		res := excursion.Evaluate(sr.rs, rules, now)
		last := sr.rs[len(sr.rs)-1]
		// Limits are checked on every reading since the previous pass (plus
		// the gap tolerance), not only the latest one: a short spike between
		// two passes must still raise an alarm.
		recent := now.Add(-e.s.cfg.EvalInterval - rules.MaxGap)
		var high, low *excursion.Reading
		for i := range sr.rs {
			rd := &sr.rs[i]
			if rd.TS.Before(recent) {
				continue
			}
			if rd.TempCC > rules.MaxCC && (high == nil || rd.TempCC > high.TempCC) {
				high = rd
			}
			if rd.TempCC < rules.MinCC && (low == nil || rd.TempCC < low.TempCC) {
				low = rd
			}
		}
		if high != nil {
			peak := high.TempCC
			a := base
			a.key, a.rule, a.peakCC = key(excursion.RuleLimitHigh), excursion.RuleLimitHigh, &peak
			a.excursionS = int(res.HighDuration.Seconds())
			a.message = fmt.Sprintf("%s: %.2f °C — %s için üst limit %.1f °C aşıldı", sr.code, float64(peak)/100, p.Name, float64(rules.MaxCC)/100)
			out = append(out, a)
		}
		if low != nil {
			v := low.TempCC
			a := base
			a.key, a.rule, a.peakCC = key(excursion.RuleLimitLow), excursion.RuleLimitLow, &v
			a.excursionS = int(res.LowDuration.Seconds())
			a.message = fmt.Sprintf("%s: %.2f °C — %s için alt limit %.1f °C'nin altında", sr.code, float64(v)/100, p.Name, float64(rules.MinCC)/100)
			out = append(out, a)
		}
		if now.Sub(last.TS) <= rules.MaxGap {
			reporting = append(reporting, id)
		}
		if now.Sub(last.TS) > rules.MaxGap {
			a := base
			a.key, a.rule = key(excursion.RuleGap), excursion.RuleGap
			a.excursionS = int(now.Sub(last.TS).Seconds())
			a.message = fmt.Sprintf("%s: %s süredir ölçüm gelmiyor (son: %s)", sr.code, dur(now.Sub(last.TS)), last.TS.UTC().Format("15:04:05 UTC"))
			out = append(out, a)
		}
		peak := res.PeakCC
		if res.Breached(excursion.RuleWindow) {
			a := base
			a.key, a.rule, a.peakCC = key(excursion.RuleWindow), excursion.RuleWindow, &peak
			a.excursionS = int(res.MaxWindowExcursion.Seconds())
			a.message = fmt.Sprintf("%s: %s içinde %s limit dışında (izin %s) — %s", sr.code,
				dur(rules.Window), dur(res.MaxWindowExcursion), dur(rules.WindowMax), p.Name)
			out = append(out, a)
		}
	}
	if err := e.upsert(ctx, out); err != nil {
		return err
	}
	// A connectivity alarm resolves itself when the sensor reports again;
	// the closed alarm stays as a record. Temperature alarms always need a
	// person to close them.
	if len(reporting) > 0 {
		if _, err := e.s.pool.Exec(ctx, `UPDATE alarms SET status = 'closed', closed_at = $2
			WHERE rule = 'gap' AND status <> 'closed' AND sensor_id = ANY($1)`, reporting, now); err != nil {
			return err
		}
	}
	return nil
}

// LotPass refreshes lot shelf life and raises lot-level alarms.
func (e *Evaluator) LotPass(ctx context.Context) error {
	rows, err := e.s.pool.Query(ctx, `
		SELECT DISTINCT lo.id FROM lots lo
		JOIN unit_contents c ON c.lot_id = lo.id AND c.quantity > 0
		JOIN units u ON u.id = c.unit_id AND u.status IN ('active', 'loaded')
		WHERE lo.status IN ('released', 'on_hold', 'accepted', 'discounted')`)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := e.evaluateOneLot(ctx, id); err != nil {
			return fmt.Errorf("lot %d: %w", id, err)
		}
	}
	return nil
}

func (e *Evaluator) evaluateOneLot(ctx context.Context, lotID int64) error {
	ev, err := e.s.evaluateLot(ctx, e.s.pool, lotID)
	if err != nil || ev.Worst == nil {
		return err
	}
	w := ev.Worst
	remaining := int64(w.estimate.Remaining.Seconds())
	if _, err := e.s.pool.Exec(ctx, `UPDATE lots SET remaining_shelf_life_s = $2, shelf_life_computed_at = $3 WHERE id = $1`,
		lotID, remaining, ev.EvaluatedAt); err != nil {
		return err
	}
	var out []alarmUpsert
	lot := lotID
	if w.result.Breached(excursion.RuleCumulative) {
		out = append(out, alarmUpsert{
			key: fmt.Sprintf("lot:%d:cumulative", lotID), lotID: &lot, productID: ev.Product.ID, rule: excursion.RuleCumulative,
			excursionS: int(w.result.ExcursionDuration.Seconds()), at: ev.EvaluatedAt,
			message: fmt.Sprintf("Parti %s (%s): toplam %s limit dışında, izin verilen %s — parti beklemeye alındı", ev.LotCode, w.UnitCode,
				dur(w.result.ExcursionDuration), dur(time.Duration(ev.Product.MaxCumulativeExcursionS)*time.Second)),
		})
		// goods with a broken cold chain must not ship before a quality decision
		if _, err := e.s.pool.Exec(ctx, `UPDATE lots SET status = 'on_hold' WHERE id = $1 AND status = 'released'`, lotID); err != nil {
			return err
		}
	}
	if w.estimate.Expired {
		out = append(out, alarmUpsert{
			key: fmt.Sprintf("lot:%d:shelf_life", lotID), lotID: &lot, productID: ev.Product.ID, rule: "shelf_life",
			at: ev.EvaluatedAt, message: fmt.Sprintf("Parti %s raf ömrünü doldurdu (kalan %.1f sa)", ev.LotCode, w.estimate.Remaining.Hours()),
		})
	}
	return e.upsert(ctx, out)
}

// Escalate raises the level of unacknowledged alarms.
func (e *Evaluator) Escalate(ctx context.Context) error {
	step := e.s.cfg.EscalateAfter.Seconds()
	rows, err := e.s.pool.Query(ctx, `
		UPDATE alarms SET escalation_level = lvl, escalated_at = now()
		FROM (SELECT id, LEAST(3, floor(extract(epoch FROM now() - opened_at) / $1))::smallint AS lvl
		      FROM alarms WHERE status = 'open') x
		WHERE alarms.id = x.id AND x.lvl > alarms.escalation_level
		RETURNING alarms.id, alarms.escalation_level, alarms.message`, step)
	if err != nil {
		return err
	}
	defer rows.Close()
	notify := map[int16]string{1: "quality_manager", 2: "operations_manager", 3: "all_roles"}
	for rows.Next() {
		var id int64
		var lvl int16
		var msg string
		if err := rows.Scan(&id, &lvl, &msg); err != nil {
			return err
		}
		e.escalated.Inc()
		// a real deployment would page/e-mail here; the log line is the hook
		e.s.log.Warn("alarm escalated", "alarm_id", id, "level", lvl, "notify", notify[lvl], "message", msg)
	}
	return rows.Err()
}

// Housekeeping removes expired idempotency keys and refresh tokens.
func (e *Evaluator) Housekeeping(ctx context.Context) error {
	if _, err := e.s.pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE created_at < now() - interval '24 hours'`); err != nil {
		return err
	}
	_, err := e.s.pool.Exec(ctx, `DELETE FROM refresh_tokens WHERE expires_at < now() - interval '1 day'`)
	return err
}

func prefixed(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// dur formats a duration for alarm messages: "1 sa 5 dk", "8 dk 26 sn".
func dur(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, sec := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	switch {
	case h > 0:
		return fmt.Sprintf("%d sa %d dk", h, m)
	case m > 0 && sec > 0:
		return fmt.Sprintf("%d dk %d sn", m, sec)
	case m > 0:
		return fmt.Sprintf("%d dk", m)
	default:
		return fmt.Sprintf("%d sn", sec)
	}
}
