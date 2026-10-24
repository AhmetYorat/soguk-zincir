package db

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Retention describes how long each time-series level is kept.
type Retention struct {
	Raw, OneMinute, FifteenMinute, OneHour time.Duration
}

// DefaultRetention: raw data for a week, 1-minute rollups for a month,
// 15-minute for half a year, hourly for two years (HACCP records are usually
// kept at least as long as the product's shelf life plus a margin).
func DefaultRetention(raw time.Duration) Retention {
	return Retention{Raw: raw, OneMinute: 30 * 24 * time.Hour, FifteenMinute: 180 * 24 * time.Hour, OneHour: 730 * 24 * time.Hour}
}

type partitioned struct {
	table   string
	monthly bool
	keep    time.Duration
}

// MaintainPartitions creates partitions from `back` before now until two
// periods ahead and drops partitions entirely older than the retention.
// Dropping a partition is O(1) compared with DELETE on millions of rows.
func MaintainPartitions(ctx context.Context, pool *pgxpool.Pool, now time.Time, back time.Duration, ret Retention, log *slog.Logger) error {
	tables := []partitioned{
		{"readings", false, ret.Raw},
		{"readings_1m", false, ret.OneMinute},
		{"readings_15m", true, ret.FifteenMinute},
		{"readings_1h", true, ret.OneHour},
	}
	for _, t := range tables {
		start := now.Add(-back)
		if keepStart := now.Add(-t.keep); keepStart.After(start) {
			start = keepStart
		}
		// current period plus two ahead, so inserts never hit a missing partition
		end := next(next(next(periodStart(now, t.monthly), t.monthly), t.monthly), t.monthly)
		for p := periodStart(start, t.monthly); p.Before(end); p = next(p, t.monthly) {
			name := partitionName(t.table, p, t.monthly)
			sql := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')`,
				name, t.table, p.Format(time.RFC3339), next(p, t.monthly).Format(time.RFC3339))
			if _, err := pool.Exec(ctx, sql); err != nil {
				return fmt.Errorf("create partition %s: %w", name, err)
			}
		}
		if err := dropExpired(ctx, pool, t, now, log); err != nil {
			return err
		}
	}
	return nil
}

func dropExpired(ctx context.Context, pool *pgxpool.Pool, t partitioned, now time.Time, log *slog.Logger) error {
	rows, err := pool.Query(ctx, `
		SELECT c.relname FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		WHERE p.relname = $1`, t.table)
	if err != nil {
		return err
	}
	names, err := collectStrings(rows)
	if err != nil {
		return err
	}
	cutoff := now.Add(-t.keep)
	for _, n := range names {
		start, ok := parsePartition(t.table, n, t.monthly)
		if !ok {
			continue
		}
		if !next(start, t.monthly).After(cutoff) {
			if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS "+n); err != nil {
				return fmt.Errorf("drop partition %s: %w", n, err)
			}
			log.Info("dropped expired partition", "partition", n)
		}
	}
	return nil
}

func collectStrings(rows pgx.Rows) ([]string, error) {
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func periodStart(t time.Time, monthly bool) time.Time {
	t = t.UTC()
	if monthly {
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func next(t time.Time, monthly bool) time.Time {
	if monthly {
		return t.AddDate(0, 1, 0)
	}
	return t.AddDate(0, 0, 1)
}

func partitionName(table string, start time.Time, monthly bool) string {
	if monthly {
		return fmt.Sprintf("%s_p%s", table, start.Format("200601"))
	}
	return fmt.Sprintf("%s_p%s", table, start.Format("20060102"))
}

func parsePartition(table, name string, monthly bool) (time.Time, bool) {
	suffix, ok := strings.CutPrefix(name, table+"_p")
	if !ok {
		return time.Time{}, false
	}
	layout := "20060102"
	if monthly {
		layout = "200601"
	}
	t, err := time.ParseInLocation(layout, suffix, time.UTC)
	return t, err == nil
}
