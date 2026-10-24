// Command simulator plays 2 000 temperature probes and a warehouse operator.
//
// Every 30 s each probe measures and sends its readings, HMAC-signed, to the
// API. On top of the normal profile it injects, deterministically from SEED:
//
//   - door-open spikes (several minutes above the limit),
//
//   - network outages: the probe keeps measuring and sends the backlog later,
//
//   - power outages: no measurements at all (a real gap),
//
//   - attacks: unsigned requests, wrong signatures and replays of captured
//     messages, which the API must reject,
//
//   - pallet split events through the operator's API account.
//
//     simulator             run
//     simulator healthcheck exit 0 if the heartbeat file is fresh
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/domain/devicesig"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/seed"
)

type reading struct {
	TS         time.Time `json:"ts"`
	TempCC     int       `json:"temp_cc"`
	HumidityPM int       `json:"humidity_pm"`
}

type probe struct {
	spec    seed.Sensor
	key     []byte
	rng     *rand.Rand
	pending []reading
	spike   int // remaining samples of a door-open spike
	netDown int // remaining ticks without network
	power   int // remaining ticks switched off
	battery float64
	last    *http.Request // last signed message, replayed by the attacker
	lastB   []byte
}

type stats struct {
	sent, accepted, failed, rejectedOK, rejectedUnexpected atomic.Int64
}

type sim struct {
	api      string
	client   *http.Client
	log      *slog.Logger
	probes   []*probe
	interval time.Duration
	seq      atomic.Uint64
	st       stats
	faults   bool
	rng      *rand.Rand
	mu       sync.Mutex // guards rng
	health   string

	opEmail, opPassword string
	token               string
}

const maxBacklog = 720 // 6 hours of readings per probe

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	health := env("HEALTH_FILE", "/tmp/simulator-alive")
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		fi, err := os.Stat(health)
		if err != nil || time.Since(fi.ModTime()) > 90*time.Second {
			os.Exit(1)
		}
		os.Exit(0)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	seedV, _ := strconv.ParseInt(env("SEED", "42"), 10, 64)
	count, _ := strconv.Atoi(env("SENSOR_COUNT", "2000"))
	interval, err := time.ParseDuration(env("INTERVAL", "30s"))
	if err != nil {
		interval = 30 * time.Second
	}
	secret := []byte(os.Getenv("DEVICE_PROVISION_SECRET"))
	if len(secret) < 16 {
		log.Error("DEVICE_PROVISION_SECRET must be set")
		os.Exit(2)
	}
	s := &sim{
		api:      env("API_URL", "http://localhost:8080"),
		client:   &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 64}},
		log:      log,
		interval: interval,
		faults:   env("SIM_FAULTS", "true") == "true",
		rng:      rand.New(rand.NewPCG(uint64(seedV), 1)),
		health:   health,
		opEmail:  env("SIM_OPERATOR_EMAIL", "depo@sogukzincir.test"), opPassword: env("SIM_OPERATOR_PASSWORD", seed.DemoPassword),
	}
	s.seq.Store(uint64(time.Now().UnixMicro()))
	for _, sp := range seed.NewLayout(count).Sensors {
		s.probes = append(s.probes, &probe{
			spec: sp, key: devicesig.DeriveKey(secret, sp.Code),
			rng:     rand.New(rand.NewPCG(uint64(seedV), uint64(sp.Index))),
			battery: 40 + float64(sp.Index%60),
		})
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("simulator starting", "probes", len(s.probes), "interval", interval.String(), "api", s.api, "seed", seedV)
	s.run(ctx)
}

func (s *sim) run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	events := time.NewTicker(2 * time.Minute)
	defer events.Stop()
	report := time.NewTicker(time.Minute)
	defer report.Stop()
	s.tick(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			s.log.Info("simulator stopped")
			return
		case t := <-ticker.C:
			s.tick(ctx, t)
		case <-events.C:
			if err := s.splitEvent(ctx); err != nil {
				s.log.Warn("pallet split event failed", "err", err)
			}
		case <-report.C:
			s.log.Info("simulator stats", "sent", s.st.sent.Load(), "accepted_readings", s.st.accepted.Load(),
				"failed", s.st.failed.Load(), "attacks_rejected", s.st.rejectedOK.Load(), "attacks_accepted", s.st.rejectedUnexpected.Load())
		}
	}
}

