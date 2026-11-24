import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { get } from '../../api'
import { useGet, type Read } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { Status } from '../../Status'
import { ListTable, type ListColumn } from '../../ListTable'
import { PageBar } from '../../PageBar'
import { fmtBytes, fmtCores, fmtWhen } from '../../format'
import { containerState, DiagnosticsDetail, HoldLine, LogView, NextStep, type ContainerState } from './settings/Diagnostics'
import { LimitEventLines } from './settings/Deployments'
import { DeploymentLine } from './settings/DeploymentLine'
import { TechnicalWords, useTechnicalDetail } from '../change/TechnicalDetail'
import { deploymentName } from '../../deploymentName'
import type { DeploymentDiagnostics, DeploymentResources, DeploymentView, PartLog, PartMemory } from '../../wire.gen'
import '../../overview-line.css'
import './settings/deployments.css'
import './deployment-containers.css'

// A deployment's containers, one row per part: its state as the host
// reports it (containerState), what it uses now against its limits, and on
// its own page the image it runs, when it started, its restarts and health,
// what happened at its limits and its log. A use the host did not report is
// unknown, never zero; a part with no limit says so. A target that runs no
// containers (a bucket website, diagnostics' noContainers) has no image,
// CPU, memory or start time: its parts say whether its address serves.

export type ContainerRow = { part: string; label: string; role?: string; diag?: DeploymentDiagnostics['parts'][number]; use?: PartMemory; state: ContainerState; available?: string }

// containerBuild is a need's running version and the newer build on its
// line, after the name: " 16.15 · 16.16 available". The table sets it apart
// from the bold name; containerLabel is the phone's Containers line.
export function containerBuild(r: ContainerRow) {
  const image = r.use?.image || ''
  const tag = image.split('@')[0].split('/').at(-1)?.split(':')[1] || ''
  const version = (r.diag?.need || r.use?.need) ? tag.match(/^\d+(?:\.\d+)+/)?.[0] : undefined
  return `${version ? ` ${version}` : ''}${r.available ? ` · ${r.available} available` : ''}`
}

export const containerLabel = (r: ContainerRow) => r.label + containerBuild(r)

// containerPath is a part's own page; the app's unnamed part is "app", as
// the rows label it.
export const containerPath = (slug: string, name: string, part: string) => `/projects/${slug}/deployments/${name}/containers/${encodeURIComponent(part || 'app')}`

// imageRows splits Docker references after separating the digest and tag;
// a colon in the registry's port is not a tag. The registry is the host
// alone; the namespace is part of the repository, as docker pull names it.
export function imageRows(image: string): { label: string; value: string; title?: string }[] {
  if (!image) return [{ label: 'Image', value: '—' }]
  if (image.startsWith('sha256:')) return [{ label: 'Image ID', value: image.slice(7, 19), title: image }]
  const [named, digest] = image.split('@')
  const colon = named.lastIndexOf(':')
  const tagged = colon > named.lastIndexOf('/')
  const tag = tagged ? named.slice(colon + 1) : ''
  const segments = (tagged ? named.slice(0, colon) : named).split('/')
  let host = 'docker.io'
  if (segments.length > 1 && /[.:]|^localhost$/.test(segments[0])) host = segments.shift()!
  if (host === 'index.docker.io' || host === 'registry-1.docker.io') host = 'docker.io'
  // Docker Hub's official images live in its library namespace, which people
  // write without a registry or namespace (mariadb:11.4).
  if (host === 'docker.io' && segments[0] === 'library' && segments.length === 2) segments.shift()
  const official = host === 'docker.io' && segments.length === 1
  return [
    ...official ? [] : [{ label: 'Registry', value: host }],
    { label: 'Repository', value: segments.join('/') },
    ...tag ? [{ label: 'Tag', value: tag }] : [],
    ...digest ? [{ label: 'Digest', value: digest.slice(0, 19), title: digest }] : [],
  ]
}

// containerRows joins the two reads into one row per part. stopped: a person
// stopped the deployment, so its parts being stopped is what was asked.
export function containerRows(resources: Read<DeploymentResources>, diagnostics: Read<DeploymentDiagnostics>, stopped: boolean) {
  const unknownNow = resources.refreshFailed || !!resources.data?.reading?.unknown || !!resources.data?.reading?.stale
  const diagUnknown = diagnostics.refreshFailed || !!diagnostics.data?.reading?.unknown
  const rows: ContainerRow[] = []
  const add = (part: string) => {
    let r = rows.find(r => r.part === part)
    if (!r) rows.push(r = { part, label: part || 'app', state: { word: 'unknown', trouble: '' } })
    return r
  }
  for (const p of diagnostics.data?.parts ?? []) {
    const r = add(p.part)
    r.diag = p
    r.role = p.need ? 'service' : p.public && p.part ? 'front door' : undefined
  }
  for (const p of resources.data?.parts ?? []) {
    if (p.suppliedService) continue
    const r = add(p.part)
    r.use = p.observed
    r.available = p.available
    r.role ??= p.need ? 'service' : p.public && p.part ? 'front door' : undefined
  }
  for (const r of rows) r.state = containerState(r.diag, { stopped, unknown: diagUnknown })
  const usage = (r: ContainerRow) => !unknownNow && r.use?.known ? r.use : undefined
  return { rows, usage, noContainers: !!diagnostics.data?.noContainers }
}

