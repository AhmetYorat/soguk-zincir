import { useState } from 'react'
import { Link } from 'react-router-dom'
import { fmtC, fmtTime, get, Page } from '../api'
import { useAsync } from '../hooks'
import { AsyncView, Card, Kpi, PageHead, ruleName, Status } from '../components/ui'

type Live = {
  ingest: { readings_per_second: number; readings_last_minute: number; buffered: number }
  open_alarms: number
  escalated_alarms: number
  active_lots: number
  shipments_in_transit: number
  last_reading_at: string | null
}

type Sensor = { id: number; code: string; location_name: string; location_kind: string; last_temp_cc: number | null; last_seen_at: string | null; battery_pct: number | null }
type Alarm = { id: number; rule: string; status: string; message: string; opened_at: string; escalation_level: number; location_name: string | null; lot_code: string | null }

function tempTone(cc: number | null, frozen: boolean) {
  if (cc == null) return 'muted'
  if (frozen) return cc > -1800 ? 'bad' : 'ok'
  return cc > 800 || cc < 200 ? 'bad' : cc > 600 ? 'warn' : 'ok'
}

export default function Dashboard() {
  const live = useAsync((s) => get<Live>('/live', s), [], 5000)
  const [kind, setKind] = useState<'vehicle' | 'cold_room'>('vehicle')
  const board = useAsync((s) => get<Page<Sensor>>(`/sensors?kind=${kind}&limit=48`, s), [kind], 15000)
  const alarms = useAsync((s) => get<Page<Alarm>>('/alarms?status=live&limit=8', s), [], 15000)
  const health = useAsync((s) => get<{ total: number; online: number; stale: number; offline: number; low_battery: number; rejected_messages: number }>('/sensors/health', s), [], 30000)

  return (
    <>
      <PageHead title="Canlı pano" sub="Depo ve araç sıcaklıkları, açık alarmlar ve veri akışı (5 sn'de bir yenilenir)" />
      <AsyncView state={live}>
        {(d) => (
          <div className="kpis">
            <Kpi label="Ölçüm hızı" value={`${d.ingest.readings_per_second.toFixed(1)}/sn`} hint={`son 1 dk: ${d.ingest.readings_last_minute.toLocaleString('tr-TR')}`} tone={d.ingest.readings_per_second > 0 ? 'ok' : 'bad'} />
            <Kpi label="Açık alarm" value={d.open_alarms} hint={`${d.escalated_alarms} eskale`} tone={d.escalated_alarms > 0 ? 'bad' : d.open_alarms ? 'warn' : 'ok'} />
            <Kpi label="Aktif parti" value={d.active_lots} />
            <Kpi label="Yoldaki sevkiyat" value={d.shipments_in_transit} />
            <Kpi label="Son ölçüm" value={d.last_reading_at ? new Date(d.last_reading_at).toLocaleTimeString('tr-TR') : '—'} hint={`tampon: ${d.ingest.buffered}`} />
          </div>
        )}
      </AsyncView>

      <div className="grid-2">
        <Card
          title="Canlı sıcaklık panosu"
          actions={
            <div className="seg">
              <button className={kind === 'vehicle' ? 'on' : ''} onClick={() => setKind('vehicle')}>
                Araçlar
              </button>
              <button className={kind === 'cold_room' ? 'on' : ''} onClick={() => setKind('cold_room')}>
                Soğuk odalar
              </button>
            </div>
          }
        >
          <AsyncView state={board} isEmpty={(d) => d.items.length === 0} empty="Sensör yok.">
            {(d) => (
              <div className="tiles">
                {d.items.map((s) => {
                  const frozen = s.location_name.startsWith('Donuk')
                  return (
                    <Link key={s.id} to={`/sensors/${s.id}`} className={'tile ' + tempTone(s.last_temp_cc, frozen)} title={s.location_name}>
                      <span className="tile-temp">{fmtC(s.last_temp_cc)}</span>
                      <span className="tile-name">{s.location_name.replace('Araç ', '')}</span>
                      <span className="tile-code">{s.code}</span>
                    </Link>
                  )
                })}
              </div>
            )}
          </AsyncView>
        </Card>

        <div className="stack">
          <Card title="Son alarmlar" actions={<Link to="/alarms">Tümü →</Link>}>
            <AsyncView state={alarms} isEmpty={(d) => d.items.length === 0} empty="Açık alarm yok.">
              {(d) => (
                <ul className="list">
                  {d.items.map((a) => (
                    <li key={a.id}>
                      <div className="row between">
                        <strong>{ruleName(a.rule)}</strong>
                        <span className="row">
                          {a.escalation_level > 0 && <span className="badge bad">Seviye {a.escalation_level}</span>}
                          <Status value={a.status} />
                        </span>
                      </div>
                      <div className="small">{a.message}</div>
                      <div className="muted small">{fmtTime(a.opened_at)}</div>
                    </li>
                  ))}
                </ul>
              )}
            </AsyncView>
          </Card>
          <Card title="Sensör sağlığı">
            <AsyncView state={health}>
              {(h) => (
                <div className="kpis compact">
                  <Kpi label="Çevrimiçi" value={`${h.online}/${h.total}`} tone={h.offline ? 'warn' : 'ok'} />
                  <Kpi label="Gecikmeli" value={h.stale} />
                  <Kpi label="Çevrimdışı" value={h.offline} tone={h.offline ? 'bad' : undefined} />
                  <Kpi label="Zayıf pil" value={h.low_battery} tone={h.low_battery ? 'warn' : undefined} />
                  <Kpi label="Reddedilen mesaj" value={h.rejected_messages} hint="imzasız / sahte / replay" />
                </div>
              )}
            </AsyncView>
          </Card>
        </div>
      </div>
    </>
  )
}
