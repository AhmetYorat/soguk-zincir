// Package ingest buffers accepted sensor readings and writes them in batches.
//
// Writing every request as its own INSERT would cost a round trip and a
// commit per message (≈67 msg/s at 2 000 sensors / 30 s). Instead accepted
// readings go into a bounded in-memory buffer and are flushed every second
// with COPY into a temporary table followed by INSERT … ON CONFLICT DO
// NOTHING, which makes re-sent readings harmless.
//
// If the database is down the buffer keeps the readings and retries; when it
// is full, the API answers 503 and devices retry later with their own buffer.
package ingest

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/domain/devicesig"
)

// Reading is a validated measurement waiting to be written.
type Reading struct {
	SensorID   int32
	TS         time.Time
	TempCC     int16
	HumidityPM int16
}

// ErrBufferFull is returned when the backlog exceeds its limit.
var ErrBufferFull = errors.New("ingest buffer full")

// Pipeline is the buffered writer.
type Pipeline struct {
	pool     *pgxpool.Pool
	log      *slog.Logger
	maxBuf   int
	interval time.Duration

	mu        sync.Mutex
	buf       []Reading
	lastFlush time.Time
	dirty     time.Time // oldest reading written since the last TakeDirty
	recent    [60]int64 // accepted readings per second, ring buffer
	recentAt  [60]int64 // unix second each slot belongs to

	accepted *prometheus.CounterVec
	rejected *prometheus.CounterVec
	flushDur prometheus.Histogram
	backlog  prometheus.GaugeFunc
}

// NewPipeline creates the pipeline and registers its metrics.
func NewPipeline(pool *pgxpool.Pool, log *slog.Logger, reg prometheus.Registerer, interval time.Duration) *Pipeline {
	p := &Pipeline{pool: pool, log: log, maxBuf: 500_000, interval: interval}
	p.accepted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ingest_readings_accepted_total", Help: "Sensor readings accepted.",
	}, []string{})
	p.rejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ingest_requests_rejected_total", Help: "Device requests rejected, by reason.",
	}, []string{"reason"})
	p.flushDur = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "ingest_flush_duration_seconds", Help: "Time to write one batch.",
		Buckets: prometheus.ExponentialBuckets(0.005, 2, 10),
	})
	p.backlog = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "ingest_buffer_readings", Help: "Readings waiting to be written.",
	}, func() float64 { return float64(p.Buffered()) })
	reg.MustRegister(p.accepted, p.rejected, p.flushDur, p.backlog)
	return p
}

// Add queues readings.
func (p *Pipeline) Add(rs []Reading) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.buf)+len(rs) > p.maxBuf {
		return ErrBufferFull
	}
	p.buf = append(p.buf, rs...)
	now := time.Now().Unix()
	slot := now % 60
	if p.recentAt[slot] != now {
		p.recentAt[slot], p.recent[slot] = now, 0
	}
	p.recent[slot] += int64(len(rs))
	p.accepted.WithLabelValues().Add(float64(len(rs)))
	return nil
}

// Reject counts a rejected device request.
func (p *Pipeline) Reject(reason string) { p.rejected.WithLabelValues(reason).Inc() }

// Buffered returns the backlog size.
func (p *Pipeline) Buffered() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.buf)
}

// Stats for the live dashboard.
type Stats struct {
	PerSecond float64   `json:"readings_per_second"`
	LastMin   int64     `json:"readings_last_minute"`
	Buffered  int       `json:"buffered"`
	LastFlush time.Time `json:"last_flush"`
}

// Stats summarises the last minute.
func (p *Pipeline) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now().Unix()
	var total int64
	for i := range p.recent {
		if now-p.recentAt[i] < 60 {
			total += p.recent[i]
		}
	}
	return Stats{PerSecond: float64(total) / 60, LastMin: total, Buffered: len(p.buf), LastFlush: p.lastFlush}
}

