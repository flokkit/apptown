import { PRODUCT } from '../../brand'
import { useGet } from '../../data'
import { StaleNote } from '../../ReadGate'
import { fmtBytes, relTime } from '../../format'
import type { DiskCapacity, Figure, TargetCapacity, Tenant } from '../../wire.gen'
import { disk, promised, usedMemory } from './capacitySegments'
import { CapacityBar, CapacityTable } from './CapacityBars'

type Resource = NonNullable<TargetCapacity['memory']>

// figureWords is a reading in words: its value as format writes it, or
// "unknown" with the reason on hover. Never 0 for a reading the forge
// could not make.
function FigureWords({ f, format }: { f: Figure; format: (v: number) => string }) {
  if (!f.known) return <span className="muted" title={f.reason}>unknown</span>
  return <>{format(f.value)}</>
}

const cpuWords = (v: number) => `${Math.round(v * 100) / 100} CPU${v === 1 ? '' : 's'}`
const usd = (v: number) => `$${v.toFixed(2)}`

// countWords: how many containers a figure covers, and how many carry no
// limit or one the forge could not read, so a sum is never mistaken for
// the whole.
function countWords(r: Resource): string {
  const p = r.promised
  const words = [`${p.limited.forge + p.limited.other} limited`]
  const unlimited = p.unlimited.forge + p.unlimited.other
  if (unlimited > 0) words.push(`${unlimited} with no limit`)
  const unread = p.unread.forge + p.unread.other
  if (unread > 0) words.push(`${unread} with a limit not read (the sum is partial)`)
  return words.join(', ')
}

function ResourceRow({ label, r, format }: { label: string; r: Resource; format: (v: number) => string }) {
  const p = r.promised.sum
  return (
    <tr>
      <td>{label}</td>
      <td><FigureWords f={r.total} format={format} /></td>
      <td>
        <FigureWords f={p.forge} format={format} /> <span className="muted">{PRODUCT}</span>{' + '}
        <FigureWords f={p.other} format={format} /> <span className="muted">other</span>
        <div className="muted">{countWords(r)}</div>
        {r.overcommitted && <span className="chip pending" title="the limits add up to more than the host has; limits are caps, so this is allowed">overcommitted</span>}
      </td>
      <td>
        {r.inUse ? <><FigureWords f={r.inUse.forge} format={format} /> <span className="muted">{PRODUCT}</span>{' + '}<FigureWords f={r.inUse.other} format={format} /> <span className="muted">other</span></> : <span className="muted">—</span>}
      </td>
    </tr>
  )
}

