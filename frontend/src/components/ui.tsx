import { ReactNode } from 'react'
import { ApiError } from '../api'
import { Async } from '../hooks'

// AsyncView renders the three states every screen must handle: loading,
// error (with retry) and empty; children only get real data.
export function AsyncView<T>({
  state,
  empty,
  isEmpty,
  children,
}: {
  state: Async<T>
  empty?: string
  isEmpty?: (d: T) => boolean
  children: (data: T) => ReactNode
}) {
  if (state.loading && state.data === undefined) {
    return (
      <div className="state" role="status" aria-live="polite">
        <span className="spinner" aria-hidden /> Yükleniyor…
      </div>
    )
  }
  if (state.error && state.data === undefined) {
    return <ErrorBox error={state.error} onRetry={state.reload} />
  }
  if (state.data === undefined) return null
  if (isEmpty?.(state.data)) {
    return <div className="state empty">{empty ?? 'Kayıt yok.'}</div>
  }
  return <>{children(state.data)}</>
}

export function ErrorBox({ error, onRetry }: { error: ApiError; onRetry?: () => void }) {
  const fields = (error.details?.fields ?? {}) as Record<string, string>
  return (
    <div className="state error" role="alert">
      <strong>{error.status === 403 ? 'Bu işlem için yetkiniz yok.' : error.message}</strong>
      {Object.keys(fields).length > 0 && (
        <ul>
          {Object.entries(fields).map(([k, v]) => (
            <li key={k}>
              <code>{k}</code>: {v}
            </li>
          ))}
        </ul>
      )}
      <span className="muted small">
        {error.code}
        {error.status ? ` · HTTP ${error.status}` : ''}
      </span>
      {onRetry && (
        <button className="btn ghost" onClick={onRetry}>
          Tekrar dene
        </button>
      )}
    </div>
  )
}

const statusTone: Record<string, string> = {
  released: 'ok', accepted: 'ok', delivered: 'ok', active: 'ok', closed: 'muted', applied: 'ok', compliant: 'ok',
  on_hold: 'warn', discounted: 'warn', acknowledged: 'warn', loading: 'info', planned: 'info', loaded: 'info',
  in_transit: 'info', open: 'bad', destroyed: 'bad', recalled: 'bad', deviation: 'bad', consumed: 'muted', cancelled: 'muted',
}

const statusLabel: Record<string, string> = {
  released: 'Serbest', on_hold: 'Beklemede', accepted: 'Kabul', discounted: 'İndirimli', destroyed: 'İmha', recalled: 'Geri çağrıldı',
  active: 'Depoda', loaded: 'Araçta', delivered: 'Teslim edildi', consumed: 'Birleştirildi',
  planned: 'Planlandı', loading: 'Yükleniyor', in_transit: 'Yolda', cancelled: 'İptal',
  open: 'Açık', acknowledged: 'Onaylandı', closed: 'Kapalı', compliant: 'Uygun', deviation: 'Sapma var',
  cold_room: 'Soğuk oda', vehicle: 'Araç', customer: 'Müşteri',
}

export function Status({ value }: { value: string }) {
  return <span className={'badge ' + (statusTone[value] ?? 'muted')}>{statusLabel[value] ?? value}</span>
}

const ruleLabel: Record<string, string> = {
  limit_high: 'Üst limit', limit_low: 'Alt limit', cumulative: 'Kümülatif sapma', window: 'Kayan pencere',
  gap: 'Sensör kesintisi', shelf_life: 'Raf ömrü bitti',
}
export const ruleName = (r: string) => ruleLabel[r] ?? r

export function Kpi({ label, value, hint, tone }: { label: string; value: ReactNode; hint?: string; tone?: string }) {
  return (
    <div className={'kpi ' + (tone ?? '')}>
      <div className="kpi-label">{label}</div>
      <div className="kpi-value">{value}</div>
      {hint && <div className="kpi-hint">{hint}</div>}
    </div>
  )
}

export function Card({ title, actions, children }: { title?: ReactNode; actions?: ReactNode; children: ReactNode }) {
  return (
    <section className="card">
      {(title || actions) && (
        <header className="card-head">
          {title && <h2>{title}</h2>}
          {actions && <div className="card-actions">{actions}</div>}
        </header>
      )}
      {children}
    </section>
  )
}

export function Field({ label, children, hint }: { label: string; children: ReactNode; hint?: string }) {
  return (
    <label className="field">
      <span>{label}</span>
      {children}
      {hint && <small className="muted">{hint}</small>}
    </label>
  )
}

export function PageHead({ title, sub, actions }: { title: string; sub?: string; actions?: ReactNode }) {
  return (
    <div className="page-head">
      <div>
        <h1>{title}</h1>
        {sub && <p className="muted">{sub}</p>}
      </div>
      {actions && <div className="row">{actions}</div>}
    </div>
  )
}

export function MoreButton({ cursor, onMore, loading }: { cursor: string | null | undefined; onMore: () => void; loading?: boolean }) {
  if (!cursor) return null
  return (
    <div className="more">
      <button className="btn ghost" onClick={onMore} disabled={loading}>
        {loading ? 'Yükleniyor…' : 'Daha fazla göster'}
      </button>
    </div>
  )
}

export function Notice({ tone, children }: { tone: 'ok' | 'warn' | 'bad' | 'info'; children: ReactNode }) {
  return <div className={'notice ' + tone}>{children}</div>
}
