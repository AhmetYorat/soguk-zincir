import { get, fmtDuration, fmtKurus } from '../api'
import { useAsync } from '../hooks'
import { AsyncView, Card, PageHead } from '../components/ui'

type Product = {
  id: number; sku: string; name: string; temp_min_cc: number; temp_max_cc: number; max_cumulative_excursion_s: number
  window_s: number; window_max_excursion_s: number; shelf_life_h: number; gap_policy: string; unit_weight_g: number; list_price_kurus: number
}

export default function Products() {
  const list = useAsync((s) => get<{ items: Product[] }>('/products', s), [])
  return (
    <>
      <PageHead eyebrow="Stok" title="Ürünler" sub="Ürün bazlı soğuk zincir kuralları: sıcaklık aralığı, kümülatif sapma, kayan pencere, boşluk politikası" />
      <Card>
        <AsyncView state={list} isEmpty={(d) => d.items.length === 0} empty="Ürün tanımlı değil.">
          {(d) => (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr><th>SKU</th><th>Ürün</th><th>Aralık</th><th>Kümülatif limit</th><th>Pencere kuralı</th><th>Raf ömrü</th><th>Boşluk</th><th className="num">Fiyat</th></tr>
                </thead>
                <tbody>
                  {d.items.map((p) => (
                    <tr key={p.id}>
                      <td><code>{p.sku}</code></td>
                      <td>{p.name}</td>
                      <td>{p.temp_min_cc / 100} … {p.temp_max_cc / 100} °C</td>
                      <td>{fmtDuration(p.max_cumulative_excursion_s)}</td>
                      <td>{fmtDuration(p.window_max_excursion_s)} / {fmtDuration(p.window_s)}</td>
                      <td>{p.shelf_life_h >= 48 ? `${(p.shelf_life_h / 24).toFixed(0)} gün` : `${p.shelf_life_h} sa`}</td>
                      <td>{p.gap_policy === 'uncertain' ? 'Belirsiz say' : 'Doldur'}</td>
                      <td className="num">{fmtKurus(p.list_price_kurus)}</td>
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
