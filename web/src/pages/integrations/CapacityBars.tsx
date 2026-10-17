import { Link } from 'react-router-dom'
import type { Bar, Segment } from './capacitySegments'

// One stacked bar: its segments in proportion, a mark where the total falls
// when the promise runs past it, and the same numbers in a table beneath,
// so it reads without colour or hover.

export function CapacityBar({ label, bar, format, sum }: { label: string; bar: Bar; format: (v: number) => string; sum: string }) {
  const pct = (v: number) => `${(v / bar.scale) * 100}%`
  const over = bar.mark < 1
  const summary = `${label}: ${sum}. ` + bar.segments.map((s) => `${s.name} ${format(s.value)}`).join(', ') +
    (bar.unlimited ? `, ${bar.unlimited} with no limit` : '') +
    (bar.unknown.length ? `, not read: ${bar.unknown.map((u) => u.name).join(', ')}` : '')
  return (
    <div className="cap-row">
      <div className="cap-head">
        <span className="cap-label">{label}</span>
        <span className="cap-sum">{sum}</span>
      </div>
      <div className="cap-line">
        <div className={`cap-bar${over ? ' cap-over' : ''}`} role="img" aria-label={summary}>
          {bar.segments.map((s) => <Seg key={s.key} s={s} width={pct(s.value)} share={s.value / bar.scale} format={format} />)}
          {over && <div className="cap-overflow" style={{ left: `${bar.mark * 100}%` }} />}
          {over && <div className="cap-mark" style={{ left: `${bar.mark * 100}%` }} title="what the host has" />}
        </div>
        {bar.unlimited > 0 && <span className="cap-nolimit" title="containers with no limit: each may take what the host has">{bar.unlimited} no limit</span>}
      </div>
      {bar.unknown.length > 0 && (
        <div className="cap-unknown">not read: {bar.unknown.map((u) => <span key={u.name} title={u.reason}>{u.name}</span>).reduce<React.ReactNode[]>((a, n, i) => (i ? [...a, ', ', n] : [n]), [])}</div>
      )}
    </div>
  )
}

// labelled are the neutral segments that carry their words inside the bar
// when there is room: free and not read, in text ink, never a series colour.
const labelled = new Set(['free', 'unread'])

function Seg({ s, width, share, format }: { s: Segment; width: string; share: number; format: (v: number) => string }) {
  const title = `${s.name}: ${format(s.value)}${s.detail ? ` — ${s.detail}` : ''}`
  const cls = `cap-seg cap-${s.kind}`
  // Each segment takes its share and shrinks to make room for the gaps
  // between them, so the bar never runs past its track.
  const style = { flex: `0 1 ${width}` }
  if (s.project && s.deployment) {
    return <Link className={cls} style={style} title={title} aria-label={title} to={`/projects/${s.project}/settings/deployments`} />
  }
  const words = labelled.has(s.kind) && share >= 0.18 ? `${format(s.value)} ${s.name}` : null
  return <span className={cls} style={style} title={title} aria-label={title} tabIndex={0}>{words && <span className="cap-in">{words}</span>}</span>
}

// CapacityTable is every bar's numbers as rows: the holder, then a column
// per bar.
export function CapacityTable({ rows, heads }: { rows: { name: string; kind: string; cells: string[]; to?: string }[]; heads: string[] }) {
  return (
    <table className="small cap-table">
      <thead><tr><th>holder</th>{heads.map((h) => <th key={h}>{h}</th>)}</tr></thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.kind + r.name}>
            <td><span className={`cap-swatch cap-${r.kind}`} />{r.to ? <Link to={r.to}>{r.name}</Link> : r.name}</td>
            {r.cells.map((c, i) => <td key={i}>{c}</td>)}
          </tr>
        ))}
      </tbody>
    </table>
  )
}
