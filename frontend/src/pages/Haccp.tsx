import { FormEvent, useState } from 'react'
import { can, fmtTime, get, post } from '../api'
import { useAction, useAsync, usePaged } from '../hooks'
import { AsyncView, Card, ErrorBox, Field, MoreButton, Notice, PageHead } from '../components/ui'

type Item = { id: number; position: number; ccp_code: string; question: string; kind: 'boolean' | 'temperature'; min_cc: number | null; max_cc: number | null }
type Checklist = { id: number; code: string; name: string; frequency: string; items: Item[] }
type Rec = { id: number; checklist: string; location: string; performed_by: string; performed_at: string; all_ok: boolean; answers: { ccp_code: string; question: string; ok: boolean; corrective_action: string | null }[] }

const freq: Record<string, string> = { per_shift: 'Her vardiya', daily: 'Günlük', per_shipment: 'Her sevkiyat' }

export default function Haccp() {
  const lists = useAsync((s) => get<{ items: Checklist[] }>('/haccp/checklists', s), [])
  const records = usePaged<Rec>('/haccp/records?limit=20')
  const [active, setActive] = useState<Checklist>()
  return (
    <>
      <PageHead eyebrow="Kalite ve uyum" title="HACCP kontrol listeleri" sub="Kritik kontrol noktaları; başarısız madde düzeltici faaliyet olmadan kaydedilemez" />
      <div className="grid-2">
        <div className="stack">
          <Card title="Listeler">
            <AsyncView state={lists} isEmpty={(d) => d.items.length === 0} empty="Kontrol listesi tanımlı değil.">
              {(d) => (
                <ul className="list">
                  {d.items.map((c) => (
                    <li key={c.id} className="row between">
                      <span><strong>{c.name}</strong><div className="muted small">{c.code} · {freq[c.frequency]} · {c.items.length} madde</div></span>
                      {can('haccp:record') && <button className="btn ghost small" onClick={() => setActive(c)}>Doldur</button>}
                    </li>
                  ))}
                </ul>
              )}
            </AsyncView>
          </Card>
          {active && <Fill checklist={active} onDone={() => { setActive(undefined); records.state.reload() }} />}
        </div>
        <Card title="Son kayıtlar">
          <AsyncView state={records.state} isEmpty={(d) => d.length === 0} empty="Henüz kayıt yok.">
            {(items) => (
              <ul className="list">
                {items.map((r) => (
                  <li key={r.id}>
                    <div className="row between"><strong>{r.checklist}</strong><span className={'badge ' + (r.all_ok ? 'ok' : 'bad')}>{r.all_ok ? 'Uygun' : 'Uygunsuz'}</span></div>
                    <div className="muted small">{r.location} · {r.performed_by} · {fmtTime(r.performed_at)}</div>
                    {r.answers?.filter((a) => !a.ok).map((a) => <div key={a.ccp_code} className="small text-bad">{a.ccp_code}: {a.question} → {a.corrective_action}</div>)}
                  </li>
                ))}
              </ul>
            )}
          </AsyncView>
          <MoreButton cursor={records.cursor} onMore={records.more} loading={records.moreLoading} />
        </Card>
      </div>
    </>
  )
}

function Fill({ checklist, onDone }: { checklist: Checklist; onDone: () => void }) {
  const rooms = useAsync((s) => get<{ items: { id: number; name: string }[] }>('/locations', s), [])
  const [location, setLocation] = useState('')
  const [answers, setAnswers] = useState<Record<number, { bool_value?: boolean; temp?: string; corrective_action?: string }>>({})
  const act = useAction((body: unknown) => post<{ id: number; all_ok: boolean }>('/haccp/records', body))
  const set = (id: number, v: object) => setAnswers((a) => ({ ...a, [id]: { ...a[id], ...v } }))
  const failing = (it: Item) => {
    const a = answers[it.id]
    if (!a) return false
    if (it.kind === 'boolean') return a.bool_value === false
    const t = Math.round(Number(a.temp) * 100)
    return a.temp !== undefined && a.temp !== '' && (t < (it.min_cc ?? -1e9) || t > (it.max_cc ?? 1e9))
  }
  async function submit(e: FormEvent) {
    e.preventDefault()
    const res = await act.run({
      checklist_id: checklist.id,
      location_id: Number(location),
      answers: checklist.items.map((it) => {
        const a = answers[it.id] ?? {}
        return {
          item_id: it.id,
          ...(it.kind === 'boolean' ? { bool_value: a.bool_value } : { temp_cc: a.temp === undefined || a.temp === '' ? undefined : Math.round(Number(a.temp) * 100) }),
          ...(a.corrective_action ? { corrective_action: a.corrective_action } : {}),
        }
      }),
    })
    if (res) onDone()
  }
  return (
    <Card title={checklist.name}>
      <form onSubmit={submit} className="stack">
        <Field label="Konum">
          <select required value={location} onChange={(e) => setLocation(e.target.value)}>
            <option value="">Seçin</option>
            {rooms.data?.items.filter((r) => r.name).slice(0, 300).map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
          </select>
        </Field>
        {checklist.items.map((it) => (
          <div key={it.id} className={'ccp ' + (failing(it) ? 'failing' : '')}>
            <div><code>{it.ccp_code}</code> {it.question}</div>
            {it.kind === 'boolean' ? (
              <div className="seg">
                <button type="button" className={answers[it.id]?.bool_value === true ? 'on' : ''} onClick={() => set(it.id, { bool_value: true })}>Evet</button>
                <button type="button" className={answers[it.id]?.bool_value === false ? 'on' : ''} onClick={() => set(it.id, { bool_value: false })}>Hayır</button>
              </div>
            ) : (
              <div className="row">
                <input type="number" step="0.1" placeholder="°C" value={answers[it.id]?.temp ?? ''} onChange={(e) => set(it.id, { temp: e.target.value })} style={{ width: 100 }} />
                <span className="muted small">limit {(it.min_cc ?? 0) / 100} – {(it.max_cc ?? 0) / 100} °C</span>
              </div>
            )}
            {failing(it) && <input placeholder="Düzeltici faaliyet (zorunlu)" value={answers[it.id]?.corrective_action ?? ''} onChange={(e) => set(it.id, { corrective_action: e.target.value })} />}
          </div>
        ))}
        {act.error && <ErrorBox error={act.error} />}
        {act.pending && <Notice tone="info">Kaydediliyor…</Notice>}
        <button className="btn primary" disabled={act.pending}>Kaydet</button>
      </form>
    </Card>
  )
}