// TargetCapacity says how full a target is. A fixed host: what it has,
// what the running containers are promised (the limits Docker enforces),
// what they use, and its disk. An elastic one: what each deployment runs
// on and what it is estimated to cost. Read on its own, so a slow host
// never holds the target's page.
export function Capacity({ name }: { name: string }) {
  const q = useGet<TargetCapacity>(`/targets/${name}/capacity`)
  if (q.denied) return null
  if (q.loading) return <div className="muted small">reading its capacity…</div>
  if (!q.data) return <div className="error small">capacity unknown — {q.err || 'no answer'}</div>
  const c = q.data
  return (
    <div className="small">
      <div>{c.words} <span className="muted">· read {relTime(c.readAt)}</span></div>
      {c.tenants && c.tenants.length > 0 ? <Bars c={c} /> : c.memory && c.cpu && (
        <table className="small" style={{ marginTop: 4 }}>
          <thead><tr><th></th><th>has</th><th>{c.kind === 'portainer' ? `declared by ${PRODUCT}'s deployments` : 'promised to containers'}</th><th>in use by containers</th></tr></thead>
          <tbody>
            <ResourceRow label="memory" r={c.memory} format={fmtBytes} />
            <ResourceRow label="CPUs" r={c.cpu} format={cpuWords} />
          </tbody>
        </table>
      )}
      {!(c.tenants && c.tenants.length > 0) && c.disk && <DiskLine d={c.disk} />}
      {c.elastic && (
        <table className="small" style={{ marginTop: 4 }}>
          <thead><tr><th>deployment</th><th>part</th><th>runs on</th><th>listed per month</th></tr></thead>
          <tbody>
            {(c.parts ?? []).map((p) => (
              <tr key={`${p.deployment}/${p.part}`}>
                <td>{p.deployment}</td>
                <td>{p.part || 'app'}{p.need ? <span className="muted"> (service)</span> : null}</td>
                <td>{p.runsOn}</td>
                <td><FigureWords f={p.usdPerMonth} format={usd} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {c.estimate && (
        <div style={{ marginTop: 4 }}>
          <b>{usd(c.estimate.value)}</b> a month, estimated <span className="muted">— {c.estimate.reason}</span>
        </div>
      )}
      <StaleNote q={q} inline />
    </div>
  )
}

// diskLabel names the filesystem a disk reading is of: the Docker data
// folder on the host, by its path when the daemon said it.
const diskLabel = (d: DiskCapacity) => `disk (Docker's data${d.path ? `, ${d.path}` : ''})`

// DiskState is what a host's disk means for new work: refused below its
// floor (a release or preview reads it again when it starts), or low.
function DiskState({ d }: { d: DiskCapacity }) {
  if (d.refuses) return <span className="chip failed" title="a new release or preview on this host reads the disk again when it starts">new releases refused: under {fmtBytes(d.floor ?? 0)} free</span>
  if (d.low) return <span className="chip pending">less than a tenth free</span>
  return null
}

// DiskLine is a disk reading in a line: what it is of, free of total and
// when it was read, or why it could not be read.
function DiskLine({ d }: { d: DiskCapacity }) {
  const why = !d.free.known ? d.free.reason : !d.total.known ? d.total.reason : ''
  return (
    <div style={{ marginTop: 4 }}>
      <span className="muted">{diskLabel(d)}</span>{' '}
      {why ? <>unknown <span className="muted">— {why}</span></> : <><FigureWords f={d.free} format={fmtBytes} /> free of <FigureWords f={d.total} format={fmtBytes} /></>}
      {d.readAt && <span className="muted"> · read {relTime(d.readAt)}</span>}
      {' '}<DiskState d={d} />
    </div>
  )
}

const f = (v: Figure | null | undefined, format: (v: number) => string) => (v && v.known ? format(v.value) : 'unknown')

// limitWords: a holder's promised sum, or "no limit" when none of its
// containers has one: never "0", which would read as a promise of nothing.
const limitWords = (v: Figure, unlimited: number, containers: number, format: (v: number) => string) =>
  v.known && v.value === 0 && unlimited > 0 && unlimited === containers ? 'no limit' : f(v, format)

// cpuCell: a holder's CPU promise, partial when a limit was not read as a
// number of CPUs (CV-C02): "not read" or "at least …", never a plain sum.
function cpuCell(t: Tenant): string {
  if (t.cpuUnread === 0) return limitWords({ known: true, value: t.cpus }, t.cpuUnlimited, t.containers, cpuWords)
  const unread = `${t.cpuUnread} not read`
  return t.cpus > 0 ? `at least ${cpuWords(t.cpus)} (${unread})` : unread
}

// Bars: a Docker host's memory (used, promised), CPUs (promised) and disk,
// drawn by holder, with the numbers in a table beneath.
function Bars({ c }: { c: TargetCapacity }) {
  const m = c.memory!
  const used = usedMemory(c)
  const pm = promised(c, 'memory')
  const pc = promised(c, 'cpu')
  const dk = disk(c)
  const host = m.hostUsed
  const free = host?.known && m.total.known ? m.total.value - host.value : null
  const tenants = c.tenants ?? []
  return (
    <div className="cap" style={{ marginTop: 6 }}>
      {used ? (
        <CapacityBar label="memory used" bar={used} format={fmtBytes}
          sum={free !== null && free >= 0 ? `${fmtBytes(free)} free of ${fmtBytes(m.total.value)}` : `${f(m.total, fmtBytes)} · free not known`} />
      ) : <div className="muted">memory: <FigureWords f={m.total} format={fmtBytes} /></div>}
      {pm && <CapacityBar label="memory promised" bar={pm} format={fmtBytes}
        sum={`${fmtBytes(pm.sum)} of ${fmtBytes(m.total.value)}${m.overcommitted ? ` · over by ${fmtBytes(pm.sum - m.total.value)}` : ''}`} />}
      {pc && c.cpu && <CapacityBar label="CPUs promised" bar={pc} format={cpuWords}
        sum={pc.partial > 0
          ? `at least ${cpuWords(pc.sum)} of ${cpuWords(c.cpu.total.value)} · ${pc.partial} limit${pc.partial === 1 ? '' : 's'} not read`
          : `${cpuWords(pc.sum)} of ${cpuWords(c.cpu.total.value)}${c.cpu.overcommitted ? ` · over by ${cpuWords(pc.sum - c.cpu.total.value)}` : ''}`} />}
      {dk && c.disk ? (
        <>
          <CapacityBar label={diskLabel(c.disk)} bar={dk} format={fmtBytes} sum={`${fmtBytes(c.disk.free.value)} free of ${fmtBytes(c.disk.total.value)}${c.disk.readAt ? ` · read ${relTime(c.disk.readAt)}` : ''}`} />
          <DiskState d={c.disk} />
        </>
      ) : c.disk && <DiskLine d={c.disk} />}
      <details className="cap-details">
        <summary className="muted">the numbers</summary>
        <CapacityTable heads={['containers', 'memory used', 'memory promised', 'no limit', 'CPUs promised']}
          rows={[
            ...tenants.map((t) => ({
              name: t.name, kind: t.kind,
              to: t.project && t.deployment ? `/projects/${t.project}/settings/deployments` : undefined,
              cells: [String(t.containers), f(t.used, fmtBytes), limitWords(t.promised, t.unlimited, t.containers, fmtBytes), String(t.unlimited),
                cpuCell(t)],
            })),
            ...(host ? [{ name: 'the whole host', kind: 'host', cells: ['', f(host, fmtBytes), '', '', ''] }] : []),
            { name: 'what the host has', kind: 'free', cells: ['', f(m.total, fmtBytes), '', '', c.cpu ? f(c.cpu.total, cpuWords) : ''] },
          ]} />
        {host && !host.known && <div className="muted small">the whole host's use: unknown — {host.reason}</div>}
      </details>
    </div>
  )
}
