import { useState } from 'react'
import { fmtTime, get } from '../api'
import { useAction, usePaged } from '../hooks'
import { AsyncView, Card, ErrorBox, MoreButton, Notice, PageHead } from '../components/ui'

type Report = { seq: number; lot_code: string; created_at: string; prev_hash: string; hash: string }
type Verify = { valid: boolean; entries: number; head_seq: number; head_hash: string; problem?: { seq: number; reason: string } }

export default function Reports() {
  const list = usePaged<Report>('/excursion-reports?limit=50')
  const verify = useAction(() => get<Verify>('/excursion-reports/verify'))
  const [result, setResult] = useState<Verify>()
  return (
    <>
      <PageHead
        title="Sapma raporları"
        sub="Değiştirilemez kayıt: her rapor bir öncekinin hash'ini içerir (SHA-256 zinciri). Tablo UPDATE/DELETE'i tetikleyiciyle reddeder."
        actions={<button className="btn primary" disabled={verify.pending} onClick={async () => setResult(await verify.run())}>Zinciri doğrula</button>}
      />
      {result && (result.valid ? (
        <Notice tone="ok">Zincir geçerli: {result.entries} rapor, baş hash <code>{result.head_hash.slice(0, 16)}…</code></Notice>
      ) : (
        <Notice tone="bad">Zincir BOZUK: #{result.problem?.seq} — {result.problem?.reason}</Notice>
      ))}
      {verify.error && <ErrorBox error={verify.error} />}
      <Card>
        <AsyncView state={list.state} isEmpty={(d) => d.length === 0} empty="Henüz rapor yok. Parti sayfasından 'Sapma raporu oluştur' ile eklenir.">
          {(items) => (
            <div className="table-wrap">
              <table>
                <thead><tr><th className="num">#</th><th>Parti</th><th>Zaman</th><th>Önceki hash</th><th>Hash</th></tr></thead>
                <tbody>
                  {items.map((r) => (
                    <tr key={r.seq}><td className="num">{r.seq}</td><td>{r.lot_code}</td><td>{fmtTime(r.created_at)}</td><td><code>{r.prev_hash.slice(0, 12)}…</code></td><td><code>{r.hash.slice(0, 12)}…</code></td></tr>
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
