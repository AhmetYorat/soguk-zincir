package seed

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/auth"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/db"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/domain/devicesig"
	"github.com/AhmetYorat/ddo-case-ahmet-dervis-yorat/backend/internal/jobs"
)

// DemoPassword is shared by all demo accounts (see TESTING.md).
const DemoPassword = "Demo1234!"

// Account is a demo login.
type Account struct {
	Email, Name string
	Role        auth.Role
	Customer    string // customer code for the customer role
}

// DemoAccounts: one per role, plus a second driver and customer to show that
// data of others stays invisible.
var DemoAccounts = []Account{
	{"depo@sogukzincir.test", "Deniz Depocu", auth.RoleWarehouseOperator, ""},
	{"sofor@sogukzincir.test", "Serkan Sürücü", auth.RoleDriver, ""},
	{"sofor2@sogukzincir.test", "Selin Sürücü", auth.RoleDriver, ""},
	{"kalite@sogukzincir.test", "Kerem Kaliteci", auth.RoleQualityManager, ""},
	{"operasyon@sogukzincir.test", "Oya Operasyon", auth.RoleOperationsManager, ""},
	{"market@sogukzincir.test", "Mert Marketçi", auth.RoleCustomer, "MKT-001"},
	{"market2@sogukzincir.test", "Melis Marketçi", auth.RoleCustomer, "MKT-002"},
	{"denetci@sogukzincir.test", "Defne Denetçi", auth.RoleAuditor, ""},
}

type productSpec struct {
	sku, name                string
	minCC, maxCC             int
	cumS, windowS, windowMax int
	shelfH                   int
	policy                   string
	weightG                  int
	priceKurus               int64
	frozen                   bool
}

var products = []productSpec{
	{"SUT-1L", "Tam Yağlı Süt 1 L", 200, 800, 1800, 3600, 600, 168, "uncertain", 1050, 3450, false},
	{"YOG-1KG", "Süzme Yoğurt 1 kg", 200, 800, 1800, 3600, 600, 336, "uncertain", 1000, 8990, false},
	{"PEY-500", "Beyaz Peynir 500 g", 200, 800, 2700, 3600, 900, 720, "uncertain", 520, 17950, false},
	{"TAV-BUT", "Tavuk But 1 kg", 0, 500, 900, 3600, 300, 120, "uncertain", 1000, 18900, false},
	{"KIY-500", "Dana Kıyma 500 g", 0, 500, 900, 3600, 300, 96, "uncertain", 500, 32500, false},
	{"SOM-300", "Somon Fileto 300 g", 0, 500, 600, 3600, 300, 144, "uncertain", 300, 41900, false},
	{"DON-VAN", "Vanilyalı Dondurma 1 L", -2500, -1800, 1800, 3600, 900, 4320, "uncertain", 600, 15900, true},
	{"DON-SEB", "Donuk Karışık Sebze 1 kg", -2500, -1800, 3600, 3600, 1200, 8760, "interpolate", 1000, 6450, true},
	{"SAL-250", "Hazır Yeşil Salata 250 g", 100, 600, 1200, 3600, 600, 120, "uncertain", 250, 4990, false},
	{"MEY-1L", "Taze Portakal Suyu 1 L", 200, 800, 3600, 3600, 1200, 240, "interpolate", 1080, 6990, false},
	{"TER-250", "Tereyağı 250 g", 200, 800, 3600, 3600, 1200, 1440, "uncertain", 260, 16500, false},
	{"HAZ-400", "Hazır Yemek Etli Nohut 400 g", 0, 500, 900, 3600, 300, 72, "uncertain", 420, 11900, false},
}

var marketNames = []string{"Kardeşler Market", "Yıldız Gıda", "Bereket Süpermarket", "Anadolu Şarküteri", "Doğa Market",
	"Mavi Bakkal", "Çınar Market", "Güneş Gıda", "Ege Taze", "Lale Market"}
var districts = []string{"Kadıköy", "Beşiktaş", "Üsküdar", "Bakırköy", "Çankaya", "Keçiören", "Bornova", "Karşıyaka", "Nilüfer", "Osmangazi"}

// Options controls the seed.
type Options struct {
	Seed            int64
	Sensors         int
	BackfillHours   int
	ProvisionSecret []byte
	Keybox          *devicesig.Keybox
	Now             time.Time
	Log             *slog.Logger
}

