// Package jobs contains periodic database maintenance: time-series rollups
// and partition management.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is satisfied by *pgxpool.Pool and pgx.Tx, so the seed can run rollups
// inside a transaction with a larger work_mem.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Level describes one rollup step.
type Level struct {
	Name   string // watermark key and table suffix
	Width  time.Duration
	Source string // table aggregated from
	// SQL expressions on the source table
	bucketExpr, nExpr, minExpr, maxExpr, sumExpr, humExpr, tsCol string
}

// Levels are applied in order; each one reads the previous.
var Levels = []Level{
	{Name: "1m", Width: time.Minute, Source: "readings", tsCol: "ts",
		bucketExpr: "date_trunc('minute', ts)", nExpr: "count(*)", minExpr: "min(temp_cc)", maxExpr: "max(temp_cc)",
		sumExpr: "sum(temp_cc)", humExpr: "sum(humidity_pm)"},
	{Name: "15m", Width: 15 * time.Minute, Source: "readings_1m", tsCol: "bucket",
		bucketExpr: "date_bin('15 minutes', bucket, timestamptz '2000-01-01')", nExpr: "sum(n)", minExpr: "min(temp_min_cc)",
		maxExpr: "max(temp_max_cc)", sumExpr: "sum(temp_sum_cc)", humExpr: "sum(hum_sum_pm)"},
	{Name: "1h", Width: time.Hour, Source: "readings_15m", tsCol: "bucket",
		bucketExpr: "date_trunc('hour', bucket)", nExpr: "sum(n)", minExpr: "min(temp_min_cc)",
		maxExpr: "max(temp_max_cc)", sumExpr: "sum(temp_sum_cc)", humExpr: "sum(hum_sum_pm)"},
}

func (l Level) sql() string {
	return fmt.Sprintf(`
		INSERT INTO readings_%s (sensor_id, bucket, n, temp_min_cc, temp_max_cc, temp_sum_cc, hum_sum_pm)
		SELECT sensor_id, %s, %s, %s, %s, %s, %s
		FROM %s WHERE %s >= $1 AND %s < $2
		GROUP BY 1, 2
		ORDER BY 1, 2
		ON CONFLICT (sensor_id, bucket) DO UPDATE SET
		    n = EXCLUDED.n, temp_min_cc = EXCLUDED.temp_min_cc, temp_max_cc = EXCLUDED.temp_max_cc,
		    temp_sum_cc = EXCLUDED.temp_sum_cc, hum_sum_pm = EXCLUDED.hum_sum_pm`,
		l.Name, l.bucketExpr, l.nExpr, l.minExpr, l.maxExpr, l.sumExpr, l.humExpr, l.Source, l.tsCol, l.tsCol)
}

// RollupRange computes one level for [from, to) without touching
// watermarks. The seed uses it to build 1-minute buckets hour by hour in
// parallel; from and to must be aligned to the level's width.
func RollupRange(ctx context.Context, db DB, level Level, from, to time.Time) error {
	_, err := db.Exec(ctx, level.sql(), from, to)
	return err
}

// SetWatermark records that a level is complete up to `upto`.
func SetWatermark(ctx context.Context, db DB, level string, upto time.Time) error {
	_, err := db.Exec(ctx, `INSERT INTO rollup_watermarks (level, upto) VALUES ($1, $2)
		ON CONFLICT (level) DO UPDATE SET upto = EXCLUDED.upto`, level, upto)
	return err
}

// Rollup recomputes each level from its watermark (minus one bucket, to
// absorb late readings) up to now. `dirtySince` forces recomputation from an
// earlier time, e.g. when a device flushed a backlog after an outage.
// Recomputing a bucket is idempotent because of the upsert.
func Rollup(ctx context.Context, pool DB, now time.Time, dirtySince *time.Time) error {
	var lowest *time.Time = dirtySince
	for _, l := range Levels {
		var wm time.Time
		err := pool.QueryRow(ctx, `SELECT upto FROM rollup_watermarks WHERE level = $1`, l.Name).Scan(&wm)
		if err == pgx.ErrNoRows {
			wm = now.Add(-24 * time.Hour)
		} else if err != nil {
			return err
		}
		from := wm.Add(-l.Width).Truncate(l.Width)
		if lowest != nil && lowest.Before(from) {
			from = lowest.Truncate(l.Width)
		}
		if _, err := pool.Exec(ctx, l.sql(), from, now); err != nil {
			return fmt.Errorf("rollup %s: %w", l.Name, err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO rollup_watermarks (level, upto) VALUES ($1, $2)
			ON CONFLICT (level) DO UPDATE SET upto = EXCLUDED.upto`, l.Name, now); err != nil {
			return err
		}
		// the next level must at least cover what this level just touched
		f := from
		lowest = &f
	}
	return nil
}

// Loop runs fn every interval until ctx is done, logging failures. It never
// overlaps runs of the same job.
func Loop(ctx context.Context, log *slog.Logger, name string, every time.Duration, fn func(context.Context) error) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		start := time.Now()
		jctx, cancel := context.WithTimeout(ctx, every*4)
		if err := fn(jctx); err != nil && ctx.Err() == nil {
			log.Warn("job failed", "job", name, "err", err)
		} else if d := time.Since(start); d > every {
			log.Warn("job slower than its interval", "job", name, "duration_ms", d.Milliseconds())
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
