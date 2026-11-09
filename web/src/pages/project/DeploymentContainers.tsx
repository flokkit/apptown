import { useState } from 'react'
import { get } from '../../api'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { Status } from '../../Status'
import { CapacityStrip } from '../../CapacityStrip'
import { ListTable, type ListColumn } from '../../ListTable'
import { MoreMenu } from '../../MoreMenu'
import { fmtBytes, fmtWhen } from '../../format'
import { DiagnosticsDetail, HoldLine, LogView, nextStep, trouble } from './settings/Diagnostics'
import { LimitEventLines } from './settings/Deployments'
import { TechnicalWords, useTechnicalDetail } from '../change/TechnicalDetail'
import type { DeploymentDiagnostics, DeploymentResources, PartLog, PartMemory } from '../../wire.gen'
import './deployment-containers.css'

// DeploymentContainers is what a deployment runs, one row per container:
// its state as the host reports it, the image it runs and what it uses now
// against its limits, with the deployment's totals above. A use the host
// did not report is unknown, never zero; a part with no limit says so.

type Row = { part: string; label: string; role?: string; diag?: DeploymentDiagnostics['parts'][number]; use?: PartMemory }

const cores = (n: number) => `${Number(n.toFixed(2))}`
// An image run by its ID reads as Docker's short ID; a named one in full.
const shortImage = (image: string) => image.startsWith('sha256:') ? image.slice(7, 19) : image

function Gauge({ used, limit, format, what }: { used: number; limit: number; format: (n: number) => string; what: string }) {
  const percent = limit > 0 ? Math.min(100, used / limit * 100) : 0
  return <span className={`container-gauge${percent > 80 ? ' container-gauge-high' : ''}`}>
    <span className="container-gauge-words">{format(used)}{limit > 0 ? <span className="muted"> / {format(limit)}</span> : <span className="muted"> · no limit</span>}</span>
    {limit > 0 && <meter className="container-gauge-track" aria-label={`${what} use`} min={0} max={limit} value={Math.min(used, limit)} />}
  </span>
}