// Reset drops everything; the caller migrates and seeds again. The append-only
// triggers make TRUNCATE impossible by design, so a demo reset recreates the
// schema instead.
func Reset(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`)
	return err
}

// IsSeeded reports whether demo data exists.
func IsSeeded(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	var n int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n > 0, err
}

type seeder struct {
	opt     Options
	rng     *rand.Rand
	tx      pgx.Tx
	ctx     context.Context
	now     time.Time
	layout  Layout
	users   map[string]int64
	cust    []int64 // customer ids by index
	custLoc []int64 // customer location ids
	rooms   map[string]int64
	roomIdx []int64 // room location ids by layout index
	vehLoc  []int64 // vehicle location ids
	vehID   []int64
	prodID  []int64
	units   []*unit
	lotSeq  int
	shipSeq int
}

type unit struct {
	id       int64
	code     string
	loc      int64
	lot      int64 // single-lot units only; 0 for merged
	product  int
	qty      int
	lastAt   time.Time
	busy     bool // consumed, shipped or used by the trace demo
	children int
}

// Run seeds an empty, migrated database.
func Run(ctx context.Context, pool *pgxpool.Pool, opt Options) error {
	start := time.Now()
	s := &seeder{
		opt: opt, ctx: ctx, now: opt.Now.UTC().Truncate(time.Second),
		rng:    rand.New(rand.NewPCG(uint64(opt.Seed), 0x5eed)),
		layout: NewLayout(opt.Sensors),
		users:  map[string]int64{}, rooms: map[string]int64{},
	}
	// partitions for the backfill must exist before inserting readings
	back := time.Duration(opt.BackfillHours+48) * time.Hour
	if err := db.MaintainPartitions(ctx, pool, s.now, back, db.DefaultRetention(7*24*time.Hour), opt.Log); err != nil {
		return err
	}
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		s.tx = tx
		steps := []struct {
			name string
			fn   func() error
		}{
			{"customers", s.customers}, {"users", s.usersStep}, {"sites", s.sites}, {"sensors", s.sensors},
			{"products", s.products}, {"lots", s.lots}, {"trace demo", s.traceDemo}, {"splits and merges", s.lineage},
			{"shipments", s.shipments}, {"haccp", s.haccp},
		}
		for _, st := range steps {
			if err := st.fn(); err != nil {
				return fmt.Errorf("seed %s: %w", st.name, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	opt.Log.Info("seeded master data", "duration_ms", time.Since(start).Milliseconds())
	if err := s.readings(pool); err != nil {
		return fmt.Errorf("seed readings: %w", err)
	}
	from := s.now.Add(-time.Duration(opt.BackfillHours) * time.Hour)
	if err := jobs.Rollup(ctx, pool, s.now, &from); err != nil {
		return fmt.Errorf("seed rollups: %w", err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE`); err != nil {
		return err
	}
	opt.Log.Info("seed complete", "duration_ms", time.Since(start).Milliseconds())
	return nil
}

func (s *seeder) exec(sql string, args ...any) error {
	_, err := s.tx.Exec(s.ctx, sql, args...)
	return err
}

func (s *seeder) id(sql string, args ...any) (int64, error) {
	var id int64
	err := s.tx.QueryRow(s.ctx, sql, args...).Scan(&id)
	return id, err
}

func (s *seeder) customers() error {
	for i := 0; i < 40; i++ {
		code := fmt.Sprintf("MKT-%03d", i+1)
		name := fmt.Sprintf("%s %s", marketNames[i%len(marketNames)], districts[(i/len(marketNames)+i)%len(districts)])
		id, err := s.id(`INSERT INTO customers (code, name, address) VALUES ($1, $2, $3) RETURNING id`,
			code, name, districts[i%len(districts)]+" / Türkiye")
		if err != nil {
			return err
		}
		loc, err := s.id(`INSERT INTO locations (code, name, kind, customer_id) VALUES ($1, $2, 'customer', $3) RETURNING id`,
			"CUS-"+code, name, id)
		if err != nil {
			return err
		}
		s.cust = append(s.cust, id)
		s.custLoc = append(s.custLoc, loc)
	}
	return nil
}

func (s *seeder) usersStep() error {
	hash, err := auth.HashPassword(DemoPassword)
	if err != nil {
		return err
	}
	for _, a := range DemoAccounts {
		var cust *int64
		if a.Customer != "" {
			var c int64
			if err := s.tx.QueryRow(s.ctx, `SELECT id FROM customers WHERE code = $1`, a.Customer).Scan(&c); err != nil {
				return err
			}
			cust = &c
		}
		id, err := s.id(`INSERT INTO users (email, full_name, password_hash, role, customer_id) VALUES ($1,$2,$3,$4,$5) RETURNING id`,
			a.Email, a.Name, hash, a.Role, cust)
		if err != nil {
			return err
		}
		s.users[a.Email] = id
	}
	return nil
}

