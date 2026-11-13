import { JanitorAttribution } from '../janitorAttribution'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { Section } from '../Section'
import { ListTable, type ListColumn } from '../ListTable'
import { Status } from '../Status'
import { fmtWhen } from '../format'
import { RuleConsent } from './janitor/RuleConsent'
import type { JanitorView, JanitorProject, JanitorHistoryView, JanitorLandedChange, StandingRevision } from '../wire.gen'

const projectTo = (p: JanitorProject) => `/projects/${p.slug}#janitor`
const levelName = (level: string) => level === 'aggressive' ? 'Nightly · including major' : level === 'routine' ? 'Weekly · patch and minor' : level === 'off' ? 'Turned off for this project' : 'Not recorded'
const date = (value?: string) => value && !value.startsWith('0001-') ? fmtWhen(value) : 'Not known'
const covers = (r: StandingRevision, p: JanitorProject) => r.question.definition.scope === 'instance' || r.question.definition.projects.some(scope => scope.projectId === p.projectId)
const workName = (name: string) => name === 'janitor-fix' ? 'Security fixes' : name === 'janitor-update' ? 'Dependency updates' : name === 'janitor-adopt' ? 'Pack updates' : name

function ProjectState({ project: p, data, stale }: { project: JanitorProject; data: JanitorView; stale: boolean }) {
  if (stale) return <Status word="unknown" help="The latest refresh failed. This is the last answer read." />
  const hold = data.deployments.find(d => d.projectId === p.projectId && d.hold)?.hold
  if (p.level === 'off') return <span className="badge">turned off for this project</span>
  if (!p.enabled) return <span className="badge">turned off</span>
  if (hold) return <span className="badge">{hold.reason === 'release failed' ? `stopped: release ${hold.releaseNumber} to ${hold.displayLabel} failed` : `held back · ${hold.displayLabel}: ${hold.reason}`}</span>
  const active = p.queues.find(q => q.state === 'updating')
  const stopped = p.queues.find(q => q.state === 'stopped')
  const stuck = p.queues.find(q => q.state === 'stuck')
  if (stopped) return <>{active && <span className="badge">updating</span>}{p.queueSentences[p.queues.indexOf(stopped)]}</>
  if (active) return <span className="badge">updating</span>
  if (stuck) return <span className="badge">stuck since {date(stuck.since)}</span>
  if (p.problem || !p.scans.known) return <Status word="unknown" help={p.problem || p.scans.reason || 'Scan not known'} />
  if (p.queues.some(q => q.state === 'waiting')) return <span className="badge">Waiting for a person</span>
  return <span className="badge">Up to date</span>
}

function Rules({ data, current, reload }: { data: JanitorView; current: boolean; reload: () => Promise<void> }) {
  const rows: { key: string; packs: boolean; rule?: StandingRevision }[] = data.rules.map(rule => ({ key: rule.ruleId!, packs: rule.question.definition.categories.waysOfWorking, rule }))
  for (const packs of [false, true]) if (!rows.some(r => r.packs === packs && r.rule?.question.definition.scope === 'instance')) rows.push({ key: packs ? 'packs-off' : 'dependencies-off', packs })
  const columns: ListColumn<typeof rows[number]>[] = [
    { key: 'what', label: 'Change and limit', phone: 'name', render: r => <><strong>{r.packs ? 'Keep packs current' : 'Dependency updates'}</strong><div>{r.packs ? 'Newest published pack versions' : levelName(r.rule?.question.definition.categories.dependencies || 'routine')}</div>{r.rule?.question.definition.categories.securityFixes && <div>Security fixes within the same limits</div>}<span className="badge">{r.rule?.enabled ? 'On' : 'Turned off'}</span></> },
    { key: 'scope', label: 'Projects', render: r => <>
      {!r.rule || r.rule.question.definition.scope === 'instance' ? 'All projects, including new ones' : data.projects.filter(p => covers(r.rule!, p)).map(p => p.name).join(', ')}
      {data.projects.filter(p => p.choice && (!r.packs || p.level === 'off') && (!r.rule || covers(r.rule, p))).map(p => <div key={p.projectId}><Link to={projectTo(p)}>{p.name}</Link> · {levelName(p.level)} · {p.choice!.person.name}, {date(p.choice!.at)} · {p.choice!.reason}</div>)}
    </> },
    { key: 'releases', label: 'Automatic releases', render: r => {
      const projects = data.projects.filter(p => r.rule && covers(r.rule, p) && p.level !== 'off')
      const definition = r.rule?.question.definition
      const selected = definition?.projects.flatMap(p => p.environments.map(e => e.deploymentId)) ?? []
      const deps = data.deployments.filter(d => r.rule?.enabled && definition?.delivery === 'full' && d.automatic && projects.some(p => p.projectId === d.projectId) && (definition.scope === 'instance' || selected.includes(d.deploymentId)))
      return deps.length ? deps.map(d => { const p = projects.find(p => p.projectId === d.projectId)!; return <div key={d.deploymentId}><Link to={`/projects/${p.slug}/deployments/${d.name}`}>{p.name} / {d.displayLabel}</Link></div> }) : 'None'
    } },
    { key: 'by', label: 'Granted by', render: r => r.rule?.issuer.name || 'Nobody' },
    { key: 'when', label: 'When', kind: 'time', render: r => r.rule ? date(r.rule.enabledAt) : '—' },
    { key: 'action', label: '', phone: 'action', render: r => <RuleConsent rule={r.rule} packs={r.packs} current={current} reload={reload} project={r.rule?.question.definition.scope === 'instance' ? undefined : data.projects.find(p => r.rule && covers(r.rule, p))?.slug} /> },
  ]
  return <ListTable label="Janitor approvals" rows={rows} columns={columns} rowKey={r => r.key} />
}

