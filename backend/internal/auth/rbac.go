package auth

// Role mirrors the user_role enum.
type Role string

const (
	RoleWarehouseOperator Role = "warehouse_operator"
	RoleDriver            Role = "driver"
	RoleQualityManager    Role = "quality_manager"
	RoleOperationsManager Role = "operations_manager"
	RoleCustomer          Role = "customer"
	RoleAuditor           Role = "auditor"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	_, ok := grants[r]
	return ok
}

// Permission is checked on every protected endpoint.
type Permission string

const (
	PermCatalogRead    Permission = "catalog:read"  // products, lots, units, locations
	PermCatalogWrite   Permission = "catalog:write" // create products and lots
	PermUnitsOperate   Permission = "units:operate" // split, merge, move pallets
	PermSensorsRead    Permission = "sensors:read"  // sensors and readings
	PermAlarmsRead     Permission = "alarms:read"
	PermAlarmsHandle   Permission = "alarms:handle" // acknowledge, close, nonconformities
	PermQualityDecide  Permission = "quality:decide"
	PermReportsRead    Permission = "reports:read"  // excursion reports, chain verification
	PermReportsWrite   Permission = "reports:write" // create excursion reports
	PermHACCPRecord    Permission = "haccp:record"
	PermHACCPRead      Permission = "haccp:read"
	PermShipmentsRead  Permission = "shipments:read"
	PermShipmentsPlan  Permission = "shipments:plan" // create, plan, load, depart
	PermDriverApp      Permission = "driver:app"     // own vehicle's shipments + offline sync
	PermTrace          Permission = "trace:read"
	PermRecall         Permission = "recall:create"
	PermCustomerPortal Permission = "customer:portal" // own deliveries only
	PermAuditRead      Permission = "audit:read"
	PermLiveRead       Permission = "live:read" // dashboard counters
)

var grants = map[Role][]Permission{
	RoleWarehouseOperator: {
		PermCatalogRead, PermCatalogWrite, PermUnitsOperate, PermSensorsRead, PermAlarmsRead,
		PermHACCPRecord, PermHACCPRead, PermShipmentsRead, PermLiveRead,
	},
	RoleDriver: {
		PermDriverApp,
	},
	RoleQualityManager: {
		PermCatalogRead, PermSensorsRead, PermAlarmsRead, PermAlarmsHandle, PermQualityDecide,
		PermReportsRead, PermReportsWrite, PermHACCPRead, PermHACCPRecord, PermTrace, PermRecall,
		PermShipmentsRead, PermLiveRead,
	},
	RoleOperationsManager: {
		PermCatalogRead, PermCatalogWrite, PermUnitsOperate, PermSensorsRead, PermAlarmsRead, PermAlarmsHandle,
		PermReportsRead, PermHACCPRead, PermShipmentsRead, PermShipmentsPlan, PermTrace, PermRecall, PermLiveRead,
	},
	RoleCustomer: {
		PermCustomerPortal,
	},
	RoleAuditor: {
		PermCatalogRead, PermSensorsRead, PermAlarmsRead, PermReportsRead, PermHACCPRead,
		PermShipmentsRead, PermTrace, PermAuditRead, PermLiveRead,
	},
}

var index = func() map[Role]map[Permission]bool {
	m := make(map[Role]map[Permission]bool, len(grants))
	for r, ps := range grants {
		m[r] = make(map[Permission]bool, len(ps))
		for _, p := range ps {
			m[r][p] = true
		}
	}
	return m
}()

// Can reports whether the role holds the permission.
func Can(r Role, p Permission) bool { return index[r][p] }

// Permissions lists a role's permissions (for the UI).
func Permissions(r Role) []Permission { return grants[r] }

// Roles lists all roles.
func Roles() []Role {
	return []Role{RoleWarehouseOperator, RoleDriver, RoleQualityManager, RoleOperationsManager, RoleCustomer, RoleAuditor}
}