// stopped: a person stopped the deployment, so its parts being stopped is
// what was asked, not a failure.
export function DeploymentContainers({ slug, name, stopped = false }: { slug: string; name: string; stopped?: boolean }) {
  const base = `/projects/${slug}/deployments/${name}`
  const resources = useGet<DeploymentResources>(`${base}/resources`, { poll: 5000 })
  const diagnostics = useGet<DeploymentDiagnostics>(`${base}/diagnostics`, { poll: 5000 })
  // A log is read when asked and kept only here, as Diagnostics keeps it.
  const [log, setLog] = useState<PartLog | null>(null)
  // The part whose log is being read; null when none is. The app's own part
  // is named '', so '' cannot mean idle.
  const [reading, setReading] = useState<string | null>(null)
  const [err, setErr] = useState('')
  const [technical] = useTechnicalDetail()
  if (!resources.data && !diagnostics.data) return <ReadStatus q={[resources, diagnostics]} what="its containers" />
  const unknownNow = resources.refreshFailed || !!resources.data?.reading?.unknown || !!resources.data?.reading?.stale
  const rows: Row[] = []
  const add = (part: string) => {
    let r = rows.find(r => r.part === part)
    if (!r) rows.push(r = { part, label: part || 'app' })
    return r
  }
  for (const p of diagnostics.data?.parts ?? []) {
    const r = add(p.part)
    r.diag = p
    r.role = p.need ? 'service' : p.public && p.part ? 'front door' : undefined
  }
  for (const p of resources.data?.parts ?? []) {
    const r = add(p.part)
    r.use = p.observed
    r.role ??= p.need ? 'service' : p.public && p.part ? 'front door' : undefined
  }
  const usage = (r: Row) => !unknownNow && r.use?.known ? r.use : undefined
  const known = rows.map(usage)
  const all = known.length > 0 && known.every(Boolean)
  const sum = (f: (u: PartMemory) => number) => all ? known.reduce((n, u) => n + f(u!), 0) : undefined
  const limited = (f: (u: PartMemory) => number) => all && known.every(u => f(u!) > 0) ? sum(f) : undefined
  const readLog = async (part: string) => {
    setReading(part); setErr(''); setLog(null)
    try { setLog(await get<PartLog>(`${base}/logs?part=${encodeURIComponent(part)}`)) }
    catch (e) { setErr(e instanceof Error ? e.message : String(e)) }
    finally { setReading(null) }
  }
  const diagUnknown = diagnostics.refreshFailed || !!diagnostics.data?.reading?.unknown
  const columns: ListColumn<Row>[] = [
    { key: 'name', label: 'Name', phone: 'name', render: r => <><strong>{r.label}</strong>{r.role && <span className="muted small"> {r.role}</span>}</> },
    { key: 'status', label: 'Status', phone: 'state', render: r => {
      const p = r.diag
      if (!p || p.presence === 'unknown' || diagUnknown) return <Status observation word="unknown" help={p?.reason} />
      const t = trouble(p)
      if (stopped && p.presence !== 'running') return <Status observation word="stopped" />
      return <><Status observation word={t ? 'failed' : 'running'} />{t && <span className="muted small"> {t}</span>}</>
    } },
    { key: 'image', label: 'Image', kind: 'technical', render: r => <span title={usage(r)?.image}>{usage(r)?.image ? shortImage(usage(r)!.image!) : '—'}</span> },
    { key: 'cpu', label: 'CPU', render: r => { const u = usage(r); return u ? <Gauge used={u.cpuCores} limit={u.cpuLimit} format={cores} what={`${r.label} CPU`} /> : <span className="muted" title={r.use?.reason}>unknown</span> } },
    { key: 'memory', label: 'Memory', render: r => { const u = usage(r); return u ? <Gauge used={u.usedBytes} limit={u.limitBytes} format={fmtBytes} what={`${r.label} memory`} /> : <span className="muted" title={r.use?.reason}>unknown</span> } },
    { key: 'started', label: 'Started', kind: 'time', render: r => r.diag?.facts?.running && r.diag.facts.startedAt ? fmtWhen(r.diag.facts.startedAt) : '—' },
    { key: 'more', label: '', phone: 'action', render: r => diagnostics.data?.containers && r.diag && r.diag.presence !== 'absent'
      ? reading === r.part ? <span className="muted small">reading…</span>
      : <MoreMenu label={r.label} items={[{ label: 'Logs', disabled: reading !== null, onClick: () => readLog(r.part) }]} />
      : null },
  ]
  return <section className="deployment-containers" aria-label="Containers">
    <div className="deployment-containers-strip">
      <CapacityStrip name="CPU" used={sum(u => u.cpuCores)} total={limited(u => u.cpuLimit)} unlimited format={v => `${cores(v)} cores`} />
      <CapacityStrip name="MEMORY" used={sum(u => u.usedBytes)} total={limited(u => u.limitBytes)} unlimited />
    </div>
    <ListTable label="Containers" className="deployment-containers-table" columns={columns} rows={rows} rowKey={r => r.part || 'app'} />
    {!diagnostics.data?.reading?.waiting && rows.filter(r => !stopped && r.diag && r.diag.presence !== 'unknown' && trouble(r.diag)).map(r =>
      <p key={r.part} className="small"><strong>{r.label}</strong>: {nextStep(r.diag!)}</p>)}
    {diagnostics.data && <HoldLine slug={slug} d={diagnostics.data} />}
    {diagnostics.data?.unsupported && <p className="muted small">{diagnostics.data.unsupported}</p>}
    {resources.data && <div className="small"><LimitEventLines r={resources.data} /></div>}
    {technical && diagnostics.data && <DiagnosticsDetail d={diagnostics.data} busy={reading !== null} onLog={readLog} />}
    {diagnostics.data?.unsettled && <TechnicalWords shown={technical}>{diagnostics.data.unsettled}</TechnicalWords>}
    {rows.some(r => !usage(r)) && <p className="muted small">{rows.find(r => !usage(r) && r.use?.reason)?.use?.reason ?? 'Its host did not report what every container uses.'}</p>}
    <StaleNote q={[resources, diagnostics]} />
    {err && <p className="error">{err}</p>}
    {log && <LogView log={log} onClose={() => setLog(null)} />}
  </section>
}
