import { useMemo, useState } from 'react'

export type Point = { ts: string; avg_c: number; min_c: number; max_c: number; humidity_pct: number; n: number }

// TempChart draws a temperature series: min–max band, average line and the
// product limits as dashed lines. Pure SVG, scales to its container.
export function TempChart({ points, limits, height = 220 }: { points: Point[]; limits?: [number, number]; height?: number }) {
  const W = 800
  const H = height
  const pad = { l: 44, r: 12, t: 12, b: 26 }
  const [hover, setHover] = useState<number | null>(null)

  const geo = useMemo(() => {
    if (points.length === 0) return null
    const xs = points.map((p) => new Date(p.ts).getTime())
    const lo = Math.min(...points.map((p) => p.min_c), ...(limits ? [limits[0]] : []))
    const hi = Math.max(...points.map((p) => p.max_c), ...(limits ? [limits[1]] : []))
    const span = Math.max(hi - lo, 1)
    const y0 = lo - span * 0.08
    const y1 = hi + span * 0.08
    const x0 = xs[0]
    const x1 = Math.max(xs[xs.length - 1], x0 + 1)
    const x = (t: number) => pad.l + ((t - x0) / (x1 - x0)) * (W - pad.l - pad.r)
    const y = (v: number) => pad.t + (1 - (v - y0) / (y1 - y0)) * (H - pad.t - pad.b)
    const line = points.map((p, i) => `${i ? 'L' : 'M'}${x(xs[i]).toFixed(1)},${y(p.avg_c).toFixed(1)}`).join('')
    const band =
      points.map((p, i) => `${i ? 'L' : 'M'}${x(xs[i]).toFixed(1)},${y(p.max_c).toFixed(1)}`).join('') +
      points
        .slice()
        .reverse()
        .map((p, i) => `L${x(xs[xs.length - 1 - i]).toFixed(1)},${y(p.min_c).toFixed(1)}`)
        .join('') +
      'Z'
    const ticks = Array.from({ length: 5 }, (_, i) => y0 + ((y1 - y0) * i) / 4)
    const tticks = Array.from({ length: 5 }, (_, i) => x0 + ((x1 - x0) * i) / 4)
    return { x, y, xs, line, band, ticks, tticks }
  }, [points, limits, H])

  if (!geo) return <div className="state empty">Bu aralıkta ölçüm yok.</div>
  const hp = hover != null ? points[hover] : null

  return (
    <div className="chart">
      <svg
        viewBox={`0 0 ${W} ${H}`}
        role="img"
        aria-label="Sıcaklık grafiği"
        onMouseLeave={() => setHover(null)}
        onMouseMove={(e) => {
          const rect = (e.currentTarget as SVGSVGElement).getBoundingClientRect()
          const px = ((e.clientX - rect.left) / rect.width) * W
          let best = 0
          for (let i = 0; i < geo.xs.length; i++) {
            if (Math.abs(geo.x(geo.xs[i]) - px) < Math.abs(geo.x(geo.xs[best]) - px)) best = i
          }
          setHover(best)
        }}
      >
        {geo.ticks.map((t) => (
          <g key={t}>
            <line x1={pad.l} x2={W - pad.r} y1={geo.y(t)} y2={geo.y(t)} className="grid" />
            <text x={pad.l - 6} y={geo.y(t) + 4} className="axis" textAnchor="end">
              {t.toFixed(1)}
            </text>
          </g>
        ))}
        {geo.tticks.map((t) => (
          <text key={t} x={geo.x(t)} y={H - 6} className="axis" textAnchor="middle">
            {new Date(t).toLocaleTimeString('tr-TR', { hour: '2-digit', minute: '2-digit' })}
          </text>
        ))}
        {limits &&
          limits.map((l) => (
            <line key={l} x1={pad.l} x2={W - pad.r} y1={geo.y(l)} y2={geo.y(l)} className="limit" />
          ))}
        <path d={geo.band} className="band" />
        <path d={geo.line} className="line" />
        {hp && hover != null && (
          <g>
            <line x1={geo.x(geo.xs[hover])} x2={geo.x(geo.xs[hover])} y1={pad.t} y2={H - pad.b} className="cursor" />
            <circle cx={geo.x(geo.xs[hover])} cy={geo.y(hp.avg_c)} r={4} className="dot" />
          </g>
        )}
      </svg>
      <div className="chart-legend">
        {hp ? (
          <span>
            {new Date(hp.ts).toLocaleString('tr-TR')} · ort. <b>{hp.avg_c.toFixed(2)} °C</b> (min {hp.min_c.toFixed(1)}, maks{' '}
            {hp.max_c.toFixed(1)}) · nem %{hp.humidity_pct.toFixed(0)}
          </span>
        ) : (
          <span className="muted">
            {points.length} nokta · bant: min–maks{limits ? ` · kesikli çizgi: ürün limitleri ${limits[0]}–${limits[1]} °C` : ''}
          </span>
        )}
      </div>
    </div>
  )
}
