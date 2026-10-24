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
import { Icon, IconName } from './components/Icon'
import { applyTheme, loadTheme, nextTheme, ThemePref } from './theme'

type NavItem = { to: string; label: string; perm: string; icon: IconName }
type NavGroup = { title: string; items: NavItem[] }

// The menu only shows what the role may do; the API enforces the same
// permissions server side, the menu is a convenience.
const nav: NavGroup[] = [
  {
    title: 'İzleme',
    items: [
      { to: '/', label: 'Canlı pano', perm: 'live:read', icon: 'gauge' },
      { to: '/sensors', label: 'Sensörler', perm: 'sensors:read', icon: 'thermo' },
      { to: '/alarms', label: 'Alarmlar', perm: 'alarms:read', icon: 'bell' },
    ],
  },
  {
    title: 'Stok',
    items: [
      { to: '/lots', label: 'Partiler', perm: 'catalog:read', icon: 'boxes' },
      { to: '/units', label: 'Paletler', perm: 'catalog:read', icon: 'pallet' },
      { to: '/products', label: 'Ürünler', perm: 'catalog:read', icon: 'tag' },
    ],
  },
  {
    title: 'Operasyon',
    items: [
      { to: '/shipments', label: 'Sevkiyatlar', perm: 'shipments:read', icon: 'truck' },
      { to: '/driver', label: 'Teslimatlarım', perm: 'driver:app', icon: 'phone' },
      { to: '/customer', label: 'Sevkiyatlarım', perm: 'customer:portal', icon: 'store' },
    ],
  },
  {
    title: 'Kalite ve uyum',
    items: [
      { to: '/haccp', label: 'HACCP', perm: 'haccp:read', icon: 'clipboard' },
      { to: '/trace', label: 'İzlenebilirlik', perm: 'trace:read', icon: 'route' },
      { to: '/reports', label: 'Sapma raporları', perm: 'reports:read', icon: 'link' },
      { to: '/audit', label: 'Denetim kaydı', perm: 'audit:read', icon: 'scroll' },
    ],
  },
]

function home(u: User): string {
  if (u.role === 'driver') return '/driver'
  if (u.role === 'customer') return '/customer'
  return '/'
}

function initials(name: string) {
  return name
    .split(' ')
    .map((p) => p[0])
    .slice(0, 2)
    .join('')
    .toLocaleUpperCase('tr-TR')
}

function LiveDot() {
  const live = useAsync((s) => get<{ ingest: { readings_per_second: number } }>('/live', s), [], 10000)
  const rate = live.data?.ingest.readings_per_second ?? 0
  return (
    <span className={'live ' + (rate > 0 ? 'on' : 'off')} title="Sensörlerden gelen ölçüm hızı" aria-live="polite">
      <i aria-hidden /> {rate > 0 ? `${rate.toFixed(0)} ölçüm/sn` : 'veri akışı yok'}
    </span>
  )
}

const themeLabel: Record<ThemePref, string> = { system: 'Tema: sistem', light: 'Tema: açık', dark: 'Tema: koyu' }
const themeIcon: Record<ThemePref, IconName> = { system: 'monitor', light: 'sun', dark: 'moon' }

function ThemeButton() {
  const [pref, setPref] = useState<ThemePref>(loadTheme())
  return (
    <button
      className="icon-btn"
      title={themeLabel[pref]}
      aria-label={themeLabel[pref] + ' (değiştir)'}
      onClick={() => {
        const next = nextTheme[pref]
        applyTheme(next)
        setPref(next)
      }}
    >
      <Icon name={themeIcon[pref]} />
    </button>
  )
}

function Shell({ user }: { user: User }) {
  const [open, setOpen] = useState(false)
  const loc = useLocation()
  useEffect(() => setOpen(false), [loc.pathname])
  const groups = nav.map((g) => ({ ...g, items: g.items.filter((n) => can(n.perm)) })).filter((g) => g.items.length)
  const guard = (perm: string, el: JSX.Element) => (can(perm) ? el : <Navigate to={home(user)} replace />)

  return (
    <div className="shell">
      <a className="skip-link" href="#main">İçeriğe geç</a>
      <header className="topbar">
        <button className="icon-btn menu-btn" aria-label={open ? 'Menüyü kapat' : 'Menüyü aç'} aria-expanded={open} onClick={() => setOpen(!open)}>
          <Icon name={open ? 'close' : 'menu'} size={20} />
        </button>
        <div className="brand">
          <span className="logo" aria-hidden>
            <Icon name="snow" size={20} />
          </span>
          <span>
            SoğukZincir
            <small>soğuk zincir izleme</small>
          </span>
        </div>
        {can('live:read') && <LiveDot />}
        <div className="user">
          <ThemeButton />
          <div className="user-chip">
            <span className="avatar" aria-hidden>{initials(user.full_name)}</span>
            <span className="user-meta">
              <span className="user-name">{user.full_name}</span>
              <span className="user-role">{roleLabel[user.role]}</span>
            </span>
          </div>
          <button className="icon-btn" onClick={() => logout()} title="Çıkış" aria-label="Çıkış yap">
            <Icon name="logout" />
          </button>
        </div>
      </header>
      {open && <div className="scrim" onClick={() => setOpen(false)} aria-hidden />}
      <nav className={'sidebar ' + (open ? 'open' : '')} aria-label="Ana menü">
        {groups.map((g) => (
          <div key={g.title} role="group" aria-label={g.title}>
            <div className="nav-group">{g.title}</div>
            {g.items.map((n) => (
              <NavLink key={n.to} to={n.to} end={n.to === '/'}>
                <Icon name={n.icon} /> {n.label}
              </NavLink>
            ))}
          </div>
        ))}
        <div className="sidebar-foot">
          <span className="mono">{roleLabel[user.role]}</span>
          <div>{user.email}</div>
        </div>
      </nav>
      <main className="content" id="main" tabIndex={-1}>
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