func (s *seeder) sites() error {
	whIDs := make([]int64, len(s.layout.Warehouses))
	for i, w := range s.layout.Warehouses {
		id, err := s.id(`INSERT INTO warehouses (code, name, city) VALUES ($1,$2,$3) RETURNING id`, w.Code, w.Name, w.City)
		if err != nil {
			return err
		}
		whIDs[i] = id
	}
	for _, r := range s.layout.Rooms {
		id, err := s.id(`INSERT INTO locations (code, name, kind, warehouse_id) VALUES ($1,$2,'cold_room',$3) RETURNING id`,
			r.Code, r.Name, whIDs[r.Warehouse])
		if err != nil {
			return err
		}
		s.rooms[r.Code] = id
		s.roomIdx = append(s.roomIdx, id)
	}
	drivers := []string{"sofor@sogukzincir.test", "sofor@sogukzincir.test", "sofor@sogukzincir.test",
		"sofor2@sogukzincir.test", "sofor2@sogukzincir.test"}
	for i, v := range s.layout.Vehicles {
		var driver *int64
		if i < len(drivers) {
			d := s.users[drivers[i]]
			driver = &d
		}
		vid, err := s.id(`INSERT INTO vehicles (plate, capacity_kg, capacity_pallets, driver_id) VALUES ($1,$2,$3,$4) RETURNING id`,
			v.Plate, v.CapacityKG, v.CapacityPallets, driver)
		if err != nil {
			return err
		}
		loc, err := s.id(`INSERT INTO locations (code, name, kind, vehicle_id) VALUES ($1,$2,'vehicle',$3) RETURNING id`,
			VehicleLocation(i), "Araç "+v.Plate, vid)
		if err != nil {
			return err
		}
		s.vehID = append(s.vehID, vid)
		s.vehLoc = append(s.vehLoc, loc)
	}
	return nil
}

func (s *seeder) sensors() error {
	rows := make([][]any, 0, len(s.layout.Sensors))
	for _, sn := range s.layout.Sensors {
		key := devicesig.DeriveKey(s.opt.ProvisionSecret, sn.Code)
		ct, nonce, err := s.opt.Keybox.Seal(sn.Code, key)
		if err != nil {
			return err
		}
		var loc int64
		if sn.Vehicle {
			var idx int
			fmt.Sscanf(sn.Location, "VEH-%d", &idx)
			loc = s.vehLoc[idx-1]
		} else {
			loc = s.rooms[sn.Location]
		}
		battery := 35 + s.rng.IntN(66)
		if sn.Index%97 == 0 {
			battery = 8 + s.rng.IntN(10) // a few weak batteries for the health report
		}
		rows = append(rows, []any{sn.Code, loc, ct, nonce, int16(battery)})
	}
	_, err := s.tx.CopyFrom(s.ctx, pgx.Identifier{"sensors"}, []string{"code", "location_id", "key_ciphertext", "key_nonce", "battery_pct"},
		pgx.CopyFromRows(rows))
	return err
}

func (s *seeder) products() error {
	for _, p := range products {
		id, err := s.id(`INSERT INTO products (sku, name, temp_min_cc, temp_max_cc, max_cumulative_excursion_s, window_s,
			window_max_excursion_s, shelf_life_h, gap_policy, unit_weight_g, list_price_kurus)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`,
			p.sku, p.name, p.minCC, p.maxCC, p.cumS, p.windowS, p.windowMax, p.shelfH, p.policy, p.weightG, p.priceKurus)
		if err != nil {
			return err
		}
		s.prodID = append(s.prodID, id)
	}
	return nil
}

// stockRooms are the first rooms of the first two warehouses; goods are kept
// together so that rooms and their sensors matter for the demo.
func (s *seeder) stockRooms(frozen bool) []int {
	var out []int
	perWh := len(s.layout.Rooms) / len(s.layout.Warehouses)
	for i, r := range s.layout.Rooms {
		n := i % max(perWh, 1)
		if r.Frozen == frozen && n < 30 && r.Warehouse < 2 {
			out = append(out, i)
		}
	}
	if len(out) == 0 {
		for i, r := range s.layout.Rooms {
			if r.Frozen == frozen {
				out = append(out, i)
			}
		}
	}
	if len(out) == 0 {
		out = []int{0}
	}
	return out
}

