package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/AhmetYorat/soguk-zincir/backend/internal/domain/devicesig"
)

// ---- health, authentication, authorisation ----

func (r *runner) health() {
	fmt.Println("Sağlık")
	r.check("HEALTH", "GET /health 200 ve veritabanı bağlantısı ok", func() (bool, string) {
		res := r.api.get("/health")
		j := res.JSON()
		if res.Status != 200 || j["database"] != "ok" {
			return fail("%s", res)
		}
		return pass("migration v%.0f", num(j["migration_version"]))
	})
}

func (r *runner) authentication() {
	fmt.Println("Kimlik doğrulama")
	r.check("AUTH", "Yanlış parola 401 invalid_credentials", func() (bool, string) {
		res := r.api.post("/api/v1/auth/login", map[string]string{"email": "kalite@sogukzincir.test", "password": "yanlis"})
		if res.Status != 401 || res.ErrCode() != "invalid_credentials" {
			return fail("%s", res)
		}
		return pass("401")
	})
	r.check("AUTH", "Olmayan kullanıcı aynı hatayı alır (hesap sızdırmaz)", func() (bool, string) {
		res := r.api.post("/api/v1/auth/login", map[string]string{"email": "yok@sogukzincir.test", "password": "x"})
		if res.Status != 401 || res.ErrCode() != "invalid_credentials" {
			return fail("%s", res)
		}
		return pass("401")
	})
	r.check("AUTH", "Tokensız ve bozuk tokenlı istek 401", func() (bool, string) {
		a := r.api.get("/api/v1/lots")
		b := r.api.as("eyJhbGciOiJub25lIn0.e30.").get("/api/v1/lots")
		if a.Status != 401 || b.Status != 401 {
			return fail("no token %d, bad token %d", a.Status, b.Status)
		}
		return pass("401 / 401")
	})
	r.check("AUTH", "Her rol giriş yapar, /auth/me rolünü döner", func() (bool, string) {
		for _, role := range []string{"depo", "sofor", "kalite", "operasyon", "market", "denetci"} {
			res := r.as(role).get("/api/v1/auth/me")
			if res.Status != 200 {
				return fail("%s: %s", role, res)
			}
		}
		return pass("6 rol")
	})
	r.check("AUTH", "Refresh token döner; eski token tekrar kullanılınca aile iptal", func() (bool, string) {
		login := r.api.post("/api/v1/auth/login", map[string]string{"email": "denetci@sogukzincir.test", "password": "Demo1234!"})
		rt := str(login.JSON()["refresh_token"])
		first := r.api.post("/api/v1/auth/refresh", map[string]string{"refresh_token": rt})
		if first.Status != 200 {
			return fail("refresh: %s", first)
		}
		newRT := str(first.JSON()["refresh_token"])
		reuse := r.api.post("/api/v1/auth/refresh", map[string]string{"refresh_token": rt})
		after := r.api.post("/api/v1/auth/refresh", map[string]string{"refresh_token": newRT})
		if reuse.Status != 401 || after.Status != 401 {
			return fail("reuse %d, rotated-after-reuse %d (both must be 401)", reuse.Status, after.Status)
		}
		return pass("rotation + reuse detection")
	})
}

func (r *runner) authorization() {
	fmt.Println("Yetki ihlali")
	cases := []struct {
		role, method, path, why string
		body                    any
	}{
		{"sofor", "GET", "/api/v1/lots", "şoför parti listesini göremez", nil},
		{"market", "GET", "/api/v1/audit", "müşteri denetim kaydını göremez", nil},
		{"market", "GET", "/api/v1/trace/lots/1", "müşteri izlenebilirlik sorgulayamaz", nil},
		{"denetci", "POST", "/api/v1/products", "denetçi ürün oluşturamaz (salt okunur)", map[string]any{"sku": "X"}},
		{"depo", "POST", "/api/v1/lots/1/decisions", "depo operatörü kalite kararı veremez", map[string]any{"decision": "accept", "reason": "x"}},
		{"operasyon", "GET", "/api/v1/audit", "operasyon müdürü denetim kaydını göremez", nil},
		{"kalite", "POST", "/api/v1/shipments", "kalite sorumlusu sevkiyat planlayamaz", map[string]any{}},
		{"sofor", "POST", "/api/v1/recalls", "şoför geri çağırma başlatamaz", map[string]any{"lot_id": 1, "reason": "x"}},
	}
	for _, c := range cases {
		c := c
		r.check("AUTHZ", c.why+" → 403", func() (bool, string) {
			res := r.as(c.role).do(c.method, c.path, c.body, nil)
			if res.Status != 403 || res.ErrCode() != "forbidden" {
				return fail("%s", res)
			}
			return pass("403 forbidden")
		})
	}
	r.check("AUTHZ", "Müşteri başka marketin sevkiyatını göremez → 404", func() (bool, string) {
		mine := r.as("market2").get("/api/v1/customer/shipments")
		items := arr(mine.JSON()["items"])
		if len(items) == 0 {
			return fail("market2 has no shipments to probe with")
		}
		other := id(m(items[0])["id"])
		res := r.as("market").get(fmt.Sprintf("/api/v1/customer/shipments/%d", other))
		if res.Status != 404 {
			return fail("%s", res)
		}
		return pass("404, id %d", other)
	})
	r.check("AUTHZ", "Hata biçimi tutarlı {error:{code,message}}", func() (bool, string) {
		res := r.api.get("/api/v1/yok")
		e := m(res.JSON()["error"])
		if e == nil || str(e["code"]) == "" || str(e["message"]) == "" {
			return fail("%s", res)
		}
		return pass("%s", e["code"])
	})
}

// ---- F-1 products and lots ----

