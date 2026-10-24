import { useState } from 'react'
import { fmtTime } from '../api'
import { usePaged } from '../hooks'
import { AsyncView, Card, MoreButton, PageHead } from '../components/ui'

type Entry = { id: number; at: string; actor_id: number | null; actor_role: string | null; action: string; entity: string; entity_id: string; old_value: unknown; new_value: unknown; request_id: string }

export default function Audit() {
  const [entity, setEntity] = useState('')
  const list = usePaged<Entry>(`/audit?limit=50&entity=${entity}`)
  return (
    <>
      <PageHead eyebrow="Kalite ve uyum" title="Denetim kaydı" sub="Eklemeli (append-only): kim, ne zaman, neyi, eski ve yeni değer. Değiştirme ve silme veritabanında engellenir." />
      <div className="toolbar">
        <select value={entity} onChange={(e) => setEntity(e.target.value)} aria-label="Varlık">
          <option value="">Tüm varlıklar</option>
          {['lot', 'unit', 'shipment', 'alarm', 'product', 'haccp_record'].map((e) => <option key={e}>{e}</option>)}
        </select>
      </div>
      <Card>
        <AsyncView state={list.state} isEmpty={(d) => d.length === 0} empty="Kayıt yok.">
          {(items) => (
            <div className="table-wrap">
              <table>
                <thead><tr><th>Zaman</th><th>Kim</th><th>İşlem</th><th>Varlık</th><th>Eski</th><th>Yeni</th></tr></thead>
                <tbody>
                  {items.map((e) => (
                    <tr key={e.id}>
                      <td className="nowrap">{fmtTime(e.at)}</td>
                      <td>#{e.actor_id} <span className="muted small">{e.actor_role}</span></td>
                      <td><code>{e.action}</code></td>
                      <td>{e.entity} {e.entity_id}</td>
                      <td><pre className="json">{e.old_value ? JSON.stringify(e.old_value) : '—'}</pre></td>
                      <td><pre className="json">{e.new_value ? JSON.stringify(e.new_value) : '—'}</pre></td>
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
