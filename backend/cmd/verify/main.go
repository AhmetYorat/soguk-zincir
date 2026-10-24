// Command verify exercises every feature (F-1..F-7) and hard task
// (T-1..T-5) end to end against a running, seeded system and prints a
// PASS/FAIL table. Exit code 1 if anything failed.
//
// It only uses the public API (and the frontend's proxy), like a client would.
package main

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

type result struct {
	id, name string
	ok       bool
	detail   string
	elapsed  time.Duration
}

type runner struct {
	api, frontend *client
	tokens        map[string]string
	results       []result
	ctx           map[string]any // values shared between checks
	secret        []byte
}

func main() {
	apiURL := envOr("API_URL", "http://localhost:8080")
	feURL := envOr("FRONTEND_URL", "http://localhost:3000")
	hc := &http.Client{Timeout: 30 * time.Second}
	r := &runner{
		api:      &client{base: apiURL, http: hc},
		frontend: &client{base: feURL, http: hc},
		tokens:   map[string]string{},
		ctx:      map[string]any{},
		secret:   []byte(os.Getenv("DEVICE_PROVISION_SECRET")),
	}
	start := time.Now()
	fmt.Printf("SoğukZincir doğrulama — API %s, arayüz %s\n\n", apiURL, feURL)
	r.waitHealthy(2 * time.Minute)

	groups := []func(){
		r.health, r.authentication, r.authorization,
		r.f1Catalog, r.f2Sensors, r.f3Shipments, r.f4Alarms, r.f5Quality, r.f6HACCP, r.f7Trace,
		r.t1Excursion, r.t2TimeSeries, r.t3Traceability, r.t4FEFO, r.t5Integrity,
		r.platform,
	}
	for _, g := range groups {
		g()
	}
	r.print(time.Since(start))
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func (r *runner) waitHealthy(limit time.Duration) {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if res := r.api.get("/health"); res.Status == 200 {
			return
		}
		time.Sleep(2 * time.Second)
	}
}

// check runs fn and records the outcome. fn returns "" on success or the
// reason of failure; a detail can be appended after a "|" on success.
func (r *runner) check(id, name string, fn func() (bool, string)) {
	start := time.Now()
	var ok bool
	var detail string
	func() {
		defer func() {
			if p := recover(); p != nil {
				ok, detail = false, fmt.Sprintf("panic: %v", p)
			}
		}()
		ok, detail = fn()
	}()
	r.results = append(r.results, result{id: id, name: name, ok: ok, detail: detail, elapsed: time.Since(start)})
	mark := "PASS"
	if !ok {
		mark = "FAIL"
	}
	fmt.Printf("  %-4s %-6s %s\n", mark, id, name)
	if !ok {
		fmt.Printf("              └ %s\n", detail)
	}
}

func pass(detail string, args ...any) (bool, string) { return true, fmt.Sprintf(detail, args...) }
func fail(detail string, args ...any) (bool, string) { return false, fmt.Sprintf(detail, args...) }

func (r *runner) print(total time.Duration) {
	fmt.Println()
	fmt.Println("+--------+--------------------------------------------------------------+--------+------------------------------------------+")
	fmt.Printf("| %-6s | %-60s | %-6s | %-40s |\n", "Kimlik", "Kontrol", "Sonuç", "Ayrıntı")
	fmt.Println("+--------+--------------------------------------------------------------+--------+------------------------------------------+")
	failed := 0
	byID := map[string][2]int{}
	for _, res := range r.results {
		mark := "PASS"
		if !res.ok {
			mark = "FAIL"
			failed++
		}
		c := byID[res.id]
		c[0]++
		if res.ok {
			c[1]++
		}
		byID[res.id] = c
		fmt.Printf("| %-6s | %-60s | %-6s | %-40s |\n", res.id, trunc(res.name, 60), mark, trunc(res.detail, 40))
	}
	fmt.Println("+--------+--------------------------------------------------------------+--------+------------------------------------------+")
	ids := make([]string, 0, len(byID))
	for k := range byID {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	fmt.Println("\nKimlik bazında özet:")
	for _, k := range ids {
		c := byID[k]
		mark := "PASS"
		if c[0] != c[1] {
			mark = "FAIL"
		}
		fmt.Printf("  %-6s %s (%d/%d)\n", k, mark, c[1], c[0])
	}
	fmt.Printf("\nToplam %d kontrol, %d başarısız, süre %s\n", len(r.results), failed, total.Round(time.Second))
	if failed > 0 {
		os.Exit(1)
	}
}

func trunc(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// login returns a token for a demo account, cached.
func (r *runner) login(email string) string {
	if t, ok := r.tokens[email]; ok {
		return t
	}
	res := r.api.post("/api/v1/auth/login", map[string]string{"email": email, "password": "Demo1234!"})
	t := str(res.JSON()["access_token"])
	r.tokens[email] = t
	return t
}

func (r *runner) as(role string) *client {
	emails := map[string]string{
		"depo": "depo@sogukzincir.test", "sofor": "sofor@sogukzincir.test", "sofor2": "sofor2@sogukzincir.test",
		"kalite": "kalite@sogukzincir.test", "operasyon": "operasyon@sogukzincir.test",
		"market": "market@sogukzincir.test", "market2": "market2@sogukzincir.test", "denetci": "denetci@sogukzincir.test",
	}
	return r.api.as(r.login(emails[role]))
}
