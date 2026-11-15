import { Link } from 'react-router-dom'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { Section } from '../../Section'
import { PageHead } from '../../PageHead'
import { ListTable, type ListColumn } from '../../ListTable'
import { fmtWhen } from '../../format'
import { RuleConsent } from './RuleConsent'
import type { JanitorView, JanitorProject, StandingRevision } from '../../wire.gen'

const projectTo = (p: JanitorProject) => `/projects/${p.slug}#janitor`
const levelName = (level: string) => level === 'aggressive' ? 'Aggressive · including major' : level === 'routine' ? 'Routine · patch and minor' : level === 'off' ? 'Turned off for this project' : 'Not recorded'
const date = (value?: string) => value && !value.startsWith('0001-') ? fmtWhen(value) : 'Not known'
const covers = (r: StandingRevision, p: JanitorProject) => r.question.definition.scope === 'instance' || r.question.definition.projects.some(scope => scope.projectId === p.projectId)

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
      const projects = data.projects.filter(p => r.rule && covers(r.rule, p))
      const definition = r.rule?.question.definition
      const selected = definition?.projects.flatMap(p => p.environments.map(e => e.deploymentId)) ?? []
      const deps = data.deployments.filter(d => r.rule?.enabled && definition?.delivery === 'full' && d.automatic && projects.some(p => p.projectId === d.projectId && p.level !== 'off') && (definition.scope === 'instance' || selected.includes(d.deploymentId)))
      const exceptions = data.deployments.filter(d => projects.some(p => p.projectId === d.projectId) && !deps.includes(d))
      return !deps.length ? 'None' : exceptions.length ? `All deployments except ${exceptions.map(d => `${projects.find(p => p.projectId === d.projectId)!.name} / ${d.displayLabel}`).join(', ')}` : 'All deployments'
    } },
    { key: 'by', label: 'Granted by', render: r => r.rule?.issuer.name || 'Nobody' },
    { key: 'when', label: 'When', kind: 'time', render: r => r.rule ? date(r.rule.enabledAt) : '—' },
    { key: 'action', label: '', phone: 'action', render: r => <RuleConsent rule={r.rule} packs={r.packs} current={current} reload={reload} project={r.rule?.question.definition.scope === 'instance' ? undefined : data.projects.find(p => r.rule && covers(r.rule, p))?.slug} /> },
  ]
  return <ListTable label="Janitor approvals" rows={rows} columns={columns} rowKey={r => r.key} />
}

function Problems({ data }: { data: JanitorView }) {
  const policies = new Map<string, { key: string; workflow: string; title: string; version: number; sentence: string; stopRule: string; projects: JanitorProject[] }>()
  for (const p of data.projects) for (const proc of p.procedures) {
    const sentence = proc.problem ? `Not known: ${proc.problem}` : proc.sentence
    const key = `${proc.workflow}/${proc.version}/${sentence}/${proc.stopRule}`
    const row = policies.get(key) ?? { key, workflow: proc.workflow, title: proc.title, version: proc.version, sentence, stopRule: proc.stopRule, projects: [] }
    row.projects.push(p); policies.set(key, row)
  }
  const rows = [...policies.values()]
  const sharedVersions = new Map<string, typeof rows[number]>()
  for (const row of rows) {
    const shared = sharedVersions.get(row.workflow)
    if (!shared || row.projects.length > shared.projects.length || row.projects.length === shared.projects.length && row.version > shared.version) sharedVersions.set(row.workflow, row)
  }
  const deployments = data.deployments.filter(d => d.setting.mode === 'window' && data.projects.some(p => p.projectId === d.projectId))
  return <>
    {rows.length > 0 ? <ListTable label="Repair limits" rows={rows} rowKey={p => p.key} columns={[
      { key: 'work', label: 'Queue and workflow', phone: 'name', render: p => <><strong>{p.title}</strong><div>{p.workflow} · version {p.version}</div>{p.version !== sharedVersions.get(p.workflow)!.version && p.projects.map(project => <div key={project.projectId}><Link to={projectTo(project)}>{project.name}</Link></div>)}</> },
      { key: 'handling', label: 'If checks fail', render: p => p.sentence },
      { key: 'stop', label: 'When it stops', render: p => p.stopRule },
    ]} /> : null}
    <p>{data.releaseHold}</p>
    {deployments.length > 0 ? <ListTable label="Release settings" rows={deployments} rowKey={d => d.deploymentId!} columns={[
      { key: 'deployment', label: 'Deployment', phone: 'name', render: d => { const p = data.projects.find(p => p.projectId === d.projectId)!; return <Link to={`/projects/${p.slug}/deployments/${d.name}`}>{p.name} / {d.displayLabel}</Link> } },
      { key: 'setting', label: 'Accepted updates', render: d => `${d.setting.days}, ${d.setting.from}–${d.setting.to} (${d.setting.zone})` },
    ]} /> : null}
  </>
}

export default function JanitorSettings() {
  const q = useGet<JanitorView>('/janitor', { poll: 10000 })
  return <div><PageHead page="janitorSettings" />
    <ReadStatus q={q} what="janitor settings" /><StaleNote q={q} />
    {q.data && <><Section title="What it is allowed to do"><Rules data={q.data} current={q.current} reload={q.reload} /></Section>
      <Section title="How it handles problems"><Problems data={q.data} /></Section></>}
  </div>
}
