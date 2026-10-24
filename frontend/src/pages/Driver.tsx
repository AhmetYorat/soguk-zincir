import { useCallback, useEffect, useMemo, useState } from 'react'
import { api, ApiError, fmtDuration, fmtTime, get } from '../api'
import { useAsync, useOnline } from '../hooks'
import { AsyncView, Card, ErrorBox, Notice, PageHead, Status } from '../components/ui'
import { Icon } from '../components/Icon'
import { DriverEvent, OfflineQueue, safeLocalStorage, sync, SyncResult } from '../offline'

type Shipment = {
  id: number; code: string; status: string; plate: string; customer: string; origin: string; transit_s: number; departed_at: string | null
  units: { id: number; code: string; contents: { lot_code: string; product: string; quantity: number }[] }[]
}

const CACHE = 'sz.driver.shipments'
const kv = safeLocalStorage()

// The driver app keeps working without network: shipments are cached, taps
// become queued events, and the queue syncs when the phone is back online.
export default function Driver() {
  const queue = useMemo(() => new OfflineQueue(kv), [])
  const online = useOnline()
  const [forcedOffline, setForcedOffline] = useState(false)
  const connected = online && !forcedOffline
  const [pending, setPending] = useState<DriverEvent[]>(queue.list())
  const [last, setLast] = useState<SyncResult[]>()
  const [syncError, setSyncError] = useState<ApiError>()
  const [syncing, setSyncing] = useState(false)

  const shipments = useAsync(async (s) => {
    const cached = kv.getItem(CACHE)
    if (!connected && cached) return { items: JSON.parse(cached) as Shipment[], cached: true }
    try {
      const d = await get<{ items: Shipment[] }>('/driver/shipments', s)
      kv.setItem(CACHE, JSON.stringify(d.items))
      return { items: d.items, cached: false }
    } catch (e) {
      if (cached && (e as ApiError).code === 'network') return { items: JSON.parse(cached) as Shipment[], cached: true }
      throw e
    }
  }, [connected])

  const doSync = useCallback(async () => {
    if (!connected || syncing) return
    setSyncing(true)
    setSyncError(undefined)
    try {
      const r = await sync(queue, async (events) => (await api<{ results: SyncResult[] }>('POST', '/driver/sync', { events })).results)
      if (r.sent) {
        setLast(r.results)
        shipments.reload()
      }
    } catch (e) {
      setSyncError(e as ApiError)
    } finally {
      setPending(queue.list())
      setSyncing(false)
    }
  }, [connected, syncing, queue, shipments])

  // sync on reconnect and every 20 s while online
  useEffect(() => {
    if (!connected) return
    doSync()
    const t = setInterval(doSync, 20000)
    return () => clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connected])

  const record = (ev: Omit<DriverEvent, 'client_event_id' | 'recorded_at'>) => {
    queue.add(ev)
    setPending(queue.list())
    if (connected) doSync()
  }

  return (
    <>
      <PageHead eyebrow="Şoför" title="Teslimatlarım" sub="Bağlantı yokken de çalışır: kayıtlar telefonda tutulur, bağlantı gelince gönderilir." />
      <div className={'netbar ' + (connected ? 'on' : 'off')}>
        <span>
          <Icon name={connected ? 'wifi' : 'wifiOff'} />
          {connected ? 'Çevrimiçi' : 'Çevrimdışı — kayıtlar telefonda sıraya alınıyor'}
        </span>
        <label className="row small">
          <input type="checkbox" checked={forcedOffline} onChange={(e) => setForcedOffline(e.target.checked)} /> Çevrimdışı modu dene
        </label>
      </div>

      <Card title={`Gönderilmeyi bekleyen kayıtlar (${pending.length})`} actions={<button className="btn ghost small" disabled={!connected || syncing || pending.length === 0} onClick={doSync}>{syncing ? 'Gönderiliyor…' : 'Şimdi gönder'}</button>}>
        {pending.length === 0 ? (
          <div className="state empty">Sırada kayıt yok.</div>
        ) : (
          <ul className="list">
            {pending.map((e) => (
              <li key={e.client_event_id} className="small">
                {e.kind === 'delivery' ? 'Teslim' : `Sıcaklık ${((e.temp_cc ?? 0) / 100).toFixed(1)} °C`} · sevkiyat #{e.shipment_id} · {fmtTime(e.recorded_at)}
              </li>
            ))}
          </ul>
        )}
        {syncError && <ErrorBox error={syncError} />}
        {last && last.length > 0 && (
          <Notice tone={last.some((r) => r.outcome === 'rejected') ? 'warn' : 'ok'}>
            {last.map((r) => <div key={r.client_event_id} className="small">{({ applied: '✓ işlendi', already_synced: '✓ daha önce işlenmiş', duplicate_delivery: '! zaten teslim edilmiş', rejected: '✗ reddedildi' })[r.outcome]}{r.reason ? ` — ${r.reason}` : ''}</div>)}
          </Notice>
        )}
      </Card>

      <AsyncView state={shipments} isEmpty={(d) => d.items.length === 0} empty="Size atanmış sevkiyat yok.">
        {(d) => (
          <>
            {d.cached && <Notice tone="warn">Bağlantı yok: son bilinen liste gösteriliyor.</Notice>}
            {d.items.map((s) => <ShipmentCard key={s.id} s={s} queued={pending.filter((p) => p.shipment_id === s.id)} onRecord={record} />)}
          </>
        )}
      </AsyncView>
    </>
  )
}

function ShipmentCard({ s, queued, onRecord }: { s: Shipment; queued: DriverEvent[]; onRecord: (ev: Omit<DriverEvent, 'client_event_id' | 'recorded_at'>) => void }) {
  const [temp, setTemp] = useState('4.0')
  const [receiver, setReceiver] = useState('')
  const deliveryQueued = queued.some((q) => q.kind === 'delivery')
  return (
    <Card title={<>{s.customer} <Status value={s.status} /></>}>
      <div className="small muted">{s.code} · {s.plate} · {s.origin} · yol {fmtDuration(s.transit_s)} · çıkış {fmtTime(s.departed_at)}</div>
      <ul className="plain small">
        {s.units.map((u) => <li key={u.id}>{u.code}: {u.contents.map((c) => `${c.product} ×${c.quantity}`).join(', ')}</li>)}
      </ul>
      {s.status === 'in_transit' && (
        <div className="driver-actions">
          <div className="row">
            <input type="number" step="0.1" inputMode="decimal" value={temp} onChange={(e) => setTemp(e.target.value)} aria-label="Sıcaklık °C" />
            <button className="btn ghost" onClick={() => onRecord({ shipment_id: s.id, kind: 'temperature_check', temp_cc: Math.round(Number(temp) * 100) })}>Sıcaklık kaydet</button>
          </div>
          <div className="row">
            <input placeholder="Teslim alan" value={receiver} onChange={(e) => setReceiver(e.target.value)} />
            <button className="btn primary" disabled={deliveryQueued} onClick={() => onRecord({ shipment_id: s.id, kind: 'delivery', receiver_name: receiver || undefined })}>
              {deliveryQueued ? 'Teslim sırada' : 'Teslim et'}
            </button>
          </div>
        </div>
      )}
    </Card>
  )
}