func (s *seeder) newUnit(code string, loc int64, product int, lot int64, qty int, at time.Time) (*unit, error) {
	id, err := s.id(`INSERT INTO units (code, location_id, created_at) VALUES ($1,$2,$3) RETURNING id`, code, loc, at)
	if err != nil {
		return nil, err
	}
	if err := s.exec(`INSERT INTO custody_events (unit_id, location_id, from_ts) VALUES ($1,$2,$3)`, id, loc, at); err != nil {
		return nil, err
	}
	u := &unit{id: id, code: code, loc: loc, lot: lot, product: product, qty: qty, lastAt: at}
	s.units = append(s.units, u)
	return u, nil
}

func (s *seeder) contents(unitID, lotID int64, qty int) error {
	return s.exec(`INSERT INTO unit_contents (unit_id, lot_id, quantity) VALUES ($1,$2,$3)
		ON CONFLICT (unit_id, lot_id) DO UPDATE SET quantity = EXCLUDED.quantity`, unitID, lotID, qty)
}

func (s *seeder) edge(parent *unit, child *unit, op string, lot int64, qty int, at time.Time) error {
	var p *int64
	if parent != nil {
		p = &parent.id
	}
	return s.exec(`INSERT INTO unit_lineage (parent_unit_id, child_unit_id, op, lot_id, quantity, at) VALUES ($1,$2,$3,$4,$5,$6)`,
		p, child.id, op, lot, qty, at)
}

func (s *seeder) move(u *unit, loc int64, at time.Time) error {
	if err := s.exec(`UPDATE custody_events SET to_ts = $2 WHERE unit_id = $1 AND to_ts IS NULL`, u.id, at); err != nil {
		return err
	}
	if err := s.exec(`UPDATE units SET location_id = $2 WHERE id = $1`, u.id, loc); err != nil {
		return err
	}
	u.loc, u.lastAt = loc, at
	return s.exec(`INSERT INTO custody_events (unit_id, location_id, from_ts) VALUES ($1,$2,$3)`, u.id, loc, at)
}