function releasingDeployments(data: JanitorView) {
  return data.deployments.filter(d => {
    const project = data.projects.find(p => p.projectId === d.projectId)
    if (!d.automatic || !project || project.level === 'off') return false
    return data.rules.some(r => {
      const definition = r.question.definition
      return r.enabled && covers(r, project) && definition.delivery === 'full' && (definition.scope === 'instance' || definition.projects.some(p => p.projectId === d.projectId && p.environments.some(e => e.deploymentId === d.deploymentId)))
    })
  })
}

function Problems({ data }: { data: JanitorView }) {
  const policies = new Map<string, { key: string; workflow: string; version: number; sentence: string; stopRule: string; projects: JanitorProject[] }>()
  for (const p of data.projects) for (const proc of p.procedures) {
    const sentence = proc.problem ? `Not known: ${proc.problem}` : proc.sentence
    const key = `${proc.workflow}/${proc.version}/${sentence}/${proc.stopRule}`
    const row = policies.get(key) ?? { key, workflow: proc.workflow, version: proc.version, sentence, stopRule: proc.stopRule, projects: [] }
    row.projects.push(p); policies.set(key, row)
  }
  const rows = [...policies.values()]
  const sharedVersions = new Map<string, typeof rows[number]>()
  for (const row of rows) {
    const shared = sharedVersions.get(row.workflow)
    if (!shared || row.projects.length > shared.projects.length || row.projects.length === shared.projects.length && row.version > shared.version) sharedVersions.set(row.workflow, row)
  }
  const deployments = releasingDeployments(data)
  return <>
    {!data.projects.length && <p>No project settings yet.</p>}
    {rows.length > 0 ? <ListTable label="Repair limits" rows={rows} rowKey={p => p.key} columns={[
      { key: 'work', label: 'Queue and workflow', phone: 'name', render: p => <><strong>{workName(p.workflow)}</strong><div>{p.workflow} · version {p.version}</div>{p.version !== sharedVersions.get(p.workflow)!.version && p.projects.map(project => <div key={project.projectId}><Link to={projectTo(project)}>{project.name}</Link></div>)}</> },
      { key: 'handling', label: 'If checks fail', render: p => p.sentence },
      { key: 'stop', label: 'When it stops', render: p => p.stopRule },
    ]} /> : data.projects.length > 0 && <p>No janitor procedure adopted.</p>}
    <p>{data.releaseHold}</p>
    {deployments.length > 0 && <ListTable label="Release settings" rows={deployments} rowKey={d => d.deploymentId!} columns={[
      { key: 'deployment', label: 'Deployment', phone: 'name', render: d => { const p = data.projects.find(p => p.projectId === d.projectId)!; return <Link to={`/projects/${p.slug}/deployments/${d.name}`}>{p.name} / {d.displayLabel}</Link> } },
      { key: 'setting', label: 'Accepted updates', render: d => d.setting.mode === 'window' ? `${d.setting.days}, ${d.setting.from}–${d.setting.to} (${d.setting.zone})` : 'Automatic' },
    ]} />}
  </>
}