// nearLimit: a running part using more than 80% of its memory limit.
export const nearLimit = (u?: PartMemory) => !!u && u.limitBytes > 0 && u.usedBytes / u.limitBytes > 0.8

// kind names what the limit counts (Fly's "shared" CPUs), after it.
export function Gauge({ used, limit, format, what, kind }: { used: number; limit: number; format: (n: number) => string; what: string; kind?: string }) {
  const percent = limit > 0 ? Math.min(100, used / limit * 100) : 0
  return <span className={`container-gauge${percent > 80 ? ' container-gauge-high' : ''}`}>
    <span className="container-gauge-words">{format(used)}{limit > 0 ? <span className="muted"> / {format(limit)}{kind && ` ${kind}`}</span> : <span className="muted"> · no limit</span>}</span>
    {limit > 0 && <meter className="container-gauge-track" aria-label={`${what} use`} min={0} max={limit} value={Math.min(used, limit)} />}
  </span>
}

export function ContainerStatus({ state }: { state: ContainerState; available?: string }) {
  return <Status observation word={state.word} reason={state.word === 'failed' && state.trouble ? state.trouble : undefined} help={state.help} />
}

// ContainersTable is the deployment page's Containers card: each part, its
// state and its memory. A managed service the provider runs (an App Platform
// database) says what it is instead of a gauge. Each row opens its page.
export function ContainersTable({ slug, env, rows, usage, noContainers }: { slug: string; env: DeploymentView } & ReturnType<typeof containerRows>) {
  const need = (r: ContainerRow) => r.diag?.need || r.use?.need ? env.needs?.find(n => n.need === r.part) : undefined
  const columns: ListColumn<ContainerRow>[] = [
    { key: 'name', label: noContainers ? 'Website' : 'Container', phone: 'name', render: r => <Link to={containerPath(slug, env.name, r.part)}><strong>{r.label}</strong>{containerBuild(r) && <span className="container-build">{containerBuild(r)}</span>}</Link> },
    { key: 'status', label: 'Status', phone: 'state', render: r => <ContainerStatus state={r.state} /> },
    ...noContainers ? [] : [{ key: 'memory', label: 'Memory', render: (r: ContainerRow) => {
      const n = need(r)
      if (n) return <span title={n.id ? `the provider's id: ${n.id}` : undefined}>{[n.version && (n.need === 'postgres' ? `pg ${n.version}` : n.version), n.size, n.region].filter(Boolean).join(' · ') || n.name}</span>
      const u = usage(r)
      return u ? <Gauge used={u.usedBytes} limit={u.limitBytes} format={fmtBytes} what={`${r.label} memory`} /> : <span className="muted" title={r.use?.reason}>{r.use?.reason ? 'not reported' : 'unknown'}</span>
    } }],
  ]
  return <ListTable label={noContainers ? 'Website' : 'Containers'} className="deployment-containers-table" columns={columns} rows={rows} rowKey={r => r.part || 'app'} rowTo={r => containerPath(slug, env.name, r.part)} />
}