// Run flushes until ctx is cancelled, then makes a final attempt.
func (p *Pipeline) Run(ctx context.Context) {
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := p.Flush(fctx); err != nil {
				p.log.Error("final ingest flush failed", "err", err, "lost", p.Buffered())
			}
			cancel()
			return
		case <-t.C:
			if err := p.Flush(ctx); err != nil {
				p.log.Warn("ingest flush failed, will retry", "err", err, "buffered", p.Buffered())
			}
		}
	}
}

// TakeDirty returns the oldest timestamp written since the previous call, so
// rollups can recompute buckets that received late data.
func (p *Pipeline) TakeDirty() *time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dirty.IsZero() {
		return nil
	}
	d := p.dirty
	p.dirty = time.Time{}
	return &d
}

const batchSize = 20_000

// Flush writes the buffer in batches. On failure the batch is put back.
func (p *Pipeline) Flush(ctx context.Context) error {
	for {
		p.mu.Lock()
		n := min(len(p.buf), batchSize)
		batch := p.buf[:n:n]
		p.buf = p.buf[n:]
		p.mu.Unlock()
		if n == 0 {
			return nil
		}
		start := time.Now()
		if err := p.write(ctx, batch); err != nil {
			p.mu.Lock()
			p.buf = append(batch, p.buf...)
			p.mu.Unlock()
			return err
		}
		p.flushDur.Observe(time.Since(start).Seconds())
		oldest := batch[0].TS
		for _, r := range batch {
			if r.TS.Before(oldest) {
				oldest = r.TS
			}
		}
		p.mu.Lock()
		p.lastFlush = time.Now()
		if p.dirty.IsZero() || oldest.Before(p.dirty) {
			p.dirty = oldest
		}
		p.mu.Unlock()
	}
}

func (p *Pipeline) write(ctx context.Context, batch []Reading) error {
	return pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `CREATE TEMP TABLE readings_in (LIKE readings INCLUDING DEFAULTS) ON COMMIT DROP`); err != nil {
			return err
		}
		_, err := tx.CopyFrom(ctx, pgx.Identifier{"readings_in"}, []string{"sensor_id", "ts", "temp_cc", "humidity_pm"},
			pgx.CopyFromSlice(len(batch), func(i int) ([]any, error) {
				r := batch[i]
				return []any{r.SensorID, r.TS, r.TempCC, r.HumidityPM}, nil
			}))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO readings SELECT * FROM readings_in ON CONFLICT DO NOTHING`)
		return err
	})
}

// KeyCache keeps decrypted device keys in memory; keys are immutable so the
// cache never needs invalidation.
type KeyCache struct {
	pool   *pgxpool.Pool
	keybox *devicesig.Keybox
	mu     sync.RWMutex
	keys   map[string]deviceKey
}

type deviceKey struct {
	id  int32
	key []byte
}

// NewKeyCache creates an empty cache.
func NewKeyCache(pool *pgxpool.Pool, kb *devicesig.Keybox) *KeyCache {
	return &KeyCache{pool: pool, keybox: kb, keys: map[string]deviceKey{}}
}

// ErrUnknownDevice is returned for unregistered or inactive sensors.
var ErrUnknownDevice = errors.New("unknown device")

// Get returns the sensor id and key for a device code.
func (c *KeyCache) Get(ctx context.Context, code string) (int32, []byte, error) {
	c.mu.RLock()
	k, ok := c.keys[code]
	c.mu.RUnlock()
	if ok {
		return k.id, k.key, nil
	}
	var id int32
	var ct, nonce []byte
	err := c.pool.QueryRow(ctx, `SELECT id, key_ciphertext, key_nonce FROM sensors WHERE code = $1 AND active`, code).
		Scan(&id, &ct, &nonce)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, ErrUnknownDevice
	}
	if err != nil {
		return 0, nil, err
	}
	key, err := c.keybox.Open(code, ct, nonce)
	if err != nil {
		return 0, nil, err
	}
	c.mu.Lock()
	c.keys[code] = deviceKey{id: id, key: key}
	c.mu.Unlock()
	return id, key, nil
}
