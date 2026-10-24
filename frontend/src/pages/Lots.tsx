import { FormEvent, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { can, fmtDuration, fmtTemp, fmtTime, get, post } from '../api'
import { useAction, useAsync, usePaged } from '../hooks'
import { AsyncView, Card, ErrorBox, Field, Kpi, MoreButton, Notice, PageHead, Status } from '../components/ui'

export type LotRow = {
  id: number
  code: string
  product_name: string
  product_sku: string
  produced_at: string
  quantity: number
  on_hand: number
  status: string
  discount_pct: number
  remaining_shelf_life_s: number | null
}

const statuses = ['', 'released', 'on_hold', 'accepted', 'discounted', 'destroyed', 'recalled']
const statusTr: Record<string, string> = { '': 'Tümü', released: 'Serbest', on_hold: 'Beklemede', accepted: 'Kabul', discounted: 'İndirimli', destroyed: 'İmha', recalled: 'Geri çağrıldı' }

export default function Lots() {
  const [status, setStatus] = useState('')
  const [q, setQ] = useState('')
  const [query, setQuery] = useState('')
  const list = usePaged<LotRow>(`/lots?limit=50&status=${status}&q=${encodeURIComponent(query)}`)
  const [showNew, setShowNew] = useState(false)

  return (
    <>
      <PageHead
        title="Partiler"
        sub="Ürün partileri, stok ve kalan raf ömrü"
        actions={can('catalog:write') && <button className="btn primary" onClick={() => setShowNew(!showNew)}>Parti kabulü</button>}
      />
      {showNew && <NewLot onDone={() => { setShowNew(false); list.state.reload() }} />}
      <div className="toolbar">
        <div className="seg wrap">
          {statuses.map((s) => (
            <button key={s} className={status === s ? 'on' : ''} onClick={() => setStatus(s)}>
              {statusTr[s]}
            </button>
          ))}
        </div>
        <form onSubmit={(e) => { e.preventDefault(); setQuery(q) }} className="row">
          <input placeholder="Parti kodu veya ürün ara" value={q} onChange={(e) => setQ(e.target.value)} />
          <button className="btn ghost">Ara</button>
        </form>
      </div>
      <Card>
        <AsyncView state={list.state} isEmpty={(d) => d.length === 0} empty="Bu filtreyle parti bulunamadı.">
          {(items) => (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Parti</th><th>Ürün</th><th>Üretim</th><th className="num">Stok / Toplam</th><th className="num">Kalan raf ömrü</th><th>Durum</th>
                  </tr>
                </thead>
                <tbody>
                  {items.map((l) => (
                    <tr key={l.id}>
                      <td><Link to={`/lots/${l.id}`}>{l.code}</Link></td>
                      <td>{l.product_name}</td>
                      <td>{fmtTime(l.produced_at)}</td>
                      <td className="num">{l.on_hand} / {l.quantity}</td>
                      <td className="num">{l.remaining_shelf_life_s == null ? '—' : <span className={l.remaining_shelf_life_s < 86400 ? 'text-bad' : ''}>{(l.remaining_shelf_life_s / 3600).toFixed(1)} sa</span>}</td>
                      <td><Status value={l.status} />{l.discount_pct > 0 && <span className="muted small"> %{l.discount_pct}</span>}</td>
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

function NewLot({ onDone }: { onDone: () => void }) {
  const products = useAsync((s) => get<{ items: { id: number; name: string; sku: string }[] }>('/products', s), [])
  const rooms = useAsync((s) => get<{ items: { id: number; name: string; code: string }[] }>('/locations?kind=cold_room', s), [])
  const [form, setForm] = useState({ code: '', product_id: '', location_id: '', produced_at: new Date(Date.now() - 3600e3).toISOString().slice(0, 16), pallets: '200,200' })
  const act = useAction((body: unknown) => post('/lots', body))
  async function submit(e: FormEvent) {
    e.preventDefault()
    const res = await act.run({
      code: form.code,
      product_id: Number(form.product_id),
      location_id: Number(form.location_id),
      produced_at: new Date(form.produced_at).toISOString(),
      pallets: form.pallets.split(',').map((x) => Number(x.trim())).filter(Boolean),
    })
    if (res) onDone()
  }
  return (
    <Card title="Parti kabulü">
      <form className="form-grid" onSubmit={submit}>
        <Field label="Parti kodu"><input required value={form.code} onChange={(e) => setForm({ ...form, code: e.target.value })} /></Field>
        <Field label="Ürün">
          <select required value={form.product_id} onChange={(e) => setForm({ ...form, product_id: e.target.value })}>
            <option value="">Seçin</option>
            {products.data?.items.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
          </select>
        </Field>
        <Field label="Soğuk oda">
          <select required value={form.location_id} onChange={(e) => setForm({ ...form, location_id: e.target.value })}>
            <option value="">Seçin</option>
            {rooms.data?.items.slice(0, 200).map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
          </select>
        </Field>
        <Field label="Üretim zamanı"><input type="datetime-local" required value={form.produced_at} onChange={(e) => setForm({ ...form, produced_at: e.target.value })} /></Field>
        <Field label="Palet adetleri" hint="Virgülle ayırın, ör. 200,180"><input required value={form.pallets} onChange={(e) => setForm({ ...form, pallets: e.target.value })} /></Field>
        <div className="form-actions">
          <button className="btn primary" disabled={act.pending}>{act.pending ? 'Kaydediliyor…' : 'Kaydet'}</button>
        </div>
      </form>
      {act.error && <ErrorBox error={act.error} />}
    </Card>
  )
}

// ---- detail ----

type Breach = { rule: string; at: string }
type UnitEval = {
  unit_id: number
  unit_code: string
  unit_status: string
  quantity: number
  readings: number
  exposures: { location_name: string; location_kind: string; from: string; to: string; depth: number; unit_code: string }[]
  excursion: {
    high_s: number; low_s: number; uncertain_s: number; cumulative_excursion_s: number; cumulative_limit_s: number
    max_window_excursion_s: number; window_limit_s: number; peak_c: number; min_c: number; mkt_c: number | null
    gaps: { start: string; end: string }[]; breaches: Breach[]; gap_policy: string
  }
  shelf_life: { nominal_h: number; elapsed_h: number; penalty_h: number; remaining_h: number; expires_at: string; expired: boolean }
}
type Evaluation = { lot_code: string; status: string; product: { name: string; temp_min_cc: number; temp_max_cc: number; shelf_life_h: number }; units: UnitEval[]; worst: UnitEval | null }

const breachTr: Record<string, string> = { limit_high: 'Üst limit aşıldı', limit_low: 'Alt limit aşıldı', cumulative: 'Kümülatif sapma limiti aşıldı', window: 'Kayan pencere limiti aşıldı', gap: 'Ölçüm boşluğu' }

export function LotDetail() {
  const { id } = useParams()
  const lot = useAsync((s) => get<{ lot: LotRow; units: { id: number; code: string; status: string; location_name: string; contents: { lot_id: number; quantity: number }[] }[] }>(`/lots/${id}`, s), [id])
  const ev = useAsync((s) => get<Evaluation>(`/lots/${id}/evaluation`, s), [id])
  const decisions = useAsync((s) => get<{ items: { id: number; decision: string; discount_pct: number; reason: string; decided_by: string; decided_at: string }[] }>(`/lots/${id}/decisions`, s), [id])
  const reload = () => { lot.reload(); ev.reload(); decisions.reload() }

  return (
    <>
      <AsyncView state={lot}>
        {(d) => (
          <PageHead
            title={`Parti ${d.lot.code}`}
            sub={`${d.lot.product_name} · üretim ${fmtTime(d.lot.produced_at)} · stok ${d.lot.on_hand}/${d.lot.quantity}`}
            actions={<><Status value={d.lot.status} />{can('trace:read') && <Link className="btn ghost" to={`/trace?lot=${d.lot.id}`}>İzle</Link>}</>}
          />
        )}
      </AsyncView>
      <div className="grid-2">
        <Card title="Soğuk zincir değerlendirmesi">
          <AsyncView state={ev}>
            {(e) => (e.worst ? <EvalView e={e} /> : <div className="state empty">Bu parti artık gözetimimizde değil.</div>)}
          </AsyncView>
        </Card>
        <div className="stack">
          {(can('quality:decide') || can('alarms:handle') || can('reports:write') || can('recall:create')) && <Actions lotId={Number(id)} onDone={reload} />}
          <Card title="Kalite kararları">
            <AsyncView state={decisions} isEmpty={(d) => d.items.length === 0} empty="Henüz karar verilmedi.">
              {(d) => (
                <ul className="list">
                  {d.items.map((x) => (
                    <li key={x.id}>
                      <div className="row between"><strong>{({ accept: 'Kabul', discount: `İndirim %${x.discount_pct}`, destroy: 'İmha' } as Record<string, string>)[x.decision]}</strong><span className="muted small">{fmtTime(x.decided_at)}</span></div>
                      <div className="small">{x.reason}</div>
                      <div className="muted small">{x.decided_by}</div>
                    </li>
                  ))}
                </ul>
              )}
            </AsyncView>
          </Card>
          <Card title="Paletler">
            <AsyncView state={lot} isEmpty={(d) => d.units.length === 0} empty="Stokta palet yok.">
              {(d) => (
                <ul className="list">
                  {d.units.map((u) => (
                    <li key={u.id} className="row between">
                      <span><Link to={`/units?q=${encodeURIComponent(u.code)}`}>{u.code}</Link> · {u.location_name}</span>
                      <Status value={u.status} />
                    </li>
                  ))}
                </ul>
              )}
            </AsyncView>
          </Card>
        </div>
      </div>
    </>
  )
}

function EvalView({ e }: { e: Evaluation }) {
  const w = e.worst!
  const x = w.excursion
  return (
    <>
      <div className="kpis compact">
        <Kpi label="Kümülatif sapma" value={fmtDuration(x.cumulative_excursion_s)} hint={`limit ${fmtDuration(x.cumulative_limit_s)}`} tone={x.cumulative_excursion_s > x.cumulative_limit_s ? 'bad' : 'ok'} />
        <Kpi label="En kötü pencere" value={fmtDuration(x.max_window_excursion_s)} hint={`limit ${fmtDuration(x.window_limit_s)}`} tone={x.max_window_excursion_s > x.window_limit_s ? 'bad' : 'ok'} />
        <Kpi label="MKT" value={fmtTemp(x.mkt_c)} hint={`aralık ${e.product.temp_min_cc / 100}–${e.product.temp_max_cc / 100} °C`} />
        <Kpi label="Kalan raf ömrü" value={`${w.shelf_life.remaining_h.toFixed(1)} sa`} hint={`ceza ${w.shelf_life.penalty_h.toFixed(1)} sa · nominal ${w.shelf_life.nominal_h} sa`} tone={w.shelf_life.expired ? 'bad' : w.shelf_life.remaining_h < 24 ? 'warn' : 'ok'} />
      </div>
      {x.breaches.length > 0 ? (
        <Notice tone="bad">
          <ul className="plain">
            {x.breaches.map((b) => <li key={b.rule}>{breachTr[b.rule] ?? b.rule} — {fmtTime(b.at)}</li>)}
          </ul>
        </Notice>
      ) : (
        <Notice tone="ok">Kurallar ihlal edilmedi.</Notice>
      )}
      <p className="muted small">
        Değerlendirme {w.readings} ölçüm üzerinden, en kötü palet ({w.unit_code}) esas alınarak yapıldı. Boşluk politikası: <b>{x.gap_policy === 'uncertain' ? 'belirsiz say' : 'doğrusal doldur'}</b>
        {x.uncertain_s > 0 && <> · {fmtDuration(x.uncertain_s)} ölçülmemiş süre sapma sayıldı</>}. Tepe {fmtTemp(x.peak_c)}, en düşük {fmtTemp(x.min_c)}.
      </p>
      <h3>Konum geçmişi</h3>
      <div className="table-wrap">
        <table>
          <thead><tr><th>Palet</th><th>Konum</th><th>Başlangıç</th><th>Bitiş</th></tr></thead>
          <tbody>
            {w.exposures.map((p, i) => (
              <tr key={i}>
                <td>{p.unit_code}{p.depth > 0 && <span className="muted small"> (ata {p.depth})</span>}</td>
                <td><Status value={p.location_kind} /> {p.location_name}</td>
                <td>{fmtTime(p.from)}</td>
                <td>{fmtTime(p.to)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {e.units.length > 1 && <p className="muted small">Bu parti {e.units.length} palette bulunuyor; tablo en kötü durumdaki paleti gösteriyor.</p>}
    </>
  )
}

function Actions({ lotId, onDone }: { lotId: number; onDone: () => void }) {
  const [decision, setDecision] = useState('accept')
  const [discount, setDiscount] = useState(20)
  const [reason, setReason] = useState('')
  const [msg, setMsg] = useState<string>()
  const navigate = useNavigate()
  const decide = useAction(() =>
    post(`/lots/${lotId}/decisions`, { decision, reason, discount_pct: decision === 'discount' ? discount : 0 }),
  )
  const nc = useAction(() => post(`/nonconformities`, { lot_id: lotId, description: reason }, false))
  const report = useAction(() => post<{ seq: number; hash: string }>(`/lots/${lotId}/excursion-reports`))
  const recall = useAction(() => post<{ id: number }>(`/recalls`, { lot_id: lotId, reason }))

  const error = decide.error ?? nc.error ?? report.error ?? recall.error
  return (
    <Card title="İşlemler">
      <Field label="Gerekçe / açıklama">
        <textarea rows={2} value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Ör. 45 dk 9 °C üzerinde kaldı, duyusal kontrol uygun" />
      </Field>
      {can('quality:decide') && (
        <div className="row wrap">
          <select value={decision} onChange={(e) => setDecision(e.target.value)}>
            <option value="accept">Kabul</option>
            <option value="discount">İndirim</option>
            <option value="destroy">İmha</option>
          </select>
          {decision === 'discount' && (
            <input type="number" min={1} max={99} value={discount} onChange={(e) => setDiscount(Number(e.target.value))} style={{ width: 80 }} aria-label="İndirim yüzdesi" />
          )}
          <button className="btn primary" disabled={decide.pending} onClick={async () => { if (await decide.run()) { setMsg('Karar kaydedildi.'); onDone() } }}>
            Kalite kararı ver
          </button>
        </div>
      )}
      <div className="row wrap">
        {can('alarms:handle') && <button className="btn ghost" onClick={async () => { if (await nc.run()) { setMsg('Uygunsuzluk açıldı, parti beklemeye alındı.'); onDone() } }}>Uygunsuzluk aç</button>}
        {can('reports:write') && <button className="btn ghost" onClick={async () => { const r = await report.run(); if (r) setMsg(`Rapor #${r.seq} zincire eklendi (${r.hash.slice(0, 12)}…).`) }}>Sapma raporu oluştur</button>}
        {can('recall:create') && <button className="btn danger" onClick={async () => { if (!confirm('Bu parti geri çağrılsın mı?')) return; const r = await recall.run(); if (r) navigate(`/trace?recall=${r.id}`) }}>Geri çağır</button>}
      </div>
      {msg && !error && <Notice tone="ok">{msg}</Notice>}
      {error && <ErrorBox error={error} />}
    </Card>
  )
}