// measure advances a probe by one sample.
func (p *probe) measure(ts time.Time) (reading, bool) {
	if p.power > 0 {
		p.power--
		return reading{}, false
	}
	if p.spike == 0 && p.rng.IntN(2500) == 0 {
		p.spike = 16 + p.rng.IntN(40) // 8-28 minutes
	}
	t := seed.BaseTempCC(p.spec.Index, p.spec.Frozen, ts) + p.rng.IntN(81) - 40
	if p.spike > 0 {
		t += 700
		p.spike--
	}
	hum := 850
	if p.spec.Frozen {
		hum = 600
	}
	p.battery -= 0.0005
	return reading{TS: ts.UTC(), TempCC: t, HumidityPM: hum + p.rng.IntN(61) - 30}, true
}

func (s *sim) tick(ctx context.Context, now time.Time) {
	ts := now.Truncate(s.interval)
	for _, p := range s.probes {
		if s.faults && p.netDown == 0 && p.power == 0 {
			switch p.rng.IntN(6000) {
			case 0:
				p.netDown = 20 + p.rng.IntN(40) // 10-30 min offline, backlog later
			case 1:
				p.power = 20 + p.rng.IntN(20) // 10-20 min dead: a real gap
			}
		}
		if r, ok := p.measure(ts); ok {
			p.pending = append(p.pending, r)
			if len(p.pending) > maxBacklog {
				p.pending = p.pending[len(p.pending)-maxBacklog:]
			}
		}
	}
	// spread sends over the interval with a worker pool
	work := make(chan *probe)
	var wg sync.WaitGroup
	for w := 0; w < 32; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range work {
				s.send(ctx, p)
			}
		}()
	}
	for _, p := range s.probes {
		if p.netDown > 0 {
			p.netDown--
			continue
		}
		if len(p.pending) == 0 {
			continue
		}
		select {
		case work <- p:
		case <-ctx.Done():
		}
	}
	close(work)
	wg.Wait()
	if s.faults {
		s.attacks(ctx)
	}
	_ = os.WriteFile(s.health, []byte(time.Now().Format(time.RFC3339)), 0o644)
}

// nextSeq returns max(previous+1, now in µs): strictly increasing, and never
// behind wall-clock time, so a restarted simulator (or another client that
// used the current time as its sequence) does not get its messages rejected
// as replays.
func (s *sim) nextSeq() uint64 {
	for {
		prev := s.seq.Load()
		next := max(prev+1, uint64(time.Now().UnixMicro()))
		if s.seq.CompareAndSwap(prev, next) {
			return next
		}
	}
}

