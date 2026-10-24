// Package db holds the connection pool, migrations, transactions and
// partition maintenance.
package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open connects with retries so that the API survives a database that is
// still starting or briefly unavailable.
func Open(ctx context.Context, url string, log *slog.Logger) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 20
	cfg.HealthCheckPeriod = 10 * time.Second
	var pool *pgxpool.Pool
	for attempt := 1; ; attempt++ {
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err = pool.Ping(pingCtx)
			cancel()
			if err == nil {
				return pool, nil
			}
			pool.Close()
		}
		if attempt >= 30 {
			return nil, fmt.Errorf("database unreachable after %d attempts: %w", attempt, err)
		}
		log.Warn("database not ready, retrying", "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// Migrate applies all pending migrations.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	goose.SetLogger(goose.NopLogger())
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	return goose.UpContext(ctx, sqlDB, "migrations")
}

// MigrationVersion returns the current schema version.
func MigrationVersion(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	goose.SetBaseFS(migrations)
	_ = goose.SetDialect("postgres")
	return goose.GetDBVersionContext(ctx, sqlDB)
}

// InTx runs fn in a transaction and commits if it returns nil.
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, pool, fn)
}

// Serializable runs fn at SERIALIZABLE isolation and retries on
// serialization failures and deadlocks (40001 / 40P01).
func Serializable(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		err = pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, fn)
		if !IsRetryable(err) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 15 * time.Millisecond)
	}
	return err
}

// IsRetryable reports serialization failures and deadlocks.
func IsRetryable(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && (pg.Code == "40001" || pg.Code == "40P01")
}

// IsUniqueViolation reports a unique constraint error, optionally for a
// specific constraint.
func IsUniqueViolation(err error, constraint string) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505" && (constraint == "" || pg.ConstraintName == constraint)
}

// IsConstraintViolation reports a CHECK, foreign key or NOT NULL violation.
func IsConstraintViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && (pg.Code == "23514" || pg.Code == "23503" || pg.Code == "23502")
}
