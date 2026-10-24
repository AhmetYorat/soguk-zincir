// Command api runs the SoğukZincir backend.
//
//	api serve        HTTP API + background jobs (default)
//	api migrate      apply migrations, seed demo data if the database is empty
//	api reset        drop everything, migrate and seed again (make reset-demo)
//	api healthcheck  exit 0 if GET /health answers 200 (for Docker healthchecks)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/api"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/config"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/db"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/domain/devicesig"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/ingest"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/jobs"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/seed"
)

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	if cmd == "healthcheck" {
		os.Exit(healthcheck())
	}
	cfg, err := config.Load()
	log := newLogger(cfg.LogLevel)
	if err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "serve":
		err = serve(ctx, cfg, log)
	case "migrate":
		err = migrate(ctx, cfg, log, false)
	case "reset":
		err = migrate(ctx, cfg, log, true)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		log.Error("exiting", "cmd", cmd, "err", err)
		os.Exit(1)
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(strings.ToUpper(level)))
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
	slog.SetDefault(log)
	return log
}

func migrate(ctx context.Context, cfg config.Config, log *slog.Logger, reset bool) error {
	pool, err := db.Open(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	if reset {
		log.Info("dropping all data for a demo reset")
		if err := seed.Reset(ctx, pool); err != nil {
			return err
		}
		// pooled connections cache the old schema's types
		pool.Reset()
	}
	if err := db.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	v, _ := db.MigrationVersion(ctx, pool)
	log.Info("migrations applied", "version", v)
	seeded, err := seed.IsSeeded(ctx, pool)
	if err != nil {
		return err
	}
	if seeded {
		log.Info("database already seeded, skipping")
		return nil
	}
	kb, err := devicesig.NewKeybox(cfg.DeviceKeyEncKey)
	if err != nil {
		return err
	}
	return seed.Run(ctx, pool, seed.Options{
		Seed: cfg.Seed, Sensors: cfg.SensorCount, BackfillHours: cfg.BackfillHour,
		ProvisionSecret: cfg.ProvisionSecret, Keybox: kb, Now: time.Now(), Log: log,
	})
}

func serve(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	pool, err := db.Open(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	pipe := ingest.NewPipeline(pool, log, reg, cfg.IngestFlush)
	srv, err := api.New(cfg, pool, log, reg, pipe)
	if err != nil {
		return err
	}
	ev := srv.NewEvaluator()

	bg, cancelBG := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { pipe.Run(bg); close(done) }()
	go jobs.Loop(bg, log, "partitions", time.Hour, func(ctx context.Context) error {
		return db.MaintainPartitions(ctx, pool, time.Now(), 24*time.Hour, db.DefaultRetention(cfg.RawRetention), log)
	})
	go jobs.Loop(bg, log, "rollup", 30*time.Second, func(ctx context.Context) error {
		return jobs.Rollup(ctx, pool, time.Now(), pipe.TakeDirty())
	})
	go jobs.Loop(bg, log, "sensor-eval", cfg.EvalInterval, ev.SensorPass)
	go jobs.Loop(bg, log, "lot-eval", 2*time.Minute, ev.LotPass)
	go jobs.Loop(bg, log, "escalation", 30*time.Second, ev.Escalate)
	go jobs.Loop(bg, log, "housekeeping", time.Hour, ev.Housekeeping)

	hs := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.HTTPAddr)
		errc <- hs.ListenAndServe()
	}()

	select {
	case err := <-errc:
		cancelBG()
		return err
	case <-ctx.Done():
	}
	// Graceful shutdown: stop accepting, finish in-flight requests, then stop
	// jobs and flush buffered readings before the pool closes.
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := hs.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("http shutdown", "err", err)
	}
	cancelBG()
	<-done
	log.Info("stopped")
	return nil
}

func healthcheck() int {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" || strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
		if addr == "127.0.0.1" {
			addr += ":8080"
		}
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + addr + "/health")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
