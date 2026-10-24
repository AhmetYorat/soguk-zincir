import { useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { can, fmtTime, get, post } from '../api'
import { useAction, useAsync, usePaged } from '../hooks'
import { AsyncView, Card, ErrorBox, MoreButton, Notice, PageHead, Status } from '../components/ui'

type Unit = {
  id: number
  code: string
  status: string
  location_id: number
  location_name: string
  location_kind: string
  created_at: string
  contents: { lot_id: number; lot_code: string; product: string; quantity: number; lot_status: string }[]
}

export default function Units() {
  const [params, setParams] = useSearchParams()
  const [q, setQ] = useState(params.get('q') ?? '')
  const query = params.get('q') ?? ''
  const status = params.get('status') ?? 'active'
  const list = usePaged<Unit>(`/units?limit=50&status=${status}&q=${encodeURIComponent(query)}`)
  const [selected, setSelected] = useState<number[]>([])
  const [msg, setMsg] = useState<string>()
  const merge = useAction(() => post<{ unit_id: number }>('/units/merge', { unit_ids: selected }))

  const toggle = (id: number) => setSelected((s) => (s.includes(id) ? s.filter((x) => x !== id) : [...s, id]))

  return (
    <>
      <PageHead eyebrow="Stok" title="Paletler" sub="Palet bölme, birleştirme ve taşıma. Her işlem soy ağacına (lineage) kaydedilir." />
      <div className="toolbar">
        <div className="seg">
          {[['active', 'Depoda'], ['loaded', 'Araçta'], ['delivered', 'Teslim'], ['consumed', 'Birleştirilmiş'], ['', 'Tümü']].map(([v, l]) => (
            <button key={v} className={status === v ? 'on' : ''} onClick={() => setParams({ status: v, q: query })}>{l}</button>
          ))}
        </div>
        <form className="row" onSubmit={(e) => { e.preventDefault(); setParams({ status, q }) }}>
          <input placeholder="Palet kodu" value={q} onChange={(e) => setQ(e.target.value)} />
          <button className="btn ghost">Ara</button>
        </form>
        {can('units:operate') && (
          <button className="btn primary" disabled={selected.length < 2 || merge.pending} onClick={async () => {
            const r = await merge.run()
            if (r) { setMsg(`Birleştirildi: yeni palet #${r.unit_id}`); setSelected([]); list.state.reload() }
          }}>
            Seçilenleri birleştir ({selected.length})
          </button>
        )}
      </div>
      {msg && <Notice tone="ok">{msg}</Notice>}
      {merge.error && <ErrorBox error={merge.error} />}
      <Card>
        <AsyncView state={list.state} isEmpty={(d) => d.length === 0} empty="Palet bulunamadı.">
          {(items) => (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>{can('units:operate') && <th />}<th>Palet</th><th>Konum</th><th>İçerik</th><th>Oluşturma</th><th>Durum</th>{can('units:operate') && <th>Bölme</th>}</tr>
                </thead>
                <tbody>
                  {items.map((u) => (
                    <tr key={u.id}>
                      {can('units:operate') && <td><input type="checkbox" aria-label={`${u.code} seç`} disabled={u.status !== 'active'} checked={selected.includes(u.id)} onChange={() => toggle(u.id)} /></td>}
                      <td>{u.code}</td>
                      <td>{u.location_name}</td>
                      <td>{u.contents.map((c) => <div key={c.lot_id} className="small">{c.lot_code} · {c.product} · <b>{c.quantity}</b> <Status value={c.lot_status} /></div>)}</td>
                      <td>{fmtTime(u.created_at)}</td>
                      <td><Status value={u.status} /></td>
                      {can('units:operate') && <td>{u.status === 'active' && u.contents.length === 1 && <Split unit={u} onDone={(m) => { setMsg(m); list.state.reload() }} />}</td>}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </AsyncView>
        <MoreButton cursor={list.cursor} onMore={list.more} loading={list.moreLoading} />
      </Card>
      {can('units:operate') && <MoveCard onDone={(m) => { setMsg(m); list.state.reload() }} selected={selected} />}
    </>
  )
}

function Split({ unit, onDone }: { unit: Unit; onDone: (msg: string) => void }) {
  const [qty, setQty] = useState(Math.min(20, Math.max(1, unit.contents[0].quantity - 1)))
  const act = useAction(() => post<{ child_unit_id: number }>(`/units/${unit.id}/split`, { lot_id: unit.contents[0].lot_id, quantity: qty }))
  return (
    <div className="row nowrap">
      <input type="number" min={1} value={qty} onChange={(e) => setQty(Number(e.target.value))} style={{ width: 70 }} aria-label="Bölünecek adet" />
      <button className="btn ghost small" disabled={act.pending} onClick={async () => { const r = await act.run(); if (r) onDone(`${unit.code} bölündü → palet #${r.child_unit_id}`) }}>Böl</button>
      {act.error && <span className="text-bad small" role="alert">{act.error.message}</span>}
    </div>
  )
}

function MoveCard({ selected, onDone }: { selected: number[]; onDone: (msg: string) => void }) {
  const rooms = useAsync((s) => get<{ items: { id: number; name: string }[] }>('/locations?kind=cold_room', s), [])
  const [room, setRoom] = useState('')
  const act = useAction(async () => {
    for (const id of selected) await post(`/units/${id}/move`, { location_id: Number(room) })
  })
  return (
    <Card title="Seçili paletleri başka odaya taşı">
      <div className="row wrap">
        <select value={room} onChange={(e) => setRoom(e.target.value)}>
          <option value="">Hedef soğuk oda</option>
          {rooms.data?.items.slice(0, 200).map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
        </select>
        <button className="btn ghost" disabled={!room || selected.length === 0 || act.pending} onClick={async () => { await act.run(); if (!act.error) onDone(`${selected.length} palet taşındı.`) }}>Taşı</button>
      </div>
      {act.error && <ErrorBox error={act.error} />}
    </Card>
  )
}