// createLot books a lot with pallets in a room.
func (s *seeder) createLot(code string, product int, room int, produced time.Time, pallets []int) ([]*unit, int64, error) {
	total := 0
	for _, q := range pallets {
		total += q
	}
	lotID, err := s.id(`INSERT INTO lots (code, product_id, produced_at, quantity) VALUES ($1,$2,$3,$4) RETURNING id`,
		code, s.prodID[product], produced, total)
	if err != nil {
		return nil, 0, err
	}
	received := produced.Add(2 * time.Hour)
	if received.After(s.now.Add(-time.Hour)) {
		received = s.now.Add(-time.Hour)
	}
	var out []*unit
	for i, q := range pallets {
		u, err := s.newUnit(fmt.Sprintf("P-%s-%02d", code, i+1), s.roomIdx[room], product, lotID, q, received)
		if err != nil {
			return nil, 0, err
		}
		if err := s.contents(u.id, lotID, q); err != nil {
			return nil, 0, err
		}
		if err := s.edge(nil, u, "receive", lotID, q, received); err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	return out, lotID, nil
}

func (s *seeder) lots() error {
	for i := 0; i < 300; i++ {
		p := i % len(products)
		spec := products[p]
		rooms := s.stockRooms(spec.frozen)
		room := rooms[s.rng.IntN(len(rooms))]
		maxAge := min(time.Duration(spec.shelfH)*time.Hour*6/10, 5*24*time.Hour)
		age := 6*time.Hour + time.Duration(s.rng.Int64N(int64(maxAge)))
		if i%25 == 11 {
			// a few lots close to their end of life: FEFO must not ship them far
			age = time.Duration(spec.shelfH)*time.Hour - time.Duration(10+s.rng.IntN(20))*time.Hour
		}
		n := 2 + s.rng.IntN(4)
		pallets := make([]int, n)
		for k := range pallets {
			pallets[k] = 120 + 20*s.rng.IntN(19)
		}
		s.lotSeq++
		code := fmt.Sprintf("L%s-%04d", spec.sku[:3], s.lotSeq)
		if _, _, err := s.createLot(code, p, room, s.now.Add(-age), pallets); err != nil {
			return err
		}
	}
	return nil
}

// traceDemo builds the lineage used in TESTING.md for T-3: one milk lot,
// split three levels deep, merged with another lot and shipped to four
// customers on different vehicles.
func (s *seeder) traceDemo() error {
	room := s.stockRooms(false)[0]
	produced := s.now.Add(-30 * time.Hour)
	ps, lot, err := s.createLot("LOT-TRACE-001", 0, room, produced, []int{400, 400, 400, 400})
	if err != nil {
		return err
	}
	other, otherLot, err := s.createLot("LOT-TRACE-002", 0, room, produced.Add(time.Hour), []int{300})
	if err != nil {
		return err
	}
	t := s.now.Add(-20 * time.Hour)
	split := func(parent *unit, lotID int64, qty int, at time.Time) (*unit, error) {
		parent.children++
		child, err := s.newUnit(fmt.Sprintf("%s.%d", parent.code, parent.children), parent.loc, parent.product, lotID, qty, at)
		if err != nil {
			return nil, err
		}
		if err := s.contents(child.id, lotID, qty); err != nil {
			return nil, err
		}
		parent.qty -= qty
		if err := s.contents(parent.id, lotID, parent.qty); err != nil {
			return nil, err
		}
		return child, s.edge(parent, child, "split", lotID, qty, at)
	}
	a, err := split(ps[0], lot, 150, t)
	if err != nil {
		return err
	}
	b, err := split(a, lot, 60, t.Add(time.Hour))
	if err != nil {
		return err
	}
	c, err := split(b, lot, 20, t.Add(2*time.Hour))
	if err != nil {
		return err
	}
	o, err := split(other[0], otherLot, 100, t.Add(2*time.Hour))
	if err != nil {
		return err
	}
	// merge c (LOT-TRACE-001) with o (LOT-TRACE-002) into a mixed pallet
	mt := t.Add(3 * time.Hour)
	m, err := s.newUnit("M-TRACE-01", c.loc, 0, 0, c.qty+o.qty, mt)
	if err != nil {
		return err
	}
	for _, part := range []struct {
		u   *unit
		lot int64
	}{{c, lot}, {o, otherLot}} {
		if err := s.contents(m.id, part.lot, part.u.qty); err != nil {
			return err
		}
		if err := s.edge(part.u, m, "merge", part.lot, part.u.qty, mt); err != nil {
			return err
		}
		if err := s.contents(part.u.id, part.lot, 0); err != nil {
			return err
		}
		if err := s.exec(`UPDATE units SET status = 'consumed' WHERE id = $1`, part.u.id); err != nil {
			return err
		}
		if err := s.exec(`UPDATE custody_events SET to_ts = $2 WHERE unit_id = $1 AND to_ts IS NULL`, part.u.id, mt); err != nil {
			return err
		}
		part.u.busy = true
	}
	// four deliveries on four vehicles
	deliveries := []struct {
		units    []*unit
		customer int
		vehicle  int
		at       time.Time
	}{
		{[]*unit{m}, 2, 10, t.Add(4 * time.Hour)},
		{[]*unit{ps[1]}, 0, 11, t.Add(5 * time.Hour)},
		{[]*unit{ps[0], a}, 4, 12, t.Add(6 * time.Hour)},
		{[]*unit{b}, 1, 13, t.Add(7 * time.Hour)},
	}
	for _, d := range deliveries {
		if err := s.deliver(d.units, d.customer, d.vehicle%len(s.vehID), d.at, 3*time.Hour); err != nil {
			return err
		}
	}
	for _, u := range append(ps, other...) {
		u.busy = true
	}
	return nil
}

// lineage splits and merges ordinary pallets so traceability has depth.
func (s *seeder) lineage() error {
	var children []*unit
	for _, u := range slices.Clone(s.units) {
		if u.busy || u.lot == 0 || s.rng.IntN(10) >= 4 || u.qty < 200 {
			continue
		}
		at := maxTime(u.lastAt, s.now.Add(-22*time.Hour)).Add(time.Duration(10+s.rng.IntN(300)) * time.Minute)
		if at.After(s.now.Add(-2 * time.Hour)) {
			continue
		}
		qty := 40 + 10*s.rng.IntN(u.qty/20)
		if qty >= u.qty {
			qty = u.qty / 2
		}
		u.children++
		child, err := s.newUnit(fmt.Sprintf("%s.%d", u.code, u.children), u.loc, u.product, u.lot, qty, at)
		if err != nil {
			return err
		}
		if err := s.contents(child.id, u.lot, qty); err != nil {
			return err
		}
		u.qty -= qty
		u.lastAt = at
		if err := s.contents(u.id, u.lot, u.qty); err != nil {
			return err
		}
		if err := s.edge(u, child, "split", u.lot, qty, at); err != nil {
			return err
		}
		children = append(children, child)
	}
	// merge pairs of children of the same product in the same room
	merges := 0
	for i := 0; i < len(children) && merges < 25; i++ {
		x := children[i]
		if x.busy {
			continue
		}
		for j := i + 1; j < len(children); j++ {
			y := children[j]
			if y.busy || y.loc != x.loc || y.product != x.product || y.lot == x.lot {
				continue
			}
			at := maxTime(x.lastAt, y.lastAt).Add(30 * time.Minute)
			if at.After(s.now.Add(-90 * time.Minute)) {
				break
			}
			merges++
			m, err := s.newUnit(fmt.Sprintf("M-%04d", merges), x.loc, x.product, 0, x.qty+y.qty, at)
			if err != nil {
				return err
			}
			for _, part := range []*unit{x, y} {
				if err := s.contents(m.id, part.lot, part.qty); err != nil {
					return err
				}
				if err := s.edge(part, m, "merge", part.lot, part.qty, at); err != nil {
					return err
				}
				if err := s.contents(part.id, part.lot, 0); err != nil {
					return err
				}
				if err := s.exec(`UPDATE units SET status = 'consumed' WHERE id = $1`, part.id); err != nil {
					return err
				}
				if err := s.exec(`UPDATE custody_events SET to_ts = $2 WHERE unit_id = $1 AND to_ts IS NULL`, part.id, at); err != nil {
					return err
				}
				part.busy = true
			}
			break
		}
	}
	return nil
}

func (s *seeder) shipment(vehicle, customer int, origin int64, status string, planned time.Time, transit time.Duration) (int64, error) {
	s.shipSeq++
	return s.id(`INSERT INTO shipments (code, vehicle_id, customer_id, origin_id, status, planned_departure, transit_s, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`,
		fmt.Sprintf("SHP-%05d", s.shipSeq), s.vehID[vehicle], s.cust[customer], origin, status, planned,
		int(transit.Seconds()), s.users["operasyon@sogukzincir.test"])
}

// deliver ships units on a vehicle at `at` and hands them over after transit.
func (s *seeder) deliver(units []*unit, customer, vehicle int, at time.Time, transit time.Duration) error {
	id, err := s.shipment(vehicle, customer, units[0].loc, "delivered", at, transit)
	if err != nil {
		return err
	}
	dep, arr := at.Add(20*time.Minute), at.Add(20*time.Minute+transit)
	if err := s.exec(`UPDATE shipments SET departed_at = $2, delivered_at = $3 WHERE id = $1`, id, dep, arr); err != nil {
		return err
	}
	for _, u := range units {
		if err := s.exec(`INSERT INTO shipment_items (shipment_id, unit_id, loaded_at, loaded_by) VALUES ($1,$2,$3,$4)`,
			id, u.id, at, s.users["operasyon@sogukzincir.test"]); err != nil {
			return err
		}
		if err := s.move(u, s.vehLoc[vehicle], at); err != nil {
			return err
		}
		if err := s.move(u, s.custLoc[customer], arr); err != nil {
			return err
		}
		if err := s.exec(`UPDATE units SET status = 'delivered' WHERE id = $1`, u.id); err != nil {
			return err
		}
		u.busy = true
	}
	return nil
}

func (s *seeder) shipments() error {
	free := func() []*unit {
		var out []*unit
		for _, u := range s.units {
			if !u.busy && u.qty > 0 {
				out = append(out, u)
			}
		}
		return out
	}
	// delivered history, on vehicles without a demo driver
	fleet := max(len(s.vehID)-20, 1)
	for k := 0; k < 120; k++ {
		pool := free()
		if len(pool) < 10 {
			break
		}
		first := pool[s.rng.IntN(len(pool))]
		group := []*unit{first}
		for _, u := range pool {
			if len(group) >= 1+s.rng.IntN(4) {
				break
			}
			if u != first && u.loc == first.loc {
				group = append(group, u)
			}
		}
		latest := first.lastAt
		for _, u := range group {
			latest = maxTime(latest, u.lastAt)
		}
		transit := time.Duration(60+s.rng.IntN(240)) * time.Minute
		at := maxTime(latest, s.now.Add(-22*time.Hour)).Add(time.Duration(20+s.rng.IntN(240)) * time.Minute)
		if at.Add(transit + 30*time.Minute).After(s.now) {
			continue
		}
		vehicle := (20 + k%fleet) % len(s.vehID)
		if err := s.deliver(group, 5+s.rng.IntN(len(s.cust)-5), vehicle, at, transit); err != nil {
			return err
		}
	}
	// shipments on the road now, for the driver app: vehicles 0, 1 (sofor), 3 (sofor2)
	for _, v := range []int{0, 1, 3} {
		var group []*unit
		for _, u := range free() {
			if s.layout.Rooms[slices.Index(s.roomIdx, u.loc)].Frozen || u.lastAt.After(s.now.Add(-2*time.Hour)) {
				continue
			}
			if len(group) == 0 || u.loc == group[0].loc {
				group = append(group, u)
			}
			if len(group) == 3 {
				break
			}
		}
		if len(group) == 0 {
			continue
		}
		at := s.now.Add(-90 * time.Minute)
		customer := v % 2 // MKT-001 / MKT-002, the demo customer accounts
		id, err := s.shipment(v, customer, group[0].loc, "in_transit", at, 3*time.Hour)
		if err != nil {
			return err
		}
		if err := s.exec(`UPDATE shipments SET departed_at = $2 WHERE id = $1`, id, at.Add(10*time.Minute)); err != nil {
			return err
		}
		for _, u := range group {
			if err := s.exec(`INSERT INTO shipment_items (shipment_id, unit_id, loaded_at, loaded_by) VALUES ($1,$2,$3,$4)`,
				id, u.id, at, s.users["operasyon@sogukzincir.test"]); err != nil {
				return err
			}
			if err := s.move(u, s.vehLoc[v], at); err != nil {
				return err
			}
			if err := s.exec(`UPDATE units SET status = 'loaded' WHERE id = $1`, u.id); err != nil {
				return err
			}
			u.busy = true
		}
	}
	// planned shipments from the busiest chilled rooms, ready for FEFO planning
	chilled := s.stockRooms(false)
	for k := 0; k < 6 && k < len(chilled); k++ {
		if _, err := s.shipment(5+k, 2+k, s.roomIdx[chilled[k]], "planned", s.now.Add(time.Duration(2+k)*time.Hour),
			time.Duration(3+k)*time.Hour); err != nil {
			return err
		}
	}
	return nil
}

func (s *seeder) haccp() error {
	type item struct {
		ccp, q, kind string
		min, max     int
	}
	lists := []struct {
		code, name, freq string
		items            []item
	}{
		{"HACCP-ODA", "Soğuk oda vardiya kontrolü", "per_shift", []item{
			{"CCP-1", "Oda sıcaklığı (°C)", "temperature", 0, 800},
			{"PRP-1", "Kapı contası sağlam ve kapı tam kapanıyor mu?", "boolean", 0, 0},
			{"PRP-2", "Termometre kalibrasyon etiketi geçerli mi?", "boolean", 0, 0},
			{"PRP-3", "Zemin temiz ve kuru mu?", "boolean", 0, 0},
		}},
		{"HACCP-YUK", "Araç yükleme kontrolü", "per_shipment", []item{
			{"CCP-2", "Araç ön soğutma sıcaklığı (°C)", "temperature", -100, 600},
			{"PRP-4", "Araç kasası temiz ve kokusuz mu?", "boolean", 0, 0},
			{"PRP-5", "Paletler hava akışını engellemeyecek şekilde dizildi mi?", "boolean", 0, 0},
		}},
		{"HACCP-KABUL", "Mal kabul kontrolü", "per_shipment", []item{
			{"CCP-3", "Ürün çekirdek sıcaklığı (°C)", "temperature", 0, 800},
			{"PRP-6", "Ambalaj hasarsız mı?", "boolean", 0, 0},
			{"PRP-7", "Etiket ve parti numarası okunaklı mı?", "boolean", 0, 0},
		}},
	}
	operator := s.users["depo@sogukzincir.test"]
	for li, l := range lists {
		cid, err := s.id(`INSERT INTO haccp_checklists (code, name, frequency) VALUES ($1,$2,$3) RETURNING id`, l.code, l.name, l.freq)
		if err != nil {
			return err
		}
		var itemIDs []int64
		for pos, it := range l.items {
			var mn, mx *int
			if it.kind == "temperature" {
				mn, mx = &it.min, &it.max
			}
			id, err := s.id(`INSERT INTO haccp_items (checklist_id, position, ccp_code, question, kind, min_cc, max_cc)
				VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`, cid, pos+1, it.ccp, it.q, it.kind, mn, mx)
			if err != nil {
				return err
			}
			itemIDs = append(itemIDs, id)
		}
		for k := 0; k < 8; k++ {
			at := s.now.Add(-time.Duration(8*k+li) * time.Hour)
			fail := k == 3 && li == 0
			rid, err := s.id(`INSERT INTO haccp_records (checklist_id, location_id, performed_by, performed_at, all_ok, notes)
				VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`, cid, s.roomIdx[k%len(s.roomIdx)], operator, at, !fail, "")
			if err != nil {
				return err
			}
			for pos, it := range l.items {
				var bv *bool
				var temp *int
				ok := true
				var action *string
				if it.kind == "boolean" {
					v := !(fail && pos == 1)
					bv, ok = &v, v
				} else {
					v := (it.min+it.max)/2 + s.rng.IntN(100)
					temp = &v
				}
				if !ok {
					a := "Kapı contası değiştirildi, oda sıcaklığı 30 dk izlendi."
					action = &a
				}
				if err := s.exec(`INSERT INTO haccp_answers (record_id, item_id, bool_value, temp_cc, ok, corrective_action)
					VALUES ($1,$2,$3,$4,$5,$6)`, rid, itemIDs[pos], bv, temp, ok, action); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// readings backfills history for every sensor in one INSERT … SELECT over
// generate_series. Noise, door-open spikes and outages are derived from
// hashtextextended(seed, …) so the same seed gives the same data.
func (s *seeder) readings(pool *pgxpool.Pool) error {
	hours := s.opt.BackfillHours
	if hours <= 0 {
		return nil
	}
	start := time.Now()
	from := s.now.Add(-time.Duration(hours) * time.Hour)
	hotRoom := s.layout.Rooms[0].Code
	if r := s.stockRooms(false); len(r) > 1 {
		hotRoom = s.layout.Rooms[r[1]].Code
	}
	// One statement per hour-slice keeps each transaction small.
	for h := 0; h < hours; h++ {
		sliceFrom := from.Add(time.Duration(h) * time.Hour)
		_, err := pool.Exec(s.ctx, `
			WITH sens AS (
			    SELECT s.id AS sid, l.code AS loc, l.id AS lid, (l.name LIKE 'Donuk%') AS frozen,
			           abs(hashtextextended($1 || ':spike:' || l.id, 0)) AS hs,
			           abs(hashtextextended($1 || ':gap:' || s.id, 0)) AS hg
			    FROM sensors s JOIN locations l ON l.id = s.location_id
			), g AS (
			    SELECT sens.*, ts, (extract(epoch FROM ts - $2::timestamptz) / 30)::int AS i
			    FROM sens, generate_series($3::timestamptz, $3::timestamptz + interval '1 hour' - interval '30 seconds', interval '30 seconds') AS ts
			)
			INSERT INTO readings (sensor_id, ts, temp_cc, humidity_pm)
			SELECT sid, ts,
			    (`+BaseTempSQL+`
			     + (abs(hashtextextended($1 || ':' || sid || ':' || i, 0)) % 81) - 40
			     -- door-open spike: one in 12 rooms/vehicles, 8-28 minutes at about +7 °C
			     + CASE WHEN hs % 12 = 0 AND i BETWEEN (hs / 12) % ($4 * 120) AND (hs / 12) % ($4 * 120) + 16 + hs % 40 THEN 700 ELSE 0 END
			     -- the demo hot room: a 50-minute excursion 6 hours ago
			     + CASE WHEN loc = $5 AND ts >= $6::timestamptz AND ts < $6::timestamptz + interval '50 minutes' THEN 800 ELSE 0 END
			    )::smallint,
			    (CASE WHEN frozen THEN 600 ELSE 850 END + (abs(hashtextextended($1 || ':h:' || sid || ':' || i, 0)) % 61) - 30)::smallint
			FROM g
			-- outages: one sensor in 60 is silent for 20 minutes
			WHERE NOT (hg % 60 = 0 AND i BETWEEN (hg / 60) % ($4 * 120) AND (hg / 60) % ($4 * 120) + 40)
			ON CONFLICT DO NOTHING`,
			fmt.Sprint(s.opt.Seed), from, sliceFrom, hours, hotRoom, s.now.Add(-6*time.Hour))
		if err != nil {
			return err
		}
	}
	if _, err := pool.Exec(s.ctx, `UPDATE sensors s SET last_seen_at = (SELECT max(ts) FROM readings r WHERE r.sensor_id = s.id)`); err != nil {
		return err
	}
	s.opt.Log.Info("backfilled readings", "hours", hours, "sensors", len(s.layout.Sensors), "duration_ms", time.Since(start).Milliseconds())
	return nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
