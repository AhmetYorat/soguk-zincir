import { Link, useParams } from 'react-router-dom'
import { fmtDuration, fmtTemp, fmtTime, get, Page } from '../api'
import { useAsync } from '../hooks'
import { AsyncView, Card, PageHead, Status } from '../components/ui'

type Shipment = { id: number; code: string; status: string; plate: string; origin: string; departed_at: string | null; delivered_at: string | null; pallets: number }

export default function Customer() {
  const list = useAsync((s) => get<Page<Shipment>>('/customer/shipments?limit=50', s), [])
  return (
    <>
      <PageHead eyebrow="Müşteri portalı" title="Sevkiyatlarım" sub="Size gelen ürünler ve yol boyunca sıcaklık geçmişi" />
      <Card>
        <AsyncView state={list} isEmpty={(d) => d.items.length === 0} empty="Henüz sevkiyat yok.">
          {(d) => (
            <div className="table-wrap">
              <table>
                <thead><tr><th>Sevkiyat</th><th>Araç</th><th>Çıkış</th><th>Teslim</th><th className="num">Palet</th><th>Durum</th></tr></thead>
                <tbody>
                  {d.items.map((s) => (
                    <tr key={s.id}>
                      <td><Link to={`/customer/${s.id}`}>{s.code}</Link></td>
                      <td>{s.plate}</td>
                      <td>{fmtTime(s.departed_at)}</td>
                      <td>{fmtTime(s.delivered_at)}</td>
                      <td className="num">{s.pallets}</td>
                      <td><Status value={s.status} /></td>
                    </tr>
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

type Cert = {
  unit_code: string; lot_code: string; product: string; quantity: number; limits_c: [number, number]; verdict: string
  cold_chain: { from: string; to: string; peak_c: number; min_c: number; mkt_c: number | null; cumulative_excursion_s: number; uncertain_s: number; breaches: { rule: string }[] }
}

// Printable temperature certificate; the browser's "save as PDF" produces the
// PDF document (bonus requirement), styles in @media print.
export function CustomerShipment() {
  const { id } = useParams()
  const cert = useAsync((s) => get<{ shipment: Shipment & { customer: string }; certificates: Cert[]; issued_at: string }>(`/customer/shipments/${id}`, s), [id])
  return (
    <AsyncView state={cert}>
      {(d) => (
        <div className="certificate">
          <PageHead
            title="Sevkiyat sıcaklık sertifikası"
            sub={`${d.shipment.code} · ${d.shipment.customer} · ${d.shipment.plate}`}
            actions={<button className="btn primary no-print" onClick={() => window.print()}>PDF olarak kaydet / yazdır</button>}
          />
          <Card>
            <p className="small">Çıkış: {fmtTime(d.shipment.departed_at)} · Teslim: {fmtTime(d.shipment.delivered_at)} · Düzenlenme: {fmtTime(d.issued_at)}</p>
            <div className="table-wrap">
              <table>
                <thead><tr><th>Palet</th><th>Parti</th><th>Ürün</th><th className="num">Adet</th><th>Limit</th><th className="num">Min / Maks</th><th className="num">MKT</th><th className="num">Sapma</th><th>Sonuç</th></tr></thead>
                <tbody>
                  {d.certificates.map((c) => (
                    <tr key={c.unit_code + c.lot_code}>
                      <td>{c.unit_code}</td><td>{c.lot_code}</td><td>{c.product}</td><td className="num">{c.quantity}</td>
                      <td>{c.limits_c[0]}…{c.limits_c[1]} °C</td>
                      <td className="num">{fmtTemp(c.cold_chain.min_c)} / {fmtTemp(c.cold_chain.peak_c)}</td>
                      <td className="num">{fmtTemp(c.cold_chain.mkt_c)}</td>
                      <td className="num">{fmtDuration(c.cold_chain.cumulative_excursion_s)}</td>
                      <td><Status value={c.verdict} /></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <p className="muted small">Değerler ürünün üretimden teslimata kadar bulunduğu tüm soğuk oda ve araçlardaki sensör ölçümlerinden hesaplanmıştır. MKT: ortalama kinetik sıcaklık (ΔH = 83,144 kJ/mol).</p>
          </Card>
        </div>
      )}
    </AsyncView>
  )
}
