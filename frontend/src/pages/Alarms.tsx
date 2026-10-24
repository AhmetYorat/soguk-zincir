import { useState } from 'react'
import { Link } from 'react-router-dom'
import { can, fmtC, fmtDuration, fmtTime, post } from '../api'
import { useAction, usePaged } from '../hooks'
import { AsyncView, Card, ErrorBox, MoreButton, PageHead, ruleName, Status } from '../components/ui'

type Alarm = {
  id: number; rule: string; status: string; escalation_level: number; sensor_id: number | null; sensor_code: string | null
  location_name: string | null; lot_id: number | null; lot_code: string | null; peak_cc: number | null; excursion_s: number
  message: string; opened_at: string; last_seen_at: string; acknowledged_at: string | null
}

export default function Alarms() {
  const [status, setStatus] = useState('live')
  const [rule, setRule] = useState('')
  const list = usePaged<Alarm>(`/alarms?limit=50&status=${status}&rule=${rule}`)
  const act = useAction((id: number, op: 'ack' | 'close') => post(`/alarms/${id}/${op}`, undefined, false))

  return (
    <>
      <PageHead eyebrow="İzleme" title="Alarmlar" sub="Aynı koşul için tek canlı alarm tutulur; onaylanmayan alarm 10 dakikada bir eskale edilir (1: kalite, 2: operasyon, 3: herkes)." />
      <div className="toolbar">
        <div className="seg">
          {[['live', 'Canlı'], ['open', 'Açık'], ['acknowledged', 'Onaylı'], ['closed', 'Kapalı'], ['', 'Tümü']].map(([v, l]) => (
            <button key={v} className={status === v ? 'on' : ''} onClick={() => setStatus(v)}>{l}</button>
          ))}
        </div>
        <select value={rule} onChange={(e) => setRule(e.target.value)} aria-label="Kural">
          <option value="">Tüm kurallar</option>
          {['limit_high', 'limit_low', 'window', 'cumulative', 'gap', 'shelf_life'].map((r) => <option key={r} value={r}>{ruleName(r)}</option>)}
        </select>
      </div>
      {act.error && <ErrorBox error={act.error} />}
      <Card>
        <AsyncView state={list.state} isEmpty={(d) => d.length === 0} empty="Bu filtrede alarm yok.">
          {(items) => (
            <div className="table-wrap">
              <table>
                <thead><tr><th>Kural</th><th>Kaynak</th><th>Mesaj</th><th className="num">Tepe</th><th className="num">Süre</th><th>Açılış</th><th>Durum</th>{can('alarms:handle') && <th />}</tr></thead>
                <tbody>
                  {items.map((a) => (
                    <tr key={a.id}>
                      <td><strong>{ruleName(a.rule)}</strong></td>
                      <td>
                        {a.lot_id ? <Link to={`/lots/${a.lot_id}`}>{a.lot_code}</Link> : <Link to={`/sensors/${a.sensor_id}`}>{a.sensor_code}</Link>}
                        <div className="muted small">{a.location_name}</div>
                      </td>
                      <td className="small">{a.message}</td>
                      <td className="num">{fmtC(a.peak_cc)}</td>
                      <td className="num">{fmtDuration(a.excursion_s)}</td>
                      <td>{fmtTime(a.opened_at)}<div className="muted small">son: {fmtTime(a.last_seen_at)}</div></td>
                      <td><Status value={a.status} />{a.escalation_level > 0 && a.status === 'open' && <span className="badge bad">Seviye {a.escalation_level}</span>}</td>
                      {can('alarms:handle') && (
                        <td className="nowrap">
                          {a.status === 'open' && <button className="btn ghost small" onClick={async () => { await act.run(a.id, 'ack'); list.state.reload() }}>Onayla</button>}
                          {a.status !== 'closed' && <button className="btn ghost small" onClick={async () => { await act.run(a.id, 'close'); list.state.reload() }}>Kapat</button>}
                        </td>
                      )}
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