func (r *runner) f1Catalog() {
	fmt.Println("F-1 Ürün ve parti yönetimi")
	op := r.as("depo")
	sku := "VER-" + uuid.NewString()[:6]
	r.check("F-1", "Ürün oluşturma, sunucu tarafı doğrulama (422)", func() (bool, string) {
		bad := op.post("/api/v1/products", map[string]any{"sku": sku, "name": "x", "temp_min_cc": 800, "temp_max_cc": 200,
			"window_s": 3600, "shelf_life_h": 10, "unit_weight_g": 100})
		if bad.Status != 422 {
			return fail("invalid product: %s", bad)
		}
		res := op.post("/api/v1/products", map[string]any{"sku": sku, "name": "Doğrulama Ayranı", "temp_min_cc": 200, "temp_max_cc": 800,
			"max_cumulative_excursion_s": 1800, "window_s": 3600, "window_max_excursion_s": 600, "shelf_life_h": 240,
			"unit_weight_g": 300, "list_price_kurus": 1500})
		if res.Status != 201 {
			return fail("%s", res)
		}
		r.ctx["product"] = id(res.JSON()["id"])
		return pass("ürün %d", r.ctx["product"])
	})
	r.check("F-1", "Parti kabulü paletlere bölünür, parti ve paletler okunur", func() (bool, string) {
		room := r.firstLocation("cold_room")
		key := uuid.NewString()
		body := map[string]any{"code": "VER-LOT-" + uuid.NewString()[:8], "product_id": r.ctx["product"], "location_id": room,
			"produced_at": time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339), "pallets": []int{100, 120}}
		res := op.postIdem("/api/v1/lots", key, body)
		if res.Status != 201 {
			return fail("%s", res)
		}
		lot := id(res.JSON()["id"])
		r.ctx["lot"], r.ctx["lotKey"], r.ctx["lotBody"] = lot, key, body
		r.ctx["lotUnits"] = arr(res.JSON()["unit_ids"])
		get := op.get(fmt.Sprintf("/api/v1/lots/%d", lot))
		if get.Status != 200 || len(arr(get.JSON()["units"])) != 2 || num(m(get.JSON()["lot"])["on_hand"]) != 220 {
			return fail("%s", get)
		}
		return pass("lot %d, 2 palet, 220 adet", lot)
	})
	r.check("F-1", "Idempotency-Key: aynı istek aynı yanıt, yeni kayıt yok", func() (bool, string) {
		res := op.postIdem("/api/v1/lots", r.ctx["lotKey"].(string), r.ctx["lotBody"])
		if res.Status != 201 || res.Header.Get("Idempotent-Replayed") != "true" || id(res.JSON()["id"]) != r.ctx["lot"] {
			return fail("%s replayed=%q", res, res.Header.Get("Idempotent-Replayed"))
		}
		other := r.ctx["lotBody"].(map[string]any)
		changed := map[string]any{}
		for k, v := range other {
			changed[k] = v
		}
		changed["pallets"] = []int{1}
		reuse := op.postIdem("/api/v1/lots", r.ctx["lotKey"].(string), changed)
		if reuse.Status != 422 || reuse.ErrCode() != "idempotency_key_reused" {
			return fail("different body same key: %s", reuse)
		}
		return pass("replay + 422 on reuse")
	})
	r.check("F-1", "Mükerrer parti kodu 409 (UNIQUE)", func() (bool, string) {
		res := op.post("/api/v1/lots", r.ctx["lotBody"])
		if res.Status != 409 {
			return fail("%s", res)
		}
		return pass("409 %s", res.ErrCode())
	})
	r.check("F-1", "Keyset sayfalama: sayfalar çakışmaz", func() (bool, string) {
		p1 := op.get("/api/v1/lots?limit=20")
		cur := str(p1.JSON()["next_cursor"])
		p2 := op.get("/api/v1/lots?limit=20&cursor=" + url.QueryEscape(cur))
		seen := map[int64]bool{}
		for _, it := range arr(p1.JSON()["items"]) {
			seen[id(m(it)["id"])] = true
		}
		for _, it := range arr(p2.JSON()["items"]) {
			if seen[id(m(it)["id"])] {
				return fail("overlap at id %d", id(m(it)["id"]))
			}
		}
		if cur == "" || len(arr(p2.JSON()["items"])) == 0 {
			return fail("no second page")
		}
		return pass("2×20 kayıt")
	})
}

func (r *runner) firstLocation(kind string) int64 {
	res := r.as("operasyon").get("/api/v1/locations?kind=" + kind)
	for _, it := range arr(res.JSON()["items"]) {
		if num(m(it)["sensors"]) > 0 || kind == "customer" {
			return id(m(it)["id"])
		}
	}
	return 0
}

// ---- F-2 sensors ----

func (r *runner) f2Sensors() {
	fmt.Println("F-2 Sensör verisi")
	q := r.as("kalite")
	r.check("F-2", "Sensör listesi sıcaklık ve nem ile canlı", func() (bool, string) {
		res := q.get("/api/v1/sensors?limit=5")
		items := arr(res.JSON()["items"])
		if res.Status != 200 || len(items) != 5 {
			return fail("%s", res)
		}
		live := r.as("depo").get("/api/v1/live")
		rate := num(m(live.JSON()["ingest"])["readings_per_second"])
		return pass("ingest %.1f ölçüm/sn", rate)
	})
	r.check("F-2", "Son 24 saat serisi (sıcaklık + nem) döner", func() (bool, string) {
		res := q.get("/api/v1/sensors/1/readings")
		pts := arr(res.JSON()["points"])
		if res.Status != 200 || len(pts) < 1000 {
			return fail("%d points: %s", len(pts), res)
		}
		p := m(pts[0])
		if _, ok := p["humidity_pct"]; !ok {
			return fail("no humidity")
		}
		return pass("%d nokta, %s", len(pts), res.JSON()["resolution"])
	})
	r.check("F-2", "Sensör pil/bağlantı sağlığı raporu", func() (bool, string) {
		res := q.get("/api/v1/sensors/health")
		j := res.JSON()
		if res.Status != 200 || num(j["total"]) < 10 {
			return fail("%s", res)
		}
		return pass("%.0f sensör, %.0f çevrimiçi, %.0f zayıf pil", num(j["total"]), num(j["online"]), num(j["low_battery"]))
	})
	r.check("F-2", "İmzalı cihaz ölçümü kabul edilir (202)", func() (bool, string) {
		key := devicesig.DeriveKey(r.secret, "S-00002")
		body, _ := json.Marshal(map[string]any{"battery_pct": 77, "readings": []map[string]any{
			{"ts": time.Now().UTC().Format(time.RFC3339), "temp_cc": 410, "humidity_pm": 820}}})
		res := r.api.signedIngest("S-00002", key, uint64(time.Now().UnixMicro()), time.Now(), body)
		if res.Status != 202 {
			return fail("%s", res)
		}
		return pass("202")
	})
}

// ---- F-3 shipments ----

func (r *runner) f3Shipments() {
	fmt.Println("F-3 Sevkiyat ve araç yükleme")
	ops := r.as("operasyon")
	r.check("F-3", "Sevkiyat oluştur, palet yükle, yola çıkar", func() (bool, string) {
		units := r.ctx["lotUnits"].([]any)
		room := r.firstLocation("cold_room")
		veh := r.freeVehicle()
		sh := ops.postIdem("/api/v1/shipments", uuid.NewString(), map[string]any{"vehicle_id": veh, "customer_id": 3, "origin_id": room,
			"planned_departure": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "transit_minutes": 120})
		if sh.Status != 201 {
			return fail("create: %s", sh)
		}
		sid := id(sh.JSON()["id"])
		load := ops.postIdem(fmt.Sprintf("/api/v1/shipments/%d/load", sid), uuid.NewString(), map[string]any{"unit_ids": []int64{id(units[0])}})
		if load.Status != 200 {
			return fail("load: %s", load)
		}
		dep := ops.postIdem(fmt.Sprintf("/api/v1/shipments/%d/depart", sid), uuid.NewString(), nil)
		if dep.Status != 200 || dep.JSON()["status"] != "in_transit" {
			return fail("depart: %s", dep)
		}
		again := ops.post(fmt.Sprintf("/api/v1/shipments/%d/load", sid), map[string]any{"unit_ids": []int64{id(units[1])}})
		if again.Status != 409 {
			return fail("loading after departure must be 409: %s", again)
		}
		r.ctx["shipment"] = sid
		return pass("sevkiyat %d yolda", sid)
	})
	r.check("F-3", "Yüklenen palet ikinci sevkiyata yüklenemez", func() (bool, string) {
		units := r.ctx["lotUnits"].([]any)
		sid := r.newShipment(r.firstLocation("cold_room"), 120)
		res := ops.post(fmt.Sprintf("/api/v1/shipments/%d/load", sid), map[string]any{"unit_ids": []int64{id(units[0])}})
		if res.Status != 409 {
			return fail("%s", res)
		}
		return pass("409 %s", res.ErrCode())
	})
}

