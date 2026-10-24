// Package seed creates deterministic demo data. The physical layout
// (warehouses, rooms, vehicles, sensors) is a pure function shared with the
// simulator, so both agree on device codes without talking to each other.
package seed

import (
	"fmt"
	"math"
	"time"
)

// Warehouse of the demo company.
type Warehouse struct {
	Code, Name, City string
}

// Room is a cold room inside a warehouse.
type Room struct {
	Code      string
	Name      string
	Warehouse int // index into Layout.Warehouses
	Frozen    bool
}

// Vehicle is a reefer truck.
type Vehicle struct {
	Plate           string
	CapacityKG      int
	CapacityPallets int
}

// Sensor is a temperature/humidity probe.
type Sensor struct {
	Code     string
	Location string // room code or vehicle location code
	Vehicle  bool
	Frozen   bool
	Index    int // 1-based, also the database id after seeding
}

// Layout is the whole physical setup.
type Layout struct {
	Warehouses []Warehouse
	Rooms      []Room
	Vehicles   []Vehicle
	Sensors    []Sensor
}

// SensorsPerRoom: rooms carry several probes (door, back wall, ceiling, floor).
const SensorsPerRoom = 4

// NewLayout returns the layout for `sensors` probes: 80 % in cold rooms
// (4 per room), 20 % in vehicles (1 per truck).
func NewLayout(sensors int) Layout {
	if sensors < 10 {
		sensors = 10
	}
	l := Layout{Warehouses: []Warehouse{
		{"IST", "İstanbul Hadımköy Depo", "İstanbul"},
		{"ANK", "Ankara Sincan Depo", "Ankara"},
		{"IZM", "İzmir Kemalpaşa Depo", "İzmir"},
		{"BUR", "Bursa Nilüfer Depo", "Bursa"},
	}}
	vehicles := sensors / 5
	rooms := (sensors - vehicles) / SensorsPerRoom
	vehicles = sensors - rooms*SensorsPerRoom
	perWarehouse := (rooms + len(l.Warehouses) - 1) / len(l.Warehouses)
	for i := 0; i < rooms; i++ {
		w := i / perWarehouse
		n := i%perWarehouse + 1
		frozen := n%7 == 3
		kind := "Soğuk Oda"
		if frozen {
			kind = "Donuk Oda"
		}
		l.Rooms = append(l.Rooms, Room{
			Code:      fmt.Sprintf("%s-R%03d", l.Warehouses[w].Code, n),
			Name:      fmt.Sprintf("%s %03d · %s", kind, n, l.Warehouses[w].City),
			Warehouse: w, Frozen: frozen,
		})
	}
	for i := 0; i < vehicles; i++ {
		capKG, capPal := 8000, 18
		if i%3 == 0 {
			capKG, capPal = 14000, 33
		}
		l.Vehicles = append(l.Vehicles, Vehicle{Plate: fmt.Sprintf("34 SZ %04d", i+1), CapacityKG: capKG, CapacityPallets: capPal})
	}
	idx := 1
	for _, r := range l.Rooms {
		for k := 0; k < SensorsPerRoom; k++ {
			l.Sensors = append(l.Sensors, Sensor{Code: fmt.Sprintf("S-%05d", idx), Location: r.Code, Frozen: r.Frozen, Index: idx})
			idx++
		}
	}
	for i := range l.Vehicles {
		l.Sensors = append(l.Sensors, Sensor{Code: fmt.Sprintf("S-%05d", idx), Location: VehicleLocation(i), Vehicle: true, Index: idx})
		idx++
	}
	return l
}

// VehicleLocation is the location code of the i-th vehicle (0-based).
func VehicleLocation(i int) string { return fmt.Sprintf("VEH-%04d", i+1) }

// BaseTempCC is the undisturbed temperature profile of a probe: a set point
// plus a slow sinusoidal drift (compressor cycles, day/night load). The seed
// SQL uses the same formula so live data continues the backfilled history.
func BaseTempCC(sensorIndex int, frozen bool, t time.Time) int {
	set := 350.0
	if frozen {
		set = -2100
	}
	hours := float64(t.Unix()) / 3600
	return int(math.Round(set + 100*math.Sin(hours*0.5+float64(sensorIndex))))
}

// BaseTempSQL is BaseTempCC in SQL, for a sensor id column `sid`, a frozen
// flag column `frozen` and a timestamp column `ts`.
const BaseTempSQL = `round(CASE WHEN frozen THEN -2100 ELSE 350 END
	+ 100 * sin(extract(epoch FROM ts) / 3600.0 * 0.5 + sid))`