function History() {
  const [page, setPage] = useState({ older: false, before: '', offset: 0 })
  const params = new URLSearchParams({ period: page.older ? 'older' : 'recent', offset: String(page.offset) })
  if (page.before) params.set('before', page.before)
  const q = useGet<JanitorHistoryView>(`/janitor/history?${params}`, { poll: page.older ? undefined : 10000 })
  const data = q.data
  const columns: ListColumn<JanitorLandedChange>[] = [
    { key: 'change', label: 'Change', phone: 'name', render: c => <Link to={`/changes/${c.changeId}`}>{c.project} · {c.title}</Link> },
    { key: 'packages', label: 'Updated', render: c => c.queue === 'packs' ? c.packVersions.map(p => `${p.pack} version ${p.version}`).join(', ') || 'Pack versions not recorded' : c.packages.map(p => `${p.package} ${p.from} → ${p.to}`).join(', ') || 'Package versions not recorded' },
    { key: 'releases', label: 'Released to', render: c => {
      const releases = data?.releases.filter(r => r.changeId === c.changeId) || []
            return releases.length ? releases.map(r => <div key={`${r.deployment}-${r.number}`}><Link to={`/projects/${c.slug}/releases/${r.number}/log`}>{r.displayLabel} · release {r.number}</Link> · {r.state}</div>) : 'No releases recorded'
    } },
    { key: 'origin', label: 'By', render: c => c.ruleId ? <><JanitorAttribution rule={c.rule!} revision={c.ruleRevision} issuer={c.issuer.name} /> · {date(c.grantedAt)}</> : <>The janitor · schedule {workName(c.schedule)}</> },
    { key: 'evidence', label: 'Evidence', render: c => <Link to={`/changes/${c.changeId}?tab=details`}>Checks and approval</Link> },
    { key: 'when', label: 'Landed', kind: 'time', render: c => date(c.landedAt) },
  ]
  return <>
    <h3>{page.older ? 'Older history' : 'Last 7 days'}</h3>
    <ReadStatus q={q} what="landed janitor Changes" /><StaleNote q={q} />
    {data && <>{data.history.changes.length ? <ListTable label="Landed janitor Changes" columns={columns} rows={data.history.changes} rowKey={c => c.changeId!} rowTo={c => `/changes/${c.changeId}`} /> : <p>{page.older ? 'No older janitor Changes.' : 'No janitor Changes landed in the last 7 days.'}</p>}
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
  const columns: ListColumn<JanitorProject>[] = [
    { key: 'project', label: 'Project', phone: 'name', render: p => <Link to={projectTo(p)}>{p.name}</Link> },
    { key: 'state', label: 'State', phone: 'state', render: p => <ProjectState project={p} data={data!} stale={q.refreshFailed} /> },
    { key: 'scan', label: 'Last scan', kind: 'time', render: p => <>{date(p.scans.lastScan)}{p.scans.stale && <span className="badge">Old scan</span>}</> },
    { key: 'findings', label: 'Findings', kind: 'number', render: p => p.scans.known ? new Set([...p.scans.source.flatMap(s => s.findings), ...p.scans.running.flatMap(s => s.findings)].map(f => f.fingerprint)).size : 'Not known' },
    { key: 'next', label: 'Next update', render: p => !p.enabled || p.level === 'off' ? '—' : data?.deployments.some(d => d.projectId === p.projectId && d.hold) ? 'Waiting for a person to recover the release' : p.nextReason || date(p.nextUpdate) },
    { key: 'level', label: 'Level', render: p => <>{levelName(p.level)}<div>{p.choice ? `${p.choice.person.name} · ${date(p.choice.at)}` : p.issuer.name ? `${p.issuer.name} · ${date(p.grantedAt)}` : 'Nobody has enabled updates'}</div></> },
  ]
  const images = data?.projects.filter(p => p.scans.running.some(s => s.imageOnlyFindings.length > 0)).length ?? 0
  return <div>
    <h1>Janitor</h1><p>Keeps projects updated within the limits people grant. <Link to="/docs/guides/janitor">Janitor guide</Link></p>
    <Section title="What it is allowed to do"><ReadStatus q={q} what="janitor approvals" /><StaleNote q={q} />{data && <Rules data={data} current={q.current} reload={q.reload} />}</Section>
    <Section title="How it handles problems"><ReadStatus q={q} what="janitor settings" /><StaleNote q={q} />{data && <Problems data={data} /> }</Section>
    <Section title="What it is doing and waiting on"><ReadStatus q={q} what="projects’ janitor state" /><StaleNote q={q} />{data && <>
      {data.projects.length ? <ListTable label="Projects’ janitor state" columns={columns} rows={data.projects} rowKey={p => p.projectId!} rowTo={projectTo} /> : <p>No projects yet.</p>}
      {images > 0 && <p>{images} apps’ images need a base-image update; the janitor does not do this yet.</p>}
    </>}</Section>
    <Section title="What it has done"><History /></Section>
  </div>
}
