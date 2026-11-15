import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useGet, useAction } from '../data'
import { post } from '../api'
import { ReadStatus, StaleNote } from '../ReadGate'
import { Section } from '../Section'
import { ListTable, type ListColumn } from '../ListTable'
import { Status } from '../Status'
import { fmtWhen } from '../format'
import { TryAgain } from './janitor/TryAgain'
import { firstLine } from './change/Head'
import type { JanitorView, JanitorProject, JanitorHistoryView, JanitorLandedChange, JanitorRunView, QueueStatus } from '../wire.gen'

const projectTo = (p: JanitorProject) => `/projects/${p.slug}#janitor`
const date = (value?: string) => value && !value.startsWith('0001-') ? fmtWhen(value) : 'Not known'

function summary(data: JanitorView) {
  const rules = data.rules.filter(r => r.enabled)
  if (!rules.length) return 'Turned off'
  const groups = new Map<string, Set<string>>()
  for (const { question: { definition: d } } of rules) {
    const scope = d.scope === 'instance' ? 'all projects' : data.projects.filter(p => d.projects.some(scope => scope.projectId === p.projectId)).map(p => p.name).join(', ')
    const kinds = groups.get(scope) ?? new Set<string>()
    if (d.categories.dependencies && d.categories.dependencies !== 'off') kinds.add(`Dependency updates (${d.categories.dependencies === 'aggressive' ? 'aggressive, including major' : 'routine, patch and minor'})`)
    if (d.categories.securityFixes) kinds.add('security fixes')
    if (d.categories.waysOfWorking) kinds.add('pack updates')
    groups.set(scope, kinds)
  }
  return [...groups].map(([scope, kinds]) => `${[...kinds].join(', ')} · ${scope}`).join('; ')
}

type Need = { key: string; project: JanitorProject; what: string; reason: string; to: string; queue?: QueueStatus }
function needs(data: JanitorView): Need[] {
  return data.projects.flatMap(project => [
    ...project.queues.filter(q => ['stuck', 'stopped', 'waiting'].includes(q.state)).map(queue => ({ key: `${project.projectId}/${queue.queue}`, project, what: queue.title, reason: [queue.words, firstLine(queue.reason)].filter(Boolean).join(' · '), to: queue.changeId ? `/changes/${queue.changeId}` : projectTo(project), queue })),
    ...data.deployments.filter(d => d.projectId === project.projectId && d.hold).map(d => ({ key: d.deploymentId!, project, what: d.displayLabel, reason: `Release ${d.hold!.releaseNumber} to ${d.displayLabel} ${d.hold!.reason === 'release failed' ? 'failed' : d.hold!.reason}`, to: `/projects/${project.slug}/deployments/${d.name}?tab=history` })),
    ...(project.scans.imageUpdates ? [{ key: `${project.projectId}/image`, project, what: 'Base image', reason: 'Base image needs an update; the janitor cannot do this', to: projectTo(project) }] : []),
  ])
}

function History({ projects }: { projects: JanitorProject[] }) {
  const [page, setPage] = useState({ older: false, before: '', offset: 0 })
  const params = new URLSearchParams({ period: page.older ? 'older' : 'recent', offset: String(page.offset) })
  if (page.before) params.set('before', page.before)
  const q = useGet<JanitorHistoryView>(`/janitor/history?${params}`, { poll: page.older ? undefined : 10000 })
  const data = q.data
  const columns: ListColumn<JanitorLandedChange>[] = [
    { key: 'project', label: 'Project', phone: 'name', render: c => <Link to={`/changes/${c.changeId}?tab=changes`}>{c.project}</Link> },
    { key: 'summary', label: 'Change', render: c => c.packVersions.length ? `Pack versions: ${c.packVersions.map(p => `${p.pack} v${p.version}`).join(', ')}` : c.packages.length ? c.packages.map(p => `${p.package} ${p.from} → ${p.to}`).join(', ') : projects.find(p => p.projectId === c.projectId)?.queues.find(q => q.queue === c.queue)?.title || c.title },
    { key: 'releases', label: 'Released', render: c => {
      const names = [...new Set(data?.releases.filter(r => r.changeId === c.changeId && r.state === 'released').map(r => r.displayLabel))]
      return <Link to={`/changes/${c.changeId}?tab=details`}>{names.length ? `Released to ${names.join(', ')}` : 'Not released'}</Link>
    } },
    { key: 'when', label: 'Landed', kind: 'time', render: c => date(c.landedAt) },
  ]
  return <>
    <h3>{page.older ? 'Older history' : 'Last 7 days'}</h3>
    <ReadStatus q={q} what="landed janitor Changes" /><StaleNote q={q} />
    {data && <>{data.history.changes.length ? <ListTable label="Landed janitor Changes" columns={columns} rows={data.history.changes} rowKey={c => c.changeId!} rowTo={c => `/changes/${c.changeId}?tab=changes`} /> : <p>{page.older ? 'No older janitor Changes.' : 'No janitor Changes landed in the last 7 days.'}</p>}
      <div className="row row-2">
        {page.offset > 0 && <button disabled={!q.current} onClick={() => setPage({ ...page, offset: Math.max(0, page.offset - 20) })}>Previous</button>}
        {data.history.more && <button disabled={!q.current} onClick={() => setPage({ ...page, before: data.until, offset: page.offset + 20 })}>Next 20</button>}
        {!page.older && <button disabled={!q.current} onClick={() => setPage({ older: true, before: data.since, offset: 0 })}>Older history</button>}
        {page.older && <button onClick={() => setPage({ older: false, before: '', offset: 0 })}>Last 7 days</button>}
      </div>
    </>}
  </>
}

