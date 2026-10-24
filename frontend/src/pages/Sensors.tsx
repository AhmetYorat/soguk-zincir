import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { fmtC, fmtTime, get } from '../api'
import { useAsync, usePaged } from '../hooks'
import { AsyncView, Card, Kpi, MoreButton, PageHead, Status } from '../components/ui'
import { Point, TempChart } from '../components/Chart'

type Sensor = {
  id: number; code: string; location_id: number; location_name: string; location_kind: string
  last_seen_at: string | null; battery_pct: number | null; rejected_count: number; last_temp_cc: number | null
}

export default function Sensors() {
  const [kind, setKind] = useState('')
  const [q, setQ] = useState('')
  const [query, setQuery] = useState('')
  const list = usePaged<Sensor>(`/sensors?limit=50&kind=${kind}&q=${encodeURIComponent(query)}`)
  return (
    <>
      <PageHead eyebrow="İzleme" title="Sensörler" sub="2 000 sensör, 30 saniyede bir imzalı ölçüm" />
      <div className="toolbar">
        <div className="seg">
          {[['', 'Tümü'], ['cold_room', 'Soğuk oda'], ['vehicle', 'Araç']].map(([v, l]) => (
            <button key={v} className={kind === v ? 'on' : ''} onClick={() => setKind(v)}>{l}</button>
          ))}
        </div>
        <form className="row" onSubmit={(e) => { e.preventDefault(); setQuery(q) }}>
          <input placeholder="Sensör kodu veya konum" value={q} onChange={(e) => setQ(e.target.value)} />
          <button className="btn ghost">Ara</button>
        </form>
      </div>
      <Card>
        <AsyncView state={list.state} isEmpty={(d) => d.length === 0} empty="Sensör bulunamadı.">
          {(items) => (
            <div className="table-wrap">
              <table>
                <thead><tr><th>Kod</th><th>Konum</th><th className="num">Son sıcaklık</th><th>Son görülme</th><th className="num">Pil</th><th className="num">Reddedilen</th></tr></thead>
                <tbody>
                  {items.map((s) => (
                    <tr key={s.id}>
                      <td><Link to={`/sensors/${s.id}`}>{s.code}</Link></td>
                      <td><Status value={s.location_kind} /> {s.location_name}</td>
                      <td className="num">{fmtC(s.last_temp_cc)}</td>
                      <td>{fmtTime(s.last_seen_at)}</td>
                      <td className={'num ' + ((s.battery_pct ?? 100) < 20 ? 'text-bad' : '')}>{s.battery_pct ?? '—'}%</td>
                      <td className="num">{s.rejected_count}</td>
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

const ranges: [string, number][] = [['1 sa', 1], ['6 sa', 6], ['24 sa', 24], ['7 gün', 168]]

export function SensorDetail() {
  const { id } = useParams()
  const [hours, setHours] = useState(24)
  const sensor = useAsync((s) => get<Sensor>(`/sensors/${id}`, s), [id], 30000)
  const series = useAsync(
    (s) => {
      const from = new Date(Date.now() - hours * 3600e3).toISOString()
      return get<{ resolution: string; points: Point[] }>(`/sensors/${id}/readings?from=${encodeURIComponent(from)}`, s)
    },
    [id, hours],
    30000,
  )
  const frozen = sensor.data?.location_name.startsWith('Donuk')
  const limits: [number, number] = frozen ? [-25, -18] : [2, 8]
  return (
    <>
      <AsyncView state={sensor}>
        {(s) => (
          <>
            <PageHead title={`Sensör ${s.code}`} sub={s.location_name} actions={<Status value={s.location_kind} />} />
            <div className="kpis">
              <Kpi label="Son sıcaklık" value={fmtC(s.last_temp_cc)} />
              <Kpi label="Son görülme" value={s.last_seen_at ? new Date(s.last_seen_at).toLocaleTimeString('tr-TR') : '—'} />
              <Kpi label="Pil" value={`${s.battery_pct ?? '—'}%`} tone={(s.battery_pct ?? 100) < 20 ? 'bad' : undefined} />
              <Kpi label="Reddedilen mesaj" value={s.rejected_count} hint="geçersiz imza / replay" />
            </div>
          </>
        )}
      </AsyncView>
      <Card
        title="Sıcaklık"
        actions={<div className="seg">{ranges.map(([l, h]) => <button key={h} className={hours === h ? 'on' : ''} onClick={() => setHours(h)}>{l}</button>)}</div>}
      >
        <AsyncView state={series}>
          {(d) => (
            <>
              <TempChart points={d.points} limits={limits} />
              <p className="muted small">Çözünürlük: {d.resolution === 'raw' ? 'ham (30 sn)' : d.resolution} · aralığa göre otomatik seçilir (ham → 1 dk → 15 dk → 1 sa özet tabloları)</p>
            </>
          )}
        </AsyncView>
      </Card>
    </>
  )
}