func (s *sim) signedRequest(ctx context.Context, p *probe, body []byte) (*http.Request, error) {
	seq := s.nextSeq()
	ts := time.Now().UnixMilli()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.api+"/api/v1/ingest/readings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(devicesig.HeaderDevice, p.spec.Code)
	req.Header.Set(devicesig.HeaderSeq, strconv.FormatUint(seq, 10))
	req.Header.Set(devicesig.HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(devicesig.HeaderSignature, devicesig.Sign(p.key, p.spec.Code, seq, ts, body))
	return req, nil
}

func (s *sim) send(ctx context.Context, p *probe) {
	batch := p.pending
	if len(batch) > 1000 {
		batch = batch[:1000]
	}
	battery := int(p.battery)
	body, _ := json.Marshal(map[string]any{"battery_pct": max(battery, 1), "readings": batch})
	req, err := s.signedRequest(ctx, p, body)
	if err != nil {
		return
	}
	s.st.sent.Add(1)
	resp, err := s.client.Do(req)
	if err != nil {
		s.st.failed.Add(1)
		return // keep the backlog, retry next tick
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		s.st.accepted.Add(int64(len(batch)))
		p.pending = p.pending[len(batch):]
		p.last, p.lastB = req, body
		return
	}
	s.st.failed.Add(1)
	if resp.StatusCode == http.StatusUnprocessableEntity || resp.StatusCode == http.StatusBadRequest {
		p.pending = nil // malformed or too old: nothing to gain by retrying
	}
}

// attacks sends a few hostile requests per tick. All of them must be refused.
func (s *sim) attacks(ctx context.Context) {
	s.mu.Lock()
	victims := []*probe{s.probes[s.rng.IntN(len(s.probes))], s.probes[s.rng.IntN(len(s.probes))], s.probes[s.rng.IntN(len(s.probes))]}
	s.mu.Unlock()
	body := func(p *probe) []byte {
		b, _ := json.Marshal(map[string]any{"readings": []reading{{TS: time.Now().UTC(), TempCC: 350, HumidityPM: 800}}})
		return b
	}
	expectReject := func(req *http.Request, kind string) {
		resp, err := s.client.Do(req)
		if err != nil {
			return
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusConflict {
			s.st.rejectedOK.Add(1)
		} else {
			s.st.rejectedUnexpected.Add(1)
			s.log.Error("attack was not rejected", "kind", kind, "status", resp.StatusCode)
		}
	}
	// 1. unsigned
	b := body(victims[0])
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.api+"/api/v1/ingest/readings", bytes.NewReader(b))
	req.Header.Set(devicesig.HeaderDevice, victims[0].spec.Code)
	expectReject(req, "unsigned")
	// 2. forged: signed with the wrong key
	req, _ = s.signedRequest(ctx, &probe{spec: victims[1].spec, key: []byte("not-the-device-key-0123456789abc")}, body(victims[1]))
	expectReject(req, "forged")
	// 3. replay of the last accepted message, byte for byte
	if p := victims[2]; p.last != nil {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.api+"/api/v1/ingest/readings", bytes.NewReader(p.lastB))
		req.Header = p.last.Header.Clone()
		expectReject(req, "replay")
	}
}

// splitEvent logs in as the warehouse operator and splits a pallet.
func (s *sim) splitEvent(ctx context.Context) error {
	if s.token == "" {
		if err := s.login(ctx); err != nil {
			return err
		}
	}
	var units struct {
		Items []struct {
			ID       int64 `json:"id"`
			Contents []struct {
				LotID     int64  `json:"lot_id"`
				Quantity  int    `json:"quantity"`
				LotStatus string `json:"lot_status"`
			} `json:"contents"`
			LocationKind string `json:"location_kind"`
		} `json:"items"`
	}
	if err := s.call(ctx, http.MethodGet, "/api/v1/units?status=active&limit=100", nil, &units, ""); err != nil {
		return err
	}
	var choices []int
	for i, u := range units.Items {
		if len(u.Contents) == 1 && u.Contents[0].Quantity >= 60 && u.LocationKind == "cold_room" {
			choices = append(choices, i)
		}
	}
	if len(choices) == 0 {
		return nil
	}
	s.mu.Lock()
	u := units.Items[choices[s.rng.IntN(len(choices))]]
	qty := 10 + s.rng.IntN(20)
	s.mu.Unlock()
	var out map[string]any
	err := s.call(ctx, http.MethodPost, fmt.Sprintf("/api/v1/units/%d/split", u.ID),
		map[string]any{"lot_id": u.Contents[0].LotID, "quantity": qty}, &out, uuid.NewString())
	if err == nil {
		s.log.Info("pallet split", "unit_id", u.ID, "child_unit_id", out["child_unit_id"], "quantity", qty)
	}
	return err
}

func (s *sim) login(ctx context.Context) error {
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := s.call(ctx, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": s.opEmail, "password": s.opPassword}, &out, ""); err != nil {
		return err
	}
	s.token = out.AccessToken
	return nil
}

func (s *sim) call(ctx context.Context, method, path string, in, out any, idemKey string) error {
	var body []byte
	if in != nil {
		body, _ = json.Marshal(in)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.api+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized && path != "/api/v1/auth/login" {
		s.token = "" // expired: log in again next time
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: status %d", method, path, resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
