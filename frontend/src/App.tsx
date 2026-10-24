import { useEffect, useState } from 'react'
import { BrowserRouter, Navigate, NavLink, Route, Routes, useLocation } from 'react-router-dom'
import { can, currentUser, get, logout, onAuthChange, roleLabel, User } from './api'
import { useAsync } from './hooks'
import Login from './pages/Login'
import Dashboard from './pages/Dashboard'
import Lots, { LotDetail } from './pages/Lots'
import Units from './pages/Units'
import Sensors, { SensorDetail } from './pages/Sensors'
import Alarms from './pages/Alarms'
import Haccp from './pages/Haccp'
import Shipments, { ShipmentDetail } from './pages/Shipments'
import Trace from './pages/Trace'
import Reports from './pages/Reports'
import Audit from './pages/Audit'
import Driver from './pages/Driver'
import Customer, { CustomerShipment } from './pages/Customer'
import Products from './pages/Products'

type NavItem = { to: string; label: string; perm: string }

// The menu only shows what the role may do; the API enforces the same
// permissions server side, the menu is a convenience.
const nav: NavItem[] = [
  { to: '/', label: 'Canlı pano', perm: 'live:read' },
  { to: '/lots', label: 'Partiler', perm: 'catalog:read' },
  { to: '/units', label: 'Paletler', perm: 'catalog:read' },
  { to: '/products', label: 'Ürünler', perm: 'catalog:read' },
  { to: '/sensors', label: 'Sensörler', perm: 'sensors:read' },
  { to: '/alarms', label: 'Alarmlar', perm: 'alarms:read' },
  { to: '/haccp', label: 'HACCP', perm: 'haccp:read' },
  { to: '/shipments', label: 'Sevkiyatlar', perm: 'shipments:read' },
  { to: '/trace', label: 'İzlenebilirlik', perm: 'trace:read' },
  { to: '/reports', label: 'Sapma raporları', perm: 'reports:read' },
  { to: '/audit', label: 'Denetim kaydı', perm: 'audit:read' },
  { to: '/driver', label: 'Teslimatlarım', perm: 'driver:app' },
  { to: '/customer', label: 'Sevkiyatlarım', perm: 'customer:portal' },
]

function home(u: User): string {
  if (u.role === 'driver') return '/driver'
  if (u.role === 'customer') return '/customer'
  return '/'
}

function LiveDot() {
  const live = useAsync((s) => get<{ ingest: { readings_per_second: number } }>('/live', s), [], 10000)
  const rate = live.data?.ingest.readings_per_second ?? 0
  return (
    <span className={'live ' + (rate > 0 ? 'on' : 'off')} title="Sensörlerden gelen ölçüm hızı">
      <i /> {rate > 0 ? `${rate.toFixed(0)} ölçüm/sn` : 'veri akışı yok'}
    </span>
  )
}

function Shell({ user }: { user: User }) {
  const [open, setOpen] = useState(false)
  const loc = useLocation()
  useEffect(() => setOpen(false), [loc.pathname])
  const items = nav.filter((n) => can(n.perm))
  const guard = (perm: string, el: JSX.Element) => (can(perm) ? el : <Navigate to={home(user)} replace />)

  return (
    <div className="shell">
      <header className="topbar">
        <button className="menu-btn" aria-label="Menü" aria-expanded={open} onClick={() => setOpen(!open)}>
          ☰
        </button>
        <div className="brand">
          <span className="logo" aria-hidden>
            ❄
          </span>
          SoğukZincir
        </div>
        {can('live:read') && <LiveDot />}
        <div className="user">
          <span className="user-name">{user.full_name}</span>
          <span className="badge info">{roleLabel[user.role]}</span>
          <button className="btn ghost small" onClick={() => logout()}>
            Çıkış
          </button>
        </div>
      </header>
      <nav className={'sidebar ' + (open ? 'open' : '')}>
        {items.map((n) => (
          <NavLink key={n.to} to={n.to} end={n.to === '/'}>
            {n.label}
          </NavLink>
        ))}
      </nav>
      <main className="content">
        <Routes>
          <Route path="/" element={guard('live:read', <Dashboard />)} />
          <Route path="/lots" element={guard('catalog:read', <Lots />)} />
          <Route path="/lots/:id" element={guard('catalog:read', <LotDetail />)} />
          <Route path="/units" element={guard('catalog:read', <Units />)} />
          <Route path="/products" element={guard('catalog:read', <Products />)} />
          <Route path="/sensors" element={guard('sensors:read', <Sensors />)} />
          <Route path="/sensors/:id" element={guard('sensors:read', <SensorDetail />)} />
          <Route path="/alarms" element={guard('alarms:read', <Alarms />)} />
          <Route path="/haccp" element={guard('haccp:read', <Haccp />)} />
          <Route path="/shipments" element={guard('shipments:read', <Shipments />)} />
          <Route path="/shipments/:id" element={guard('shipments:read', <ShipmentDetail />)} />
          <Route path="/trace" element={guard('trace:read', <Trace />)} />
          <Route path="/reports" element={guard('reports:read', <Reports />)} />
          <Route path="/audit" element={guard('audit:read', <Audit />)} />
          <Route path="/driver" element={guard('driver:app', <Driver />)} />
          <Route path="/customer" element={guard('customer:portal', <Customer />)} />
          <Route path="/customer/:id" element={guard('customer:portal', <CustomerShipment />)} />
          <Route path="*" element={<Navigate to={home(user)} replace />} />
        </Routes>
      </main>
    </div>
  )
}

export default function App() {
  const [user, setUser] = useState<User | null>(currentUser())
  useEffect(() => onAuthChange(setUser), [])
  return (
    <BrowserRouter>
      {user ? (
        <Shell key={user.id} user={user} />
      ) : (
        <Routes>
          <Route path="*" element={<Login />} />
        </Routes>
      )}
    </BrowserRouter>
  )
}