export default function Janitor() {
  const q = useGet<JanitorView>('/janitor', { poll: 10000 })
  const data = q.data
  const mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  const action = useAction(() => { void q.reload() })
  const [refusals, setRefusals] = useState<string[]>([])
  const runAll = () => action.run(async () => {
    setRefusals([])
    const pending = data!.projects.filter(p => !p.runNow)
    const worker = async () => {
      while (mounted.current && pending.length) {
        const p = pending.shift()!
        try {
          const result = await post<JanitorRunView>(`/projects/${encodeURIComponent(p.slug)}/janitor/run`, { queue: '', again: false, changeId: '' })
          for (const s of result.starts) if (s.refused) setRefusals(rows => [...rows, `${p.name} · ${s.refused}`])
        } catch (err) { setRefusals(rows => [...rows, `${p.name} · ${err instanceof Error ? err.message : String(err)}`]) }
      }
    }
    await Promise.all([worker(), worker()])
  })
  const items = data ? needs(data) : []
  const columns: ListColumn<JanitorProject>[] = [
    { key: 'project', label: 'Project', phone: 'name', render: p => <Link to={projectTo(p)}>{p.name}</Link> },
    { key: 'state', label: 'State', phone: 'state', render: p => {
      const word = items.some(n => n.project === p) ? 'needs you' : p.scheduleProblem ? 'not known' : p.state.word
      return <>{q.refreshFailed ? <Status word="unknown" reason="not known" help="The latest refresh failed. This is the last answer read." /> : <>
        <Status word={word === 'not known' ? 'unknown' : word === 'needs you' ? 'waiting for you' : word === 'working' ? 'working' : word === 'up to date' ? 'current' : 'none'} reason={word} help={p.scheduleProblem && word === 'not known' ? p.scheduleProblem : p.state.help} />
        {p.scheduleProblem && word === 'needs you' && <Status word="unknown" reason="not known" help={p.scheduleProblem} />}
      </>}</>
    } },
    { key: 'scan', label: 'Last checked', kind: 'time', render: p => date(p.scans.lastScan) },
  ]
  return <div>
    <h1>Janitor</h1>
    <ReadStatus q={q} what="janitor" /><StaleNote q={q} />
    {data && <>
      <p>{summary(data)} · <Link to="/janitor/settings">Settings</Link></p>
      <button disabled={!q.current || action.busy || !data.projects.some(p => !p.runNow)} onClick={runAll}>Run now for all projects</button>
      {refusals.map((reason, i) => <div className="record-note" key={i}>{reason}</div>)}
      {items.length > 0 && <Section title="Needs you"><ListTable label="Needs you" rows={items} rowKey={n => n.key} rowTo={n => n.to} columns={[
        { key: 'project', label: 'Project', phone: 'name', render: n => <Link to={n.to}>{n.project.name}</Link> },
        { key: 'what', label: 'What', render: n => n.what },
        { key: 'reason', label: 'Reason', render: n => n.reason },
        { key: 'action', label: '', phone: 'action', render: n => n.queue && <TryAgain queue={n.queue} slug={n.project.slug} current={q.current && !action.busy} reload={q.reload} /> },
      ]} /></Section>}
      <Section title="Projects">{data.projects.length ? <ListTable label="Projects’ janitor state" columns={columns} rows={data.projects} rowKey={p => p.projectId!} rowTo={projectTo} /> : <p>No projects yet.</p>}</Section>
    </>}
    <Section title="Recent work"><History projects={data?.projects || []} /></Section>
  </div>
}
