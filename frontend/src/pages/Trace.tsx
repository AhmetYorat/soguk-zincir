import { FormEvent, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { fmtTime, get, Page } from '../api'
import { useAsync } from '../hooks'
import { AsyncView, Card, PageHead, Status } from '../components/ui'

type Node = {
  unit_id: number; unit_code: string; unit_status: string; depth: number; quantity_now: number; location: string
  shipment_code: string | null; customer: string | null; vehicle_plate: string | null; delivered_at: string | null
}
type TraceLot = { lot_id: number; lot_code: string; units: Node[]; customers: { customer_id: number; name: string; shipments: string[]; units: number }[]; query_ms: number }
type Recall = { id: number; code: string; lot_code: string; reason: string; initiated_at: string; affected_units: number; affected_customers: number; query_ms: number }

export default function Trace() {
  const [params, setParams] = useSearchParams()
  const [code, setCode] = useState('LOT-TRACE-001')
  const lotParam = params.get('lot')
  const trace = useAsync<TraceLot | null>((s) => (lotParam ? get<TraceLot>(`/trace/lots/${lotParam}`, s) : Promise.resolve(null)), [lotParam])
  const recalls = useAsync((s) => get<Page<Recall>>('/recalls?limit=20', s), [params.get('recall')])

  async function find(e: FormEvent) {
    e.preventDefault()
    const res = await get<Page<{ id: number; code: string }>>(`/lots?q=${encodeURIComponent(code)}&limit=5`)
    const hit = res.items.find((l) => l.code === code) ?? res.items[0]
    if (hit) setParams({ lot: String(hit.id) })
  }

  return (
    <>
      <PageHead title="İzlenebilirlik ve geri çağırma" sub="Palet bölme/birleştirme sonrası partinin hangi müşteriye, hangi araçla gittiği (recursive sorgu)" />
      <form className="toolbar" onSubmit={find}>
        <input value={code} onChange={(e) => setCode(e.target.value)} placeholder="Parti kodu" />
        <button className="btn primary">İzle</button>
      </form>
      {lotParam && (
        <AsyncView state={trace}>
          {(t) =>
            t && (
              <div className="grid-2">
                <Card title={`${t.lot_code} ileri iz`} actions={<span className="muted small">sorgu {t.query_ms.toFixed(1)} ms</span>}>
                  <ul className="tree">
                    {t.units.sort((a, b) => a.depth - b.depth || a.unit_id - b.unit_id).map((n) => (
                      <li key={n.unit_id} style={{ paddingLeft: n.depth * 18 }}>
                        <span className="row wrap">
                          <b>{n.unit_code}</b> <Status value={n.unit_status} />
                          <span className="muted small">{n.location}{n.quantity_now ? ` · ${n.quantity_now} adet` : ''}</span>
                        </span>
                        {n.shipment_code && <div className="small">→ {n.customer} · {n.vehicle_plate} · {n.shipment_code} · {fmtTime(n.delivered_at)}</div>}
                      </li>
                    ))}
                  </ul>
                </Card>
                <Card title={`Etkilenen müşteriler (${t.customers.length})`}>
                  {t.customers.length === 0 ? (
                    <div className="state empty">Bu parti henüz bir müşteriye ulaşmadı.</div>
                  ) : (
                    <ul className="list">
                      {t.customers.map((c) => (
                        <li key={c.customer_id}><strong>{c.name}</strong><div className="small muted">{c.units} palet · {c.shipments.join(', ')}</div></li>
                      ))}
                    </ul>
                  )}
                  <p className="small"><Link to={`/lots/${t.lot_id}`}>Parti sayfası: sıcaklık geçmişi ve geri çağırma →</Link></p>
                </Card>
              </div>
            )
          }
        </AsyncView>
      )}
      <Card title="Geri çağırmalar">
        <AsyncView state={recalls} isEmpty={(d) => d.items.length === 0} empty="Geri çağırma yok.">
          {(d) => (
            <div className="table-wrap">
              <table>
                <thead><tr><th>Kod</th><th>Parti</th><th>Gerekçe</th><th className="num">Palet</th><th className="num">Müşteri</th><th className="num">Sorgu</th><th>Zaman</th></tr></thead>
                <tbody>
                  {d.items.map((r) => (
                    <tr key={r.id}><td>{r.code}</td><td>{r.lot_code}</td><td className="small">{r.reason}</td><td className="num">{r.affected_units}</td><td className="num">{r.affected_customers}</td><td className="num">{r.query_ms} ms</td><td>{fmtTime(r.initiated_at)}</td></tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </AsyncView>
      </Card>
    </>
  )
}