func (r *runner) freeVehicle() int64 {
	// vehicles without a demo driver and without an active shipment
	busy := map[int64]bool{}
	for _, st := range []string{"loading", "in_transit"} {
		res := r.as("operasyon").get("/api/v1/shipments?limit=200&status=" + st)
		for _, it := range arr(res.JSON()["items"]) {
			busy[id(m(it)["vehicle_id"])] = true
		}
	}
	res := r.as("operasyon").get("/api/v1/vehicles")
	items := arr(res.JSON()["items"])
	for i := len(items) - 1; i >= 0; i-- {
		v := m(items[i])
		if v["driver_id"] == nil && !busy[id(v["id"])] {
			if !r.usedVehicle(id(v["id"])) {
				return id(v["id"])
			}
		}
	}
	return 0
}

var usedVehicles = map[int64]bool{}

func (r *runner) usedVehicle(v int64) bool {
	if usedVehicles[v] {
		return true
	}
	usedVehicles[v] = true
	return false
}

func (r *runner) newShipment(origin int64, transitMin int) int64 {
	res := r.as("operasyon").post("/api/v1/shipments", map[string]any{"vehicle_id": r.freeVehicle(), "customer_id": 4,
		"origin_id": origin, "planned_departure": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "transit_minutes": transitMin})
	return id(res.JSON()["id"])
}

// ---- F-4 alarms ----

func (r *runner) f4Alarms() {
	fmt.Println("F-4 Alarm ve uygunsuzluk yönetimi")
	q := r.as("kalite")
	r.check("F-4", "Canlı alarmlar listelenir, onaylanır ve kapatılır", func() (bool, string) {
		// a lot-level alarm (re-raised by the lot evaluator after closing);
		// the seeded, escalated sensor alarm is left for the T-1 check
		res := q.get("/api/v1/alarms?status=open&rule=cumulative&limit=50")
		items := arr(res.JSON()["items"])
		if len(items) == 0 {
			return fail("no open alarms: %s", res)
		}
		aid := id(m(items[0])["id"])
		ack := q.post(fmt.Sprintf("/api/v1/alarms/%d/ack", aid), nil)
		if ack.Status != 200 || ack.JSON()["status"] != "acknowledged" {
			return fail("ack: %s", ack)
		}
		twice := q.post(fmt.Sprintf("/api/v1/alarms/%d/ack", aid), nil)
		if twice.Status != 409 {
			return fail("second ack must be 409: %s", twice)
		}
		cl := q.post(fmt.Sprintf("/api/v1/alarms/%d/close", aid), nil)
		if cl.Status != 200 {
			return fail("close: %s", cl)
		}
		return pass("alarm %d", aid)
	})
	r.check("F-4", "Uygunsuzluk kaydı partiyi beklemeye alır", func() (bool, string) {
		res := q.post("/api/v1/nonconformities", map[string]any{"lot_id": r.ctx["lot"], "description": "Doğrulama: ambalajda yoğuşma"})
		if res.Status != 201 {
			return fail("%s", res)
		}
		r.ctx["nc"] = id(res.JSON()["id"])
		lot := q.get(fmt.Sprintf("/api/v1/lots/%d", r.ctx["lot"]))
		if m(lot.JSON()["lot"])["status"] != "on_hold" {
			return fail("lot status %v", m(lot.JSON()["lot"])["status"])
		}
		return pass("lot on_hold")
	})
	r.check("F-4", "Depo operatörü alarm onaylayamaz → 403", func() (bool, string) {
		res := r.as("depo").post("/api/v1/alarms/1/ack", nil)
		if res.Status != 403 {
			return fail("%s", res)
		}
		return pass("403")
	})
}

// ---- F-5 quality ----

func (r *runner) f5Quality() {
	fmt.Println("F-5 Kalite kararı")
	q := r.as("kalite")
	r.check("F-5", "İndirim kararı (%25) partiyi discounted yapar", func() (bool, string) {
		res := q.postIdem(fmt.Sprintf("/api/v1/lots/%d/decisions", r.ctx["lot"]), uuid.NewString(),
			map[string]any{"decision": "discount", "discount_pct": 25, "reason": "Yoğuşma, satış süresi kısaltıldı", "nonconformity_id": r.ctx["nc"]})
		if res.Status != 201 || res.JSON()["lot_status"] != "discounted" {
			return fail("%s", res)
		}
		return pass("discounted")
	})
	r.check("F-5", "Geçersiz geçiş: discounted → accept 409", func() (bool, string) {
		res := q.post(fmt.Sprintf("/api/v1/lots/%d/decisions", r.ctx["lot"]), map[string]any{"decision": "accept", "reason": "geri al"})
		if res.Status != 409 || res.ErrCode() != "invalid_transition" {
			return fail("%s", res)
		}
		return pass("409")
	})
	r.check("F-5", "Doğrulama: indirimsiz discount ve kısa gerekçe 422", func() (bool, string) {
		res := q.post(fmt.Sprintf("/api/v1/lots/%d/decisions", r.ctx["lot"]), map[string]any{"decision": "discount", "reason": "x"})
		if res.Status != 422 {
			return fail("%s", res)
		}
		return pass("422")
	})
	r.check("F-5", "Eşzamanlı 10 karar: tam olarak biri kazanır (FOR UPDATE)", func() (bool, string) {
		lot := r.newLot(1, []int{50})
		codes := r.parallel(10, func(i int) int {
			d := "accept"
			if i%2 == 1 {
				d = "destroy"
			}
			return q.post(fmt.Sprintf("/api/v1/lots/%d/decisions", lot), map[string]any{"decision": d, "reason": "eşzamanlılık testi"}).Status
		})
		created := count(codes, 201)
		// accept then destroy is a valid sequence, so 1 or 2 may succeed; never two accepts
		if created < 1 || created > 2 || count(codes, 500) > 0 {
			return fail("statuses %v", codes)
		}
		dec := q.get(fmt.Sprintf("/api/v1/lots/%d/decisions", lot))
		accepts := 0
		for _, d := range arr(dec.JSON()["items"]) {
			if m(d)["decision"] == "accept" {
				accepts++
			}
		}
		if accepts > 1 {
			return fail("%d accept decisions recorded", accepts)
		}
		return pass("201×%d, 409×%d", created, count(codes, 409))
	})
	r.check("F-5", "İmha kararı paletleri imha durumuna alır", func() (bool, string) {
		lot := r.newLot(1, []int{30})
		res := q.post(fmt.Sprintf("/api/v1/lots/%d/decisions", lot), map[string]any{"decision": "destroy", "reason": "Soğuk zincir kırıldı"})
		if res.Status != 201 {
			return fail("%s", res)
		}
		units := q.get(fmt.Sprintf("/api/v1/units?lot_id=%d", lot))
		get := q.get(fmt.Sprintf("/api/v1/lots/%d", lot))
		if num(m(get.JSON()["lot"])["on_hand"]) != 0 {
			return fail("stock left after destroy: %s", get)
		}
		_ = units
		return pass("on_hand 0")
	})
}

