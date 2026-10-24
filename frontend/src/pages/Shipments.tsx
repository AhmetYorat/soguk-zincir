import { FormEvent, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { can, fmtDuration, fmtTime, get, post } from '../api'
import { useAction, useAsync, usePaged } from '../hooks'
import { AsyncView, Card, ErrorBox, Field, MoreButton, Notice, PageHead, Status } from '../components/ui'

type Shipment = {
  id: number; code: string; status: string; vehicle_id: number; plate: string; customer_id: number; customer: string
  origin_id: number; origin: string; planned_departure: string; transit_s: number; departed_at: string | null; delivered_at: string | null; pallets: number
}

export default function Shipments() {
  const [status, setStatus] = useState('')
  const list = usePaged<Shipment>(`/shipments?limit=50&status=${status}`)
  const [showNew, setShowNew] = useState(false)
  return (
    <>
      <PageHead eyebrow="Operasyon" title="Sevkiyatlar" sub="FEFO planı, kapasite kısıtlı yükleme, yola çıkış" actions={can('shipments:plan') && <button className="btn primary" onClick={() => setShowNew(!showNew)}>Yeni sevkiyat</button>} />
      {showNew && <NewShipment />}
      <div className="toolbar">
        <div className="seg wrap">
          {[['', 'Tümü'], ['planned', 'Planlandı'], ['loading', 'Yükleniyor'], ['in_transit', 'Yolda'], ['delivered', 'Teslim']].map(([v, l]) => (
            <button key={v} className={status === v ? 'on' : ''} onClick={() => setStatus(v)}>{l}</button>
          ))}
        </div>
      </div>
      <Card>
        <AsyncView state={list.state} isEmpty={(d) => d.length === 0} empty="Sevkiyat yok.">
          {(items) => (
            <div className="table-wrap">
              <table>
                <thead><tr><th>Kod</th><th>Müşteri</th><th>Araç</th><th>Çıkış</th><th>Planlanan</th><th className="num">Palet</th><th>Durum</th></tr></thead>
                <tbody>
                  {items.map((s) => (
                    <tr key={s.id}>
                      <td><Link to={`/shipments/${s.id}`}>{s.code}</Link></td>
                      <td>{s.customer}</td>
                      <td>{s.plate}</td>
                      <td className="small">{s.origin}</td>
                      <td>{fmtTime(s.planned_departure)}<div className="muted small">yol {fmtDuration(s.transit_s)}</div></td>
                      <td className="num">{s.pallets}</td>
                      <td><Status value={s.status} /></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </AsyncView>
        <MoreButton cursor={list.cursor} onMore={list.more} loading={list.moreLoading} />
      </Card>
    </>
  )
}

function NewShipment() {
  const navigate = useNavigate()
  const vehicles = useAsync((s) => get<{ items: { id: number; plate: string; capacity_kg: number; capacity_pallets: number; driver: string | null }[] }>('/vehicles', s), [])
  const customers = useAsync((s) => get<{ items: { customer_id: number; name: string }[] }>('/locations?kind=customer', s), [])
  const rooms = useAsync((s) => get<{ items: { id: number; name: string; active_units: number }[] }>('/locations?kind=cold_room', s), [])
  const [f, setF] = useState({ vehicle_id: '', customer_id: '', origin_id: '', transit_minutes: 180 })
  const act = useAction((b: unknown) => post<Shipment>('/shipments', b))
  async function submit(e: FormEvent) {
    e.preventDefault()
    const r = await act.run({ vehicle_id: Number(f.vehicle_id), customer_id: Number(f.customer_id), origin_id: Number(f.origin_id), transit_minutes: Number(f.transit_minutes), planned_departure: new Date(Date.now() + 3600e3).toISOString() })
    if (r) navigate(`/shipments/${r.id}`)
  }
  return (
    <Card title="Yeni sevkiyat">
      <form className="form-grid" onSubmit={submit}>
        <Field label="Araç">
          <select required value={f.vehicle_id} onChange={(e) => setF({ ...f, vehicle_id: e.target.value })}>
            <option value="">Seçin</option>
            {vehicles.data?.items.slice(0, 200).map((v) => <option key={v.id} value={v.id}>{v.plate} · {v.capacity_pallets} palet / {v.capacity_kg} kg{v.driver ? ` · ${v.driver}` : ''}</option>)}
          </select>
        </Field>
        <Field label="Müşteri">
          <select required value={f.customer_id} onChange={(e) => setF({ ...f, customer_id: e.target.value })}>
            <option value="">Seçin</option>
            {customers.data?.items.map((c) => <option key={c.customer_id} value={c.customer_id}>{c.name}</option>)}
          </select>
        </Field>
        <Field label="Çıkış deposu">
          <select required value={f.origin_id} onChange={(e) => setF({ ...f, origin_id: e.target.value })}>
            <option value="">Seçin</option>
            {rooms.data?.items.filter((r) => r.active_units > 0).map((r) => <option key={r.id} value={r.id}>{r.name} ({r.active_units} palet)</option>)}
          </select>
        </Field>
        <Field label="Yol süresi (dk)"><input type="number" min={1} value={f.transit_minutes} onChange={(e) => setF({ ...f, transit_minutes: Number(e.target.value) })} /></Field>
        <div className="form-actions"><button className="btn primary" disabled={act.pending}>Oluştur</button></div>
      </form>
      {act.error && <ErrorBox error={act.error} />}
    </Card>
  )
}

type Candidate = { unit_id: number; unit_code: string; lot_code: string; quantity: number; expires_at: string }
type LinePlan = { product_id: number; selected: Candidate[]; rejected: { candidate: Candidate; reason: string }[]; quantity: number; weight_kg: number; pallets: number; max_quantity: number; shortfall: number }

export function ShipmentDetail() {
  const { id } = useParams()
  const sh = useAsync((s) => get<{ shipment: Shipment; units: { id: number; code: string; contents: { lot_code: string; product: string; quantity: number }[] }[] }>(`/shipments/${id}`, s), [id])
  const products = useAsync((s) => get<{ items: { id: number; name: string }[] }>('/products', s), [])
  const [lines, setLines] = useState([{ product_id: '', quantity: 500 }])
  const [margin, setMargin] = useState(24)
  const plan = useAction(() => post<{ lines: LinePlan[]; capacity_left: { kg: number; pallets: number } }>(`/shipments/${id}/plan`, { lines: lines.filter((l) => l.product_id).map((l) => ({ product_id: Number(l.product_id), quantity: l.quantity })), safety_margin_hours: margin }, false))
  const [result, setResult] = useState<{ lines: LinePlan[]; capacity_left: { kg: number; pallets: number } }>()
  const load = useAction((ids: number[]) => post(`/shipments/${id}/load`, { unit_ids: ids, safety_margin_hours: margin }))
  const depart = useAction(() => post(`/shipments/${id}/depart`))
  const editable = sh.data && ['planned', 'loading'].includes(sh.data.shipment.status) && can('shipments:plan')

  return (
    <>
      <AsyncView state={sh}>
        {(d) => (
          <PageHead
            title={`Sevkiyat ${d.shipment.code}`}
            sub={`${d.shipment.customer} · ${d.shipment.plate} · ${d.shipment.origin} · yol ${fmtDuration(d.shipment.transit_s)}`}
            actions={<><Status value={d.shipment.status} />{editable && d.shipment.status === 'loading' && <button className="btn primary" disabled={depart.pending} onClick={async () => { if (await depart.run()) sh.reload() }}>Yola çıkar</button>}</>}
          />
        )}
      </AsyncView>
      {depart.error && <ErrorBox error={depart.error} />}
      <div className="grid-2">
        {editable && (
          <Card title="FEFO yükleme planı">
            <p className="muted small">İlk son kullanma tarihi önce çıkar. Kalan raf ömrü (yol süresi + güvenlik payı)'ndan kısa paletler elenir; araç kapasitesi kısıtıyla en dolu yükleme (sırt çantası) seçilir.</p>
            {lines.map((l, i) => (
              <div key={i} className="row wrap">
                <select value={l.product_id} onChange={(e) => setLines(lines.map((x, j) => (j === i ? { ...x, product_id: e.target.value } : x)))} aria-label="Ürün">
                  <option value="">Ürün seçin</option>
                  {products.data?.items.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
                </select>
                <input type="number" min={1} value={l.quantity} onChange={(e) => setLines(lines.map((x, j) => (j === i ? { ...x, quantity: Number(e.target.value) } : x)))} style={{ width: 100 }} aria-label="Adet" />
              </div>
            ))}
            <div className="row wrap">
              <button className="btn ghost small" onClick={() => setLines([...lines, { product_id: '', quantity: 200 }])}>+ Satır</button>
              <label className="row small">Güvenlik payı <input type="number" min={0} value={margin} onChange={(e) => setMargin(Number(e.target.value))} style={{ width: 70 }} /> sa</label>
              <button className="btn primary" disabled={plan.pending} onClick={async () => setResult(await plan.run())}>Plan öner</button>
            </div>
            {plan.error && <ErrorBox error={plan.error} />}
            {result && (
              <>
                {result.lines.map((ln) => (
                  <div key={ln.product_id} className="plan">
                    <h3>{products.data?.items.find((p) => p.id === ln.product_id)?.name}</h3>
                    <div className="small">{ln.quantity} adet · {ln.pallets} palet · {ln.weight_kg.toFixed(0)} kg {ln.shortfall > 0 && <span className="text-bad">· {ln.shortfall} adet eksik</span>}</div>
                    {ln.selected.length === 0 && <div className="muted small">Uygun palet yok.</div>}
                    <ul className="plain">
                      {ln.selected.map((c) => <li key={c.unit_id} className="small">✓ {c.unit_code} · {c.lot_code} · {c.quantity} adet · SKT {fmtTime(c.expires_at)}</li>)}
                      {ln.rejected.map((r) => <li key={r.candidate.unit_id} className="small text-bad">✗ {r.candidate.unit_code} · {r.candidate.lot_code} — {r.reason === 'remaining shelf life shorter than transit time + safety margin' ? 'kalan raf ömrü yol + güvenlik payından kısa' : r.reason}</li>)}
                    </ul>
                  </div>
                ))}
                <button className="btn primary" disabled={load.pending || result.lines.every((l) => l.selected.length === 0)} onClick={async () => {
                  const ids = result.lines.flatMap((l) => l.selected.map((c) => c.unit_id))
                  if (await load.run(ids)) { setResult(undefined); sh.reload() }
                }}>Önerilen paletleri yükle</button>
                {load.error && <ErrorBox error={load.error} />}
              </>
            )}
          </Card>
        )}
        <Card title="Araçtaki paletler">
          <AsyncView state={sh} isEmpty={(d) => d.units.length === 0} empty="Henüz palet yüklenmedi.">
            {(d) => (
              <ul className="list">
                {d.units.map((u) => (
                  <li key={u.id}>
                    <strong>{u.code}</strong>
                    {u.contents.map((c) => <div key={c.lot_code} className="small">{c.lot_code} · {c.product} · {c.quantity}</div>)}
                  </li>
                ))}
              </ul>
            )}
          </AsyncView>
          {sh.data?.shipment.status === 'delivered' && <Notice tone="ok">Teslim edildi: {fmtTime(sh.data.shipment.delivered_at)}</Notice>}
        </Card>
      </div>
    </>
  )
}