// DeploymentContainerPage is one part's page: its state and use, the image
// it runs and when it started, its restarts and health check, what happened
// at its limits in the last day, and its log, read when asked.
export function DeploymentContainerPage() {
  const { slug = '', name = '', part: asked = '' } = useParams()
  const deployments = useGet<DeploymentView[]>(`/projects/${slug}/deployments`, { poll: 5000 })
  const base = `/projects/${slug}/deployments/${name}`
  const resources = useGet<DeploymentResources>(`${base}/resources`, { poll: 5000 })
  const diagnostics = useGet<DeploymentDiagnostics>(`${base}/diagnostics`, { poll: 5000 })
  const [log, setLog] = useState<PartLog | null>(null)
  const [reading, setReading] = useState(false)
  const [err, setErr] = useState('')
  const [copyError, setCopyError] = useState('')
  const [technical] = useTechnicalDetail()
  const env = deployments.data?.find(e => e.name === name)
  const part = asked === 'app' ? '' : asked
  if (!env || !resources.data && !diagnostics.data || resources.loading || diagnostics.loading) return <><PageBar title={part || 'app'} /><ReadStatus q={[deployments, resources, diagnostics]} what="this container" /></>
  const { rows, usage, noContainers } = containerRows(resources, diagnostics, env.runtimeState === 'stopped')
  const r = rows.find(r => r.part === part)
  // Absence is said only on both answers: a read that failed cannot show a
  // part is not there.
  const partial = <ReadStatus q={[resources, diagnostics]} what="this container" />
  if (!r) return <><PageBar title={part || 'app'} />{resources.data && diagnostics.data ? <><StaleNote q={[resources, diagnostics]} /><p className="muted">{deploymentName(env)} has no container called {part || 'app'}.</p></> : partial}</>
  const u = usage(r)
  const d = diagnostics.data
  const f = r.diag?.facts
  const old = !!(r.diag?.stale || d?.reading?.waiting) && !!f
  const readLog = async () => {
    setReading(true); setErr(''); setLog(null)
    try { setLog(await get<PartLog>(`${base}/logs?part=${encodeURIComponent(r.part)}`)) }
    catch (e) { setErr(e instanceof Error ? e.message : String(e)) }
    finally { setReading(false) }
  }
  const events = resources.data && { ...resources.data, limits: resources.data.limits.filter(l => l.part === r.part) }
  return <>
    <PageBar title={<>{r.label} <span className="title-context">{deploymentName(env)}</span></>} after={<ContainerStatus state={r.state} />}
      actions={d?.containers && r.diag && r.diag.presence !== 'absent' ? <button disabled={reading} onClick={readLog}>{reading ? 'Reading the log…' : 'Log'}</button> : undefined} />
    {partial}
    <StaleNote q={[resources, diagnostics]} />
    <div className="card deploy-entry"><div className="deploy-lines">
      {r.role && <DeploymentLine label="Role">{r.role}</DeploymentLine>}
      {r.state.word === 'failed' && <DeploymentLine label="Next"><NextStep p={r.diag!} settings={`${base}?tab=settings`} container={containerPath(slug, name, r.part)} /></DeploymentLine>}
      {noContainers
        ? <DeploymentLine label="Serves">{r.state.word === 'running' ? 'this release at its address' : r.diag?.reason || 'not known'}</DeploymentLine>
        : <>
          <DeploymentLine label="Memory" trailer={<Link to={`${base}?tab=settings`}>limit →</Link>}>{u ? <Gauge used={u.usedBytes} limit={u.limitBytes} format={fmtBytes} what={`${r.label} memory`} /> : <span className="muted">{r.use?.reason || 'unknown'}</span>}</DeploymentLine>
          <DeploymentLine label="CPU">{u ? <Gauge used={u.cpuCores} limit={u.cpuLimit} format={u.cpuKind === 'shared' ? fmtCores : v => `${fmtCores(v)} cores`} what={`${r.label} CPU`} kind={u.cpuKind === 'shared' ? 'shared CPUs' : undefined} /> : <span className="muted">unknown</span>}</DeploymentLine>
          {imageRows(u?.image || '').map(row => <DeploymentLine key={row.label} label={row.label}>
            {row.label === 'Digest' ? <><button className="link" title={row.title} aria-label="Copy image reference" onClick={async () => {
              setCopyError('')
              try { await navigator.clipboard.writeText(u!.image!) }
              catch { setCopyError('Could not copy the image reference.') }
            }}><code>{row.value}</code></button>{copyError && <span className="error small">{copyError}</span>}</> : row.label === 'Image' ? row.value : <code title={row.title}>{row.value}</code>}
          </DeploymentLine>)}
          <DeploymentLine label="Started">{f?.running && f.startedAt ? fmtWhen(f.startedAt) : f && !f.running && f.finishedAt ? <>stopped {fmtWhen(f.finishedAt)}{f.exitCode != null ? ` · exit ${f.exitCode}` : ''}</> : '—'}{old && <span className="muted"> (last read {r.diag?.observedAt ? fmtWhen(r.diag.observedAt) : 'earlier'})</span>}</DeploymentLine>
          <DeploymentLine label="Restarts">{f ? f.restarts : '—'}</DeploymentLine>
          <DeploymentLine label="Health check">{f?.health || '—'}</DeploymentLine>
          {events && <DeploymentLine label="Last day"><LimitEventLines r={events} /></DeploymentLine>}
        </>}
      {d?.hold && <DeploymentLine label="Restarts held"><HoldLine slug={slug} d={d} /></DeploymentLine>}
      {d?.unsupported && <DeploymentLine label="Not read"><span className="muted">{d.unsupported}</span></DeploymentLine>}
    </div></div>
    {technical && d && <DiagnosticsDetail d={d} busy={reading} onLog={() => readLog()} />}
    {d?.unsettled && <TechnicalWords shown={technical}>{d.unsettled}</TechnicalWords>}
    {err && <p className="error">{err}</p>}
    {log && <LogView log={log} onClose={() => setLog(null)} />}
  </>
}