func (r *runner) newLot(product int64, pallets []int) int64 {
	res := r.as("depo").post("/api/v1/lots", map[string]any{"code": "VER-" + uuid.NewString()[:10], "product_id": product,
		"location_id": r.firstLocation("cold_room"), "produced_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "pallets": pallets})
	return id(res.JSON()["id"])
}

func (r *runner) parallel(n int, fn func(i int) int) []int {
	out := make([]int, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			out[i] = fn(i)
		}(i)
	}
	close(start)
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

// ---- F-6 HACCP ----

func (r *runner) f6HACCP() {
	fmt.Println("F-6 HACCP kontrol listeleri")
	op := r.as("depo")
	r.check("F-6", "Kontrol listeleri ve CCP maddeleri okunur", func() (bool, string) {
		res := op.get("/api/v1/haccp/checklists")
		items := arr(res.JSON()["items"])
		if len(items) < 3 {
			return fail("%s", res)
		}
		r.ctx["checklist"] = m(items[0])
		return pass("%d liste", len(items))
	})
	answers := func(fail bool, action string) []map[string]any {
		cl := r.ctx["checklist"].(map[string]any)
		var out []map[string]any
		for i, it := range arr(cl["items"]) {
			item := m(it)
			a := map[string]any{"item_id": id(item["id"])}
			if item["kind"] == "temperature" {
				a["temp_cc"] = 400
			} else {
				a["bool_value"] = !(fail && i == 1)
				if fail && i == 1 && action != "" {
					a["corrective_action"] = action
				}
			}
			out = append(out, a)
		}
		return out
	}
	r.check("F-6", "Başarısız CCP düzeltici faaliyet olmadan reddedilir (422)", func() (bool, string) {
		cl := r.ctx["checklist"].(map[string]any)
		res := op.post("/api/v1/haccp/records", map[string]any{"checklist_id": id(cl["id"]), "location_id": r.firstLocation("cold_room"),
			"answers": answers(true, "")})
		if res.Status != 422 {
			return fail("%s", res)
		}
		return pass("422")
	})
	r.check("F-6", "Düzeltici faaliyetli kayıt kabul edilir, all_ok=false", func() (bool, string) {
		cl := r.ctx["checklist"].(map[string]any)
		res := op.postIdem("/api/v1/haccp/records", uuid.NewString(), map[string]any{"checklist_id": id(cl["id"]),
			"location_id": r.firstLocation("cold_room"), "answers": answers(true, "Conta değiştirildi")})
		if res.Status != 201 || res.JSON()["all_ok"] != false {
			return fail("%s", res)
		}
		return pass("kayıt %d", id(res.JSON()["id"]))
	})
}

// ---- F-7 traceability and recall ----

func (r *runner) lotByCode(code string) int64 {
	res := r.as("kalite").get("/api/v1/lots?q=" + url.QueryEscape(code))
	for _, it := range arr(res.JSON()["items"]) {
		if m(it)["code"] == code {
			return id(m(it)["id"])
		}
	}
	return 0
}

func (r *runner) f7Trace() {
	fmt.Println("F-7 İzlenebilirlik ve geri çağırma")
	q := r.as("kalite")
	r.check("F-7", "LOT-TRACE-001 ileri izleme: 4 müşteri, 4 araç", func() (bool, string) {
		lot := r.lotByCode("LOT-TRACE-001")
		res := q.get(fmt.Sprintf("/api/v1/trace/lots/%d", lot))
		j := res.JSON()
		custs := arr(j["customers"])
		plates := map[string]bool{}
		for _, u := range arr(j["units"]) {
			if p := str(m(u)["vehicle_plate"]); p != "" {
				plates[p] = true
			}
		}
		if res.Status != 200 || len(custs) != 4 || len(plates) != 4 {
			return fail("%d customers, %d vehicles: %s", len(custs), len(plates), res)
		}
		r.ctx["traceLot"] = lot
		return pass("%d palet, %.1f ms", len(arr(j["units"])), num(j["query_ms"]))
	})
	r.check("F-7", "Geri çağırma etkilenen müşterileri kaydeder, parti recalled olur", func() (bool, string) {
		// LOT-TRACE-002 reached one customer through the mixed pallet. On a
		// re-run (already recalled) a fresh lot is recalled instead.
		lot, wantCustomers := r.lotByCode("LOT-TRACE-002"), 1
		lots := q.get(fmt.Sprintf("/api/v1/lots/%d", lot))
		if m(lots.JSON()["lot"])["status"] == "recalled" {
			lot, wantCustomers = r.newLot(1, []int{20}), 0
		}
		res := q.postIdem("/api/v1/recalls", uuid.NewString(), map[string]any{"lot_id": lot, "reason": "Doğrulama: tedarikçi bildirimi"})
		j := res.JSON()
		if res.Status != 201 || len(arr(j["affected_customers"])) != wantCustomers {
			return fail("%s", res)
		}
		again := q.post("/api/v1/recalls", map[string]any{"lot_id": lot, "reason": "ikinci kez"})
		if again.Status != 409 {
			return fail("second recall must be 409: %s", again)
		}
		return pass("%.0f palet, 1 müşteri", num(j["affected_units"]))
	})
}

// ---- T-1 excursion engine ----

func (r *runner) t1Excursion() {
	fmt.Println("T-1 Sıcaklık sapma motoru")
	q := r.as("kalite")
	r.check("T-1", "Sıcak odadaki parti: kümülatif sapma, MKT, raf ömrü cezası", func() (bool, string) {
		res := q.get("/api/v1/lots?status=on_hold&limit=50")
		for _, it := range arr(res.JSON()["items"]) {
			lot := id(m(it)["id"])
			ev := q.get(fmt.Sprintf("/api/v1/lots/%d/evaluation", lot))
			w := m(ev.JSON()["worst"])
			if w == nil {
				continue
			}
			ex := m(w["excursion"])
			hasCum := false
			for _, b := range arr(ex["breaches"]) {
				if m(b)["rule"] == "cumulative" {
					hasCum = true
				}
			}
			if hasCum && ex["mkt_c"] != nil && num(m(w["shelf_life"])["penalty_h"]) > 0 {
				r.ctx["hotLot"] = lot
				return pass("lot %d: %.0f dk sapma, MKT %.2f °C, ceza %.1f sa", lot, num(ex["cumulative_excursion_s"])/60,
					num(ex["mkt_c"]), num(m(w["shelf_life"])["penalty_h"]))
			}
		}
		return fail("no on_hold lot with a cumulative breach found")
	})
	r.check("T-1", "Kayan pencere sapması alarm olarak üretilmiş", func() (bool, string) {
		res := q.get("/api/v1/alarms?rule=window&limit=5")
		if len(arr(res.JSON()["items"])) == 0 {
			return fail("no window alarm")
		}
		return pass("%d+ alarm", len(arr(res.JSON()["items"])))
	})
	r.check("T-1", "Canlı sapma: imzalı 11 °C ölçümü 60 sn içinde alarm üretir", func() (bool, string) {
		sensor, code := r.sensorWithGoods()
		if sensor == 0 {
			return fail("no sensor at a room with goods")
		}
		key := devicesig.DeriveKey(r.secret, code)
		now := time.Now().UTC()
		body, _ := json.Marshal(map[string]any{"readings": []map[string]any{
			{"ts": now.Add(-time.Second).Format(time.RFC3339Nano), "temp_cc": 1100, "humidity_pm": 800}}})
		if res := r.api.signedIngest(code, key, uint64(time.Now().UnixMicro()), time.Now(), body); res.Status != 202 {
			return fail("ingest: %s", res)
		}
		deadline := time.Now().Add(75 * time.Second)
		for time.Now().Before(deadline) {
			res := q.get("/api/v1/alarms?status=live&rule=limit_high&limit=200")
			for _, it := range arr(res.JSON()["items"]) {
				// the alarm must have been raised or refreshed by this reading,
				// not be left over from an earlier run
				seen, _ := time.Parse(time.RFC3339Nano, str(m(it)["last_seen_at"]))
				if int64(num(m(it)["sensor_id"])) == sensor && !seen.Before(now.Add(-2*time.Second)) {
					return pass("%s → alarm %d, %.0f sn", code, id(m(it)["id"]), time.Since(now).Seconds())
				}
			}
			time.Sleep(3 * time.Second)
		}
		return fail("no limit_high alarm for %s within 75 s", code)
	})
	r.check("T-1", "Alarmlar tekrarsız: aynı koşul için tek canlı alarm", func() (bool, string) {
		res := q.get("/api/v1/alarms?status=live&limit=200")
		seen := map[string]bool{}
		for _, it := range arr(res.JSON()["items"]) {
			a := m(it)
			k := fmt.Sprintf("%v/%v/%v/%v", a["sensor_id"], a["lot_id"], a["rule"], a["product_id"])
			if seen[k] {
				return fail("duplicate live alarm %s", k)
			}
			seen[k] = true
		}
		return pass("%d canlı alarm, tekrar yok", len(seen))
	})
	r.check("T-1", "Süre aşımında eskalasyon (onaylanmamış eski alarm seviye ≥1)", func() (bool, string) {
		res := q.get("/api/v1/alarms?status=open&rule=limit_high&limit=200")
		for _, it := range arr(res.JSON()["items"]) {
			if num(m(it)["escalation_level"]) >= 1 {
				return pass("alarm %d seviye %.0f", id(m(it)["id"]), num(m(it)["escalation_level"]))
			}
		}
		return fail("no escalated alarm")
	})
	r.check("T-1", "Boşluk politikası: aynı kesinti 'belirsiz' sapma sayılır, 'doldur' ile sayılmaz", func() (bool, string) {
		// seeded probes have deterministic 20-minute outages; find one
		for sid := 1; sid <= 400; sid++ {
			u := q.get(fmt.Sprintf("/api/v1/sensors/%d/evaluation?product_id=1&policy=uncertain", sid))
			ev := m(u.JSON()["evaluation"])
			// short gaps (e.g. the minutes between seeding and the simulator
			// starting) are real but too small to compare policies on
			if len(arr(ev["gaps"])) == 0 || num(ev["uncertain_s"]) < 600 {
				continue
			}
			in := q.get(fmt.Sprintf("/api/v1/sensors/%d/evaluation?product_id=1&policy=interpolate", sid))
			ei := m(in.JSON()["evaluation"])
			if num(ei["uncertain_s"]) != 0 || num(ev["cumulative_excursion_s"]) <= num(ei["cumulative_excursion_s"]) {
				return fail("sensor %d: uncertain %.0f s / interpolate %.0f s", sid, num(ev["uncertain_s"]), num(ei["uncertain_s"]))
			}
			return pass("S%d: belirsiz %.0f dk sapma, doldurunca %.0f dk", sid, num(ev["cumulative_excursion_s"])/60, num(ei["cumulative_excursion_s"])/60)
		}
		return fail("no sensor with a gap in the first 400")
	})
}

func (r *runner) sensorWithGoods() (int64, string) {
	res := r.as("operasyon").get("/api/v1/locations?kind=cold_room")
	for _, it := range arr(res.JSON()["items"]) {
		l := m(it)
		if num(l["active_units"]) > 0 && num(l["sensors"]) > 0 && !strings.Contains(str(l["name"]), "Donuk") {
			s := r.as("kalite").get(fmt.Sprintf("/api/v1/locations/%d/readings?resolution=1h", id(l["id"])))
			ids := arr(s.JSON()["sensor_ids"])
			if len(ids) > 0 {
				sid := id(ids[0])
				sres := r.as("kalite").get(fmt.Sprintf("/api/v1/sensors/%d", sid))
				return sid, str(sres.JSON()["code"])
			}
		}
	}
	return 0, ""
}

// ---- T-2 time series ----

func (r *runner) t2TimeSeries() {
	fmt.Println("T-2 Yüksek hacimli zaman serisi")
	q := r.as("kalite")
	r.check("T-2", "Günlük partisyonlar ve 1m/15m/1h özet seviyeleri mevcut", func() (bool, string) {
		res := q.get("/api/v1/timeseries/stats")
		j := res.JSON()
		tables := map[string]int{}
		for _, p := range arr(j["partitions"]) {
			tables[str(m(p)["table"])]++
		}
		if tables["readings"] < 3 || tables["readings_1m"] < 2 || tables["readings_15m"] < 1 || tables["readings_1h"] < 1 || len(arr(j["watermarks"])) != 3 {
			return fail("partitions %v", tables)
		}
		return pass("%.0f satır/24 sa, %d ham partisyon", num(j["raw_rows_last_24h"]), tables["readings"])
	})
	measure := func(path func(i int) string) (p50, p95 time.Duration, n int) {
		var ds []time.Duration
		for i := 0; i < 100; i++ {
			res := q.get(path(i))
			if res.Status == 200 {
				ds = append(ds, res.Elapsed)
			}
		}
		sort.Slice(ds, func(a, b int) bool { return ds[a] < ds[b] })
		if len(ds) == 0 {
			return 0, 0, 0
		}
		return ds[len(ds)/2], ds[len(ds)*95/100-1], len(ds)
	}
	r.check("T-2", "Son 24 saat sorgusu (1 dk çözünürlük) p95 < 100 ms", func() (bool, string) {
		p50, p95, n := measure(func(i int) string { return fmt.Sprintf("/api/v1/sensors/%d/readings", 1+(i*37)%1999) })
		if n < 100 || p95 >= 100*time.Millisecond {
			return fail("n=%d p50=%s p95=%s", n, p50, p95)
		}
		return pass("p50 %s, p95 %s", p50.Round(time.Millisecond/10), p95.Round(time.Millisecond/10))
	})
	r.check("T-2", "Son 24 saat ham veri (2 880 satır) p95 < 100 ms", func() (bool, string) {
		p50, p95, n := measure(func(i int) string {
			return fmt.Sprintf("/api/v1/sensors/%d/readings?resolution=raw", 1+(i*53)%1999)
		})
		if n < 100 || p95 >= 100*time.Millisecond {
			return fail("n=%d p50=%s p95=%s", n, p50, p95)
		}
		return pass("p50 %s, p95 %s", p50.Round(time.Millisecond/10), p95.Round(time.Millisecond/10))
	})
}

// ---- T-3 traceability ----

func (r *runner) t3Traceability() {
	fmt.Println("T-3 Parti ve sevkiyat izlenebilirliği")
	q := r.as("kalite")
	r.check("T-3", "Recursive geri çağırma sorgusu < 1 sn", func() (bool, string) {
		res := q.get(fmt.Sprintf("/api/v1/trace/lots/%d", r.ctx["traceLot"]))
		ms := num(res.JSON()["query_ms"])
		if res.Status != 200 || ms >= 1000 || res.Elapsed >= time.Second {
			return fail("query %.1f ms, request %s", ms, res.Elapsed)
		}
		return pass("sorgu %.1f ms, istek %s", ms, res.Elapsed.Round(time.Millisecond))
	})
	r.check("T-3", "Karışık palet geriye izlenir: iki parti, araç ve sıcaklık geçmişi", func() (bool, string) {
		units := q.get("/api/v1/units?q=M-TRACE-01")
		items := arr(units.JSON()["items"])
		if len(items) == 0 {
			return fail("M-TRACE-01 not found")
		}
		res := q.get(fmt.Sprintf("/api/v1/trace/units/%d", id(m(items[0])["id"])))
		lots := arr(res.JSON()["lots"])
		if len(lots) != 2 {
			return fail("%d lots on mixed pallet", len(lots))
		}
		hist := arr(m(lots[0])["history"])
		hasVehicle, hasTemp := false, false
		for _, h := range hist {
			if m(h)["location_kind"] == "vehicle" {
				hasVehicle = true
			}
			if m(h)["max_c"] != nil {
				hasTemp = true
			}
		}
		if !hasVehicle || !hasTemp || len(hist) < 3 {
			return fail("history %d entries, vehicle %v, temps %v", len(hist), hasVehicle, hasTemp)
		}
		return pass("%d durak, derinlik %v", len(hist), m(hist[0])["depth"])
	})
	r.check("T-3", "API ile bölme + birleştirme sonrası iz korunur", func() (bool, string) {
		op := r.as("depo")
		a, b := r.newLot(1, []int{80}), r.newLot(1, []int{60})
		ua := id(arr(op.get(fmt.Sprintf("/api/v1/units?lot_id=%d", a)).JSON()["items"])[0].(map[string]any)["id"])
		ub := id(arr(op.get(fmt.Sprintf("/api/v1/units?lot_id=%d", b)).JSON()["items"])[0].(map[string]any)["id"])
		sp := op.postIdem(fmt.Sprintf("/api/v1/units/%d/split", ua), uuid.NewString(), map[string]any{"quantity": 30})
		if sp.Status != 201 {
			return fail("split: %s", sp)
		}
		child := id(sp.JSON()["child_unit_id"])
		mg := op.postIdem("/api/v1/units/merge", uuid.NewString(), map[string]any{"unit_ids": []int64{child, ub}})
		if mg.Status != 201 {
			return fail("merge: %s", mg)
		}
		merged := id(mg.JSON()["unit_id"])
		tr := q.get(fmt.Sprintf("/api/v1/trace/lots/%d", a))
		found := false
		for _, u := range arr(tr.JSON()["units"]) {
			if id(m(u)["unit_id"]) == merged {
				found = true
			}
		}
		over := op.post(fmt.Sprintf("/api/v1/units/%d/split", ua), map[string]any{"quantity": 50})
		if !found || over.Status != 409 {
			return fail("merged in trace %v, oversplit %d", found, over.Status)
		}
		return pass("ileri izde birleşik palet %d", merged)
	})
	r.check("T-3", "Eşzamanlı bölme: stok negatife düşmez", func() (bool, string) {
		lot := r.newLot(1, []int{100})
		op := r.as("depo")
		unit := id(arr(op.get(fmt.Sprintf("/api/v1/units?lot_id=%d", lot)).JSON()["items"])[0].(map[string]any)["id"])
		codes := r.parallel(8, func(int) int {
			return op.post(fmt.Sprintf("/api/v1/units/%d/split", unit), map[string]any{"quantity": 30}).Status
		})
		ok := count(codes, 201)
		got := op.get(fmt.Sprintf("/api/v1/lots/%d", lot))
		onHand := num(m(got.JSON()["lot"])["on_hand"])
		if ok != 3 || onHand != 100 {
			return fail("successful splits %d (want 3), on hand %.0f, codes %v", ok, onHand, codes)
		}
		return pass("3 başarılı, 5×409, toplam 100")
	})
}

// ---- T-4 FEFO ----

func (r *runner) t4FEFO() {
	fmt.Println("T-4 Kalan raf ömrü ve FEFO sevkiyat planı")
	ops := r.as("operasyon")
	r.check("T-4", "FEFO planı: kısa raf ömürlü paletler elenir, seçim son kullanma sırasına göre", func() (bool, string) {
		// Self-contained scenario, independent of the seeded lots' current
		// state: three ready-meal lots (72 h shelf life) with 10, 40 and 60
		// hours left in an empty chilled room, and a 3-hour shipment from it.
		room := r.emptyChilledRoom()
		if room == 0 {
			return fail("no empty chilled room")
		}
		op := r.as("depo")
		units := map[int]int64{}
		for _, left := range []int{60, 10, 40} {
			res := op.post("/api/v1/lots", map[string]any{"code": fmt.Sprintf("VER-FEFO-%d-%s", left, uuid.NewString()[:6]), "product_id": 12,
				"location_id": room, "produced_at": time.Now().Add(-time.Duration(72-left) * time.Hour).UTC().Format(time.RFC3339), "pallets": []int{100}})
			if res.Status != 201 {
				return fail("create lot: %s", res)
			}
			units[left] = id(arr(res.JSON()["unit_ids"])[0])
		}
		sid := r.newShipment(room, 180)
		res := ops.post(fmt.Sprintf("/api/v1/shipments/%d/plan", sid), map[string]any{"lines": []map[string]any{{"product_id": 12, "quantity": 1000}}})
		if res.Status != 200 {
			return fail("plan: %s", res)
		}
		line := m(arr(res.JSON()["lines"])[0])
		var sel []int64
		for _, x := range arr(line["selected"]) {
			sel = append(sel, id(m(x)["unit_id"]))
		}
		rejected := false
		for _, x := range arr(line["rejected"]) {
			if id(m(m(x)["candidate"])["unit_id"]) == units[10] && strings.Contains(str(m(x)["reason"]), "shelf life") {
				rejected = true
			}
		}
		if !rejected || len(sel) != 2 || sel[0] != units[40] || sel[1] != units[60] {
			return fail("selected %v (want [%d %d]), 10 h lot rejected: %v", sel, units[40], units[60], rejected)
		}
		r.ctx["planShipment"], r.ctx["rejectedUnit"] = sid, units[10]
		return pass("10 sa kalan elendi; 40 sa, 60 sa sırasıyla seçildi")
	})
	r.check("T-4", "Raf ömrü yetmeyen palet yüklenmeye çalışılınca 409", func() (bool, string) {
		sid, ok := r.ctx["planShipment"].(int64)
		unit, ok2 := r.ctx["rejectedUnit"].(int64)
		if !ok || !ok2 {
			return fail("previous check did not find a plan")
		}
		res := ops.post(fmt.Sprintf("/api/v1/shipments/%d/load", sid), map[string]any{"unit_ids": []int64{unit}})
		if res.Status != 409 || (res.ErrCode() != "shelf_life_too_short" && res.ErrCode() != "lot_not_shippable") {
			return fail("%s", res)
		}
		return pass("409 %s", res.ErrCode())
	})
	r.check("T-4", "Araç kapasitesi (palet) aşılamaz", func() (bool, string) {
		lot := r.newLot(1, repeatInt(10, 40))
		units := arr(ops.get(fmt.Sprintf("/api/v1/units?lot_id=%d&limit=50", lot)).JSON()["items"])
		var ids []int64
		for _, u := range units {
			ids = append(ids, id(m(u)["id"]))
		}
		sid := r.newShipment(r.firstLocation("cold_room"), 60)
		res := ops.post(fmt.Sprintf("/api/v1/shipments/%d/load", sid), map[string]any{"unit_ids": ids})
		if res.Status != 409 || res.ErrCode() != "capacity_exceeded" {
			return fail("%s", res)
		}
		return pass("409 capacity_exceeded")
	})
	r.check("T-4", "Yarış: aynı palet iki sevkiyata aynı anda → yalnız biri", func() (bool, string) {
		lot := r.newLot(1, []int{40})
		unit := id(arr(ops.get(fmt.Sprintf("/api/v1/units?lot_id=%d", lot)).JSON()["items"])[0].(map[string]any)["id"])
		room := r.firstLocation("cold_room")
		s1, s2 := r.newShipment(room, 60), r.newShipment(room, 60)
		codes := r.parallel(2, func(i int) int {
			sid := s1
			if i == 1 {
				sid = s2
			}
			return ops.post(fmt.Sprintf("/api/v1/shipments/%d/load", sid), map[string]any{"unit_ids": []int64{unit}}).Status
		})
		if count(codes, 200) != 1 || count(codes, 409) != 1 {
			return fail("codes %v", codes)
		}
		return pass("200 + 409")
	})
}

func repeatInt(v, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// ---- T-5 integrity and device security ----

func (r *runner) t5Integrity() {
	fmt.Println("T-5 Kanıtlanabilir kayıt ve cihaz güvenliği")
	q := r.as("kalite")
	r.check("T-5", "Sapma raporları hash zinciri: iki rapor bağlanır, zincir geçerli", func() (bool, string) {
		lot, _ := r.ctx["hotLot"].(int64)
		if lot == 0 {
			lot = r.ctx["traceLot"].(int64)
		}
		a := q.postIdem(fmt.Sprintf("/api/v1/lots/%d/excursion-reports", lot), uuid.NewString(), nil)
		b := q.postIdem(fmt.Sprintf("/api/v1/lots/%d/excursion-reports", lot), uuid.NewString(), nil)
		if a.Status != 201 || b.Status != 201 {
			return fail("%s / %s", a, b)
		}
		if b.JSON()["prev_hash"] != a.JSON()["hash"] {
			return fail("report %v does not link to %v", b.JSON()["seq"], a.JSON()["seq"])
		}
		v := r.as("denetci").get("/api/v1/excursion-reports/verify")
		if v.JSON()["valid"] != true {
			return fail("%s", v)
		}
		return pass("%.0f kayıt, baş %s…", num(v.JSON()["entries"]), str(v.JSON()["head_hash"])[:12])
	})
	r.check("T-5", "Raporu değiştiren/silen uç yok (405/404)", func() (bool, string) {
		a := q.do("DELETE", "/api/v1/excursion-reports/1", nil, nil)
		b := q.do("PATCH", "/api/v1/excursion-reports/1", map[string]any{}, nil)
		if (a.Status != 405 && a.Status != 404) || (b.Status != 405 && b.Status != 404) {
			return fail("DELETE %d PATCH %d", a.Status, b.Status)
		}
		return pass("DELETE %d, PATCH %d", a.Status, b.Status)
	})
	body, _ := json.Marshal(map[string]any{"readings": []map[string]any{{"ts": time.Now().UTC().Format(time.RFC3339), "temp_cc": 400, "humidity_pm": 800}}})
	key := devicesig.DeriveKey(r.secret, "S-00003")
	r.check("T-5", "İmzasız ölçüm 401", func() (bool, string) {
		res := r.api.do("POST", "/api/v1/ingest/readings", body, map[string]string{devicesig.HeaderDevice: "S-00003"})
		if res.Status != 401 {
			return fail("%s", res)
		}
		return pass("401")
	})
	r.check("T-5", "Yanlış anahtarla imza 401, gövde değiştirilince 401", func() (bool, string) {
		bad := r.api.signedIngest("S-00003", []byte("wrong-key-wrong-key-wrong-key-00"), uint64(time.Now().UnixMicro()), time.Now(), body)
		seq := uint64(time.Now().UnixMicro())
		ms := time.Now().UnixMilli()
		tampered := r.api.do("POST", "/api/v1/ingest/readings", []byte(strings.Replace(string(body), "400", "300", 1)), map[string]string{
			devicesig.HeaderDevice: "S-00003", devicesig.HeaderSeq: fmt.Sprint(seq), devicesig.HeaderTimestamp: fmt.Sprint(ms),
			devicesig.HeaderSignature: devicesig.Sign(key, "S-00003", seq, ms, body)})
		if bad.Status != 401 || tampered.Status != 401 {
			return fail("wrong key %d, tampered %d", bad.Status, tampered.Status)
		}
		return pass("401 / 401")
	})
	r.check("T-5", "Tekrar (replay) saldırısı 409, eski zaman damgası 401", func() (bool, string) {
		seq := uint64(time.Now().UnixMicro())
		now := time.Now()
		first := r.api.signedIngest("S-00003", key, seq, now, body)
		replay := r.api.signedIngest("S-00003", key, seq, now, body)
		old := r.api.signedIngest("S-00003", key, seq+10, now.Add(-10*time.Minute), body)
		if first.Status != 202 || replay.Status != 409 || old.Status != 401 {
			return fail("first %d, replay %d, stale %d", first.Status, replay.Status, old.Status)
		}
		return pass("202 → 409, eski 401")
	})
	r.check("T-5", "Şoför çevrimdışı senkron: idempotent, mükerrer teslim, yetkisiz sevkiyat", func() (bool, string) {
		drv := r.as("sofor")
		sid, err := r.driverShipment("Serkan Sürücü")
		if err != "" {
			return fail("%s", err)
		}
		e1, e2, e3 := uuid.NewString(), uuid.NewString(), uuid.NewString()
		// recorded on the phone just after departure
		rec := time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
		events := []map[string]any{
			{"client_event_id": e1, "shipment_id": sid, "kind": "temperature_check", "recorded_at": rec, "temp_cc": 420},
			{"client_event_id": e2, "shipment_id": sid, "kind": "delivery", "recorded_at": rec, "receiver_name": "Mağaza Müdürü"},
		}
		first := drv.post("/api/v1/driver/sync", map[string]any{"events": events})
		resend := drv.post("/api/v1/driver/sync", map[string]any{"events": events})
		dup := drv.post("/api/v1/driver/sync", map[string]any{"events": []map[string]any{
			{"client_event_id": e3, "shipment_id": sid, "kind": "delivery", "recorded_at": rec}}})
		foreign := r.as("sofor2").post("/api/v1/driver/sync", map[string]any{"events": []map[string]any{
			{"client_event_id": uuid.NewString(), "shipment_id": sid, "kind": "delivery", "recorded_at": rec}}})
		outcomes := func(res response) []string {
			var out []string
			for _, x := range arr(res.JSON()["results"]) {
				out = append(out, str(m(x)["outcome"]))
			}
			return out
		}
		o1, o2, o3, o4 := outcomes(first), outcomes(resend), outcomes(dup), outcomes(foreign)
		if strings.Join(o1, ",") != "applied,applied" || strings.Join(o2, ",") != "already_synced,already_synced" ||
			strings.Join(o3, ",") != "duplicate_delivery" || strings.Join(o4, ",") != "rejected" {
			return fail("first %v resend %v dup %v foreign %v", o1, o2, o3, o4)
		}
		cust := r.as("operasyon").get(fmt.Sprintf("/api/v1/shipments/%d", sid))
		if m(cust.JSON()["shipment"])["status"] != "delivered" {
			return fail("shipment not delivered")
		}
		return pass("applied → already_synced, duplicate_delivery, rejected")
	})
	r.check("T-5", "Denetim kaydı eklemeli: kritik işlemler kim/ne/eski/yeni ile görünür", func() (bool, string) {
		res := r.as("denetci").get("/api/v1/audit?entity=lot&limit=50")
		actions := map[string]bool{}
		for _, it := range arr(res.JSON()["items"]) {
			a := m(it)
			if a["actor_id"] != nil && (a["new_value"] != nil || a["old_value"] != nil) {
				actions[str(a["action"])] = true
			}
		}
		if !actions["lot.decision"] || !actions["lot.recall"] {
			return fail("actions seen: %v", actions)
		}
		return pass("%d işlem türü", len(actions))
	})
}

// ---- platform: metrics, docs, frontend ----

func (r *runner) platform() {
	fmt.Println("Platform")
	r.check("OPS", "Prometheus /metrics: HTTP ve ingest metrikleri", func() (bool, string) {
		res := r.api.get("/metrics")
		body := string(res.Body)
		if res.Status != 200 || !strings.Contains(body, "http_request_duration_seconds") || !strings.Contains(body, "ingest_requests_rejected_total") {
			return fail("HTTP %d", res.Status)
		}
		return pass("ok")
	})
	r.check("OPS", "OpenAPI dokümanı servis ediliyor", func() (bool, string) {
		res := r.api.get("/openapi.yaml")
		if res.Status != 200 || !strings.Contains(string(res.Body), "openapi:") || !strings.Contains(string(res.Body), "/api/v1/recalls") {
			return fail("HTTP %d", res.Status)
		}
		return pass("%d bayt", len(res.Body))
	})
	r.check("UI", "Arayüz yükleniyor ve /api vekili çalışıyor", func() (bool, string) {
		page := r.frontend.get("/")
		if page.Status != 200 || !strings.Contains(string(page.Body), "<div id=\"root\">") {
			return fail("index: %s", page)
		}
		login := r.frontend.post("/api/v1/auth/login", map[string]string{"email": "depo@sogukzincir.test", "password": "Demo1234!"})
		if login.Status != 200 {
			return fail("login via proxy: %s", login)
		}
		return pass("index + login")
	})
}

// driverShipment puts a fresh shipment on the road on one of the driver's
// vehicles, so the offline-sync check never depends on earlier runs.
func (r *runner) driverShipment(driver string) (int64, string) {
	ops := r.as("operasyon")
	busy := map[int64]bool{}
	for _, st := range []string{"planned", "loading", "in_transit"} {
		for _, it := range arr(ops.get("/api/v1/shipments?limit=200&status=" + st).JSON()["items"]) {
			busy[id(m(it)["vehicle_id"])] = true
		}
	}
	var vehicle int64
	for _, it := range arr(ops.get("/api/v1/vehicles").JSON()["items"]) {
		v := m(it)
		if str(v["driver"]) == driver && !busy[id(v["id"])] {
			vehicle = id(v["id"])
			break
		}
	}
	if vehicle == 0 {
		return 0, "no free vehicle for " + driver
	}
	lot := r.newLot(1, []int{50})
	unit := id(m(arr(ops.get(fmt.Sprintf("/api/v1/units?lot_id=%d", lot)).JSON()["items"])[0])["id"])
	sh := ops.post("/api/v1/shipments", map[string]any{"vehicle_id": vehicle, "customer_id": 1, "origin_id": r.firstLocation("cold_room"),
		"planned_departure": time.Now().UTC().Format(time.RFC3339), "transit_minutes": 90})
	sid := id(sh.JSON()["id"])
	if res := ops.post(fmt.Sprintf("/api/v1/shipments/%d/load", sid), map[string]any{"unit_ids": []int64{unit}}); res.Status != 200 {
		return 0, "load: " + res.String()
	}
	if res := ops.post(fmt.Sprintf("/api/v1/shipments/%d/depart", sid), nil); res.Status != 200 {
		return 0, "depart: " + res.String()
	}
	return sid, ""
}

// emptyChilledRoom returns a cold room without stock, so a scenario does not
// mix with the seeded pallets.
func (r *runner) emptyChilledRoom() int64 {
	res := r.as("operasyon").get("/api/v1/locations?kind=cold_room")
	items := arr(res.JSON()["items"])
	for i := len(items) - 1; i >= 0; i-- {
		l := m(items[i])
		if num(l["active_units"]) == 0 && !strings.Contains(str(l["name"]), "Donuk") {
			return id(l["id"])
		}
	}
	return 0
}
