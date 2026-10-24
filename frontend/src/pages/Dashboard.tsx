import { useState } from 'react'
import { Link } from 'react-router-dom'
import { fmtC, fmtTime, get, Page } from '../api'
import { useAsync } from '../hooks'
import { AsyncView, Card, Kpi, PageHead, ruleName, Status } from '../components/ui'
import { Icon } from '../components/Icon'

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

// Board scale per storage class: the gauge spans a little beyond the limits
// so a value just outside them is still visible on the strip.
const scales = {
  chilled: { lo: -2, hi: 12, min: 2, max: 8 },
  frozen: { lo: -30, hi: -12, min: -25, max: -18 },
}

function reading(cc: number | null, frozen: boolean) {
  const s = frozen ? scales.frozen : scales.chilled
  if (cc == null) return { tone: 'muted', pos: 0, flag: 'veri yok' }
  const c = cc / 100
  const pos = Math.min(1, Math.max(0, (c - s.lo) / (s.hi - s.lo)))
  if (c > s.max || c < s.min) return { tone: 'bad', pos, flag: 'limit dışı' }
  if (c > s.max - 0.5 || c < s.min + 0.3) return { tone: 'warn', pos, flag: 'sınırda' }
  return { tone: 'ok', pos, flag: '' }
}

export default function Dashboard() {
  const live = useAsync((s) => get<Live>('/live', s), [], 5000)
  const [kind, setKind] = useState<'vehicle' | 'cold_room'>('vehicle')
  const board = useAsync((s) => get<Page<Sensor>>(`/sensors?kind=${kind}&limit=48`, s), [kind], 15000)
  const alarms = useAsync((s) => get<Page<Alarm>>('/alarms?status=live&limit=8', s), [], 15000)
  const health = useAsync((s) => get<{ total: number; online: number; stale: number; offline: number; low_battery: number; rejected_messages: number }>('/sensors/health', s), [], 30000)

  return (
    <>
      <PageHead eyebrow="Canlı" title="Soğuk zincir panosu" sub="Depo ve araç sıcaklıkları, açık alarmlar ve veri akışı. Sayaçlar 5 saniyede bir yenilenir." />
      <AsyncView state={live}>
        {(d) => (
          <div className="kpis">
            <Kpi icon="pulse" label="Ölçüm hızı" value={`${d.ingest.readings_per_second.toFixed(1)}/sn`} hint={`son 1 dk: ${d.ingest.readings_last_minute.toLocaleString('tr-TR')} okuma`} tone={d.ingest.readings_per_second > 0 ? 'ok' : 'bad'} />
            <Kpi icon="bell" label="Açık alarm" value={d.open_alarms} hint={`${d.escalated_alarms} eskale edildi`} tone={d.escalated_alarms > 0 ? 'bad' : d.open_alarms ? 'warn' : 'ok'} />
            <Kpi icon="boxes" label="Aktif parti" value={d.active_lots} hint="gözetimimizdeki" />
            <Kpi icon="truck" label="Yoldaki sevkiyat" value={d.shipments_in_transit} hint="şu an araçta" />
            <Kpi icon="thermo" label="Son ölçüm" value={d.last_reading_at ? new Date(d.last_reading_at).toLocaleTimeString('tr-TR') : '—'} hint={`yazma tamponu: ${d.ingest.buffered}`} />
          </div>
        )}
      </AsyncView>

      <div className="grid-2">
        <Card
          title={<span className="row"><Icon name="thermo" /> Canlı sıcaklık panosu</span>}
          actions={
            <div className="seg" role="tablist" aria-label="Lokasyon türü">
              <button role="tab" aria-selected={kind === 'vehicle'} className={kind === 'vehicle' ? 'on' : ''} onClick={() => setKind('vehicle')}>
                Araçlar
              </button>
              <button role="tab" aria-selected={kind === 'cold_room'} className={kind === 'cold_room' ? 'on' : ''} onClick={() => setKind('cold_room')}>
                Soğuk odalar
              </button>
            </div>
          }
        >
          <AsyncView state={board} isEmpty={(d) => d.items.length === 0} empty="Sensör yok.">
            {(d) => (
              <>
                <div className="tiles">
                  {d.items.map((s) => {
                    const frozen = s.location_name.startsWith('Donuk')
                    const r = reading(s.last_temp_cc, frozen)
                    return (
                      <Link
                        key={s.id}
                        to={`/sensors/${s.id}`}
                        className={'tile ' + r.tone}
                        aria-label={`${s.location_name}: ${fmtC(s.last_temp_cc)}${r.flag ? ', ' + r.flag : ''}`}
                      >
                        <span className="tile-temp">{fmtC(s.last_temp_cc)}</span>
                        <span className="tile-name">{s.location_name.replace('Araç ', '')}</span>
                        <span className="tile-code">
                          {r.flag ? <span className={r.tone === 'bad' ? 'tile-flag' : ''}>{r.flag}</span> : s.code}
                        </span>
                        <span className="gauge" aria-hidden>
                          <i style={{ bottom: `${r.pos * 100}%` }} />
                        </span>
                      </Link>
                    )
                  })}
                </div>
                <p className="muted small">
                  Dikey şerit ölçeği: aşağısı soğuk, yukarısı sıcak; çizgi anlık değer. Soğutmalı ürünler 2–8 °C, donuk ürünler −25…−18 °C.
                </p>
              </>
            )}
          </AsyncView>
        </Card>

        <div className="stack">
          <Card title={<span className="row"><Icon name="bell" /> Son alarmlar</span>} actions={<Link to="/alarms" className="row small">Tümü <Icon name="arrowRight" size={15} /></Link>}>
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
                      <div className="muted small mono">{fmtTime(a.opened_at)}</div>
                    </li>
                  ))}
                </ul>
              )}
            </AsyncView>
          </Card>
          <Card title={<span className="row"><Icon name="wifi" /> Sensör sağlığı</span>}>
            <AsyncView state={health}>
              {(h) => (
                <div className="kpis compact">
                  <Kpi label="Çevrimiçi" value={`${h.online}/${h.total}`} tone={h.offline ? 'warn' : 'ok'} />
                  <Kpi label="Gecikmeli" value={h.stale} />
                  <Kpi label="Çevrimdışı" value={h.offline} tone={h.offline ? 'bad' : undefined} />
                  <Kpi icon="battery" label="Zayıf pil" value={h.low_battery} tone={h.low_battery ? 'warn' : undefined} />
                  <Kpi icon="shield" label="Reddedilen" value={h.rejected_messages} hint="imzasız / sahte / replay" />
                </div>
              )}
            </AsyncView>
          </Card>
        </div>
      </div>
    </>
  )
}
