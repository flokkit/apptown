import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { post } from '../api'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import type { AuthzHoldings, ProjectDetail, ProjectPage, DeploymentView, ProjectLanes, StandingDefinition, StandingProject, StandingQuestion, StandingRevision, StandingStatus } from '../wire.gen'

type ProjectChoice = { id: string; slug: string; name: string }

export default function Janitor() {
  const { slug } = useParams()
  const auth = useGet<AuthzHoldings>('/authz/me')
  const project = useGet<ProjectDetail>(slug ? `/projects/${slug}` : null)
  const projects = useGet<ProjectPage>(slug ? null : '/projects')
  if (!auth.data) return <ReadStatus q={auth} what="your identity" />
  if (!auth.data.actorId) return <p>A verified person must sign in to manage standing consent.</p>
  if (slug && !project.data) return <ReadStatus q={project} what="the project" />
  if (!slug && !projects.data) return <ReadStatus q={projects} what="the projects" />
  const choices: ProjectChoice[] = slug && project.data?.id
    ? [{ id: project.data.id, slug, name: project.data.name }]
    : (projects.data?.projects ?? []).flatMap(p => p.id ? [{ id: p.id, slug: p.slug, name: p.name }] : [])
  return <JanitorEditor key={`${auth.data.actorId}:${slug ?? 'administration'}`} choices={choices} projectId={slug ? project.data?.id ?? undefined : undefined} />
}

function ProjectSelection({ choice, value, full, update }: { choice: ProjectChoice; value: StandingProject; full: boolean; update: (next: StandingProject) => void }) {
  const lanes = useGet<ProjectLanes>(`/projects/${choice.slug}/lanes`)
  const deployments = useGet<DeploymentView[]>(`/projects/${choice.slug}/deployments`)
  return <fieldset className="card">
    <legend>{choice.name} ({choice.slug})</legend>
    <p className="muted small">Project {choice.id}</p>
    {!lanes.data ? <ReadStatus q={lanes} what="the workflows" /> : <>
      <StaleNote q={lanes} />
      <label>Working workflow <select value={value.workflow} onChange={e => update({ ...value, workflow: e.target.value })}>
        <option value="">Select a workflow</option>
        {lanes.data.lanes.filter(l => l.selected && !l.withdrawn).map(l => <option key={l.name} value={l.name}>{l.name} v{l.version}</option>)}
      </select></label>
      <p className="muted small">Its approval and the landing approval must explicitly allow standing consent. Preview checks current policy.</p>
    </>}
    {full && (!deployments.data ? <ReadStatus q={deployments} what="the environments" /> : <>
      <StaleNote q={deployments} />
      <h4>Deliver to these environments</h4>
      {!deployments.data.some(d => d.kind === 'standing') && <p>No standing environment is configured. <Link to={`/projects/${choice.slug}`}>Configure one before enabling full delivery.</Link></p>}
      {deployments.data.filter(d => d.kind === 'standing' && d.id && d.targetId).map(d => {
        const selected = value.environments?.find(e => e.deploymentId === d.id)
        const set = (enabled: boolean) => update({ ...value, environments: enabled
          ? [...(value.environments ?? []), { deploymentId: d.id, targetId: d.targetId, promotion: false }]
          : (value.environments ?? []).filter(e => e.deploymentId !== d.id) })
        return <div key={d.id}>
          <label><input type="checkbox" checked={!!selected} onChange={e => set(e.target.checked)} /> {d.displayLabel || d.name}</label>
          <p className="muted small">Environment {d.id} · target {d.targetId}</p>
          {selected && <label><input type="checkbox" checked={selected.promotion} onChange={e => update({ ...value, environments: (value.environments ?? []).map(v => v.deploymentId === d.id ? { ...v, promotion: e.target.checked } : v) })} /> Also authorize promotion to this environment{d.protected ? ' (currently required)' : ''}</label>}
        </div>
      })}
    </>)}
  </fieldset>
}

function RuleHistory({ id }: { id: string }) {
  const q = useGet<StandingRevision[]>(`/standing-approvals/${id}/history`)
  if (!q.data) return <ReadStatus q={q} what="the consent history" />
  return <><StaleNote q={q} />{q.data.map(r => <details key={r.revision}><summary>Revision {r.revision}: {r.enabled ? 'enabled' : 'disabled'} by {r.changedBy.name} — {r.reason}</summary><p>Consent given by {r.issuer.name} on {r.enabledAt}.</p><pre style={{ whiteSpace: 'pre-wrap' }}>{r.question.shown}</pre></details>)}</>
}

function JanitorEditor({ choices, projectId }: { choices: ProjectChoice[]; projectId?: string }) {
  const q = useGet<StandingStatus[]>(`/standing-approvals${projectId ? `?projectId=${projectId}` : ''}`)
  const [definition, setDefinition] = useState<StandingDefinition>({ administrative: !projectId, projects: projectId ? [{ projectId, workflow: '', environments: [] }] : [], categories: { dependencies: 'patch', baseImages: false, securityFixes: true, waysOfWorking: false }, delivery: 'full' })
  const [editing, setEditing] = useState<StandingRevision | null>(null)
  const [question, setQuestion] = useState<StandingQuestion | null>(null)
  const [history, setHistory] = useState<string | null>(null)
  const [notice, setNotice] = useState('')
  const { run, busy, err } = useAction(undefined, { affects: ['/standing-approvals'] })
  const update = (next: StandingDefinition) => { setDefinition(next); setQuestion(null); setNotice('') }
  const preview = async () => { setQuestion(null); const next = await run(() => post<StandingQuestion>('/standing-approvals/preview', definition)); if (next) setQuestion(next) }
  const enable = async () => {
    if (!question) return
    const shown = question
    setQuestion(null)
    const saved = await run(() => post<StandingRevision>('/standing-approvals', { ruleId: editing?.ruleId ?? null, expectedRevision: editing?.revision ?? 0, definition: shown.definition, shownDigest: shown.digest }))
    q.reload()
    if (saved) { setEditing(saved); setNotice('Standing consent saved. Automatic processing is not available yet. No update or release has started.') }
    else setNotice('Consent was not confirmed. Read the current rules before trying again: a lost response may follow a committed save.')
  }
  return <div>
    <h1>Janitor</h1>
    <p>Give explicit standing consent for bounded routine updates. Automatic processing is not available yet; saving consent does not update, land or deploy software.</p>
    {projectId && <p><Link to="/janitor">Manage an explicit set of projects</Link> (administrator permission required).</p>}
    <h2>Current rules</h2>
    {!q.data ? <ReadStatus q={q} what="the standing approvals" /> : <><StaleNote q={q} />
      {q.data.length === 0 && <p>No standing approval is enabled or recorded here.</p>}
      {q.data.map(s => <section className="card" key={s.rule.ruleId}>
        <h3>Revision {s.rule.revision}: {s.rule.enabled ? 'Consent enabled' : 'Disabled'}</h3>
        <p>Enabled by {s.rule.issuer.name}. {s.reason}</p>
        <button disabled={busy} onClick={() => { setEditing(s.rule); update(s.rule.question.definition) }}>Revise and confirm</button>{' '}
        <button disabled={busy || !s.rule.enabled} onClick={async () => { const saved = await run(() => post<StandingRevision>(`/standing-approvals/${s.rule.ruleId}/disable`, { expectedRevision: s.rule.revision })); if (saved) { setQuestion(null); q.reload() } }}>Disable</button>{' '}
        <button onClick={() => setHistory(history === s.rule.ruleId ? null : s.rule.ruleId)}>History</button>
        {history === s.rule.ruleId && s.rule.ruleId && <RuleHistory id={s.rule.ruleId} />}
      </section>)}
    </>}
    <h2>{editing ? `Revise rule ${editing.ruleId}` : 'New standing consent'}</h2>
    <fieldset disabled={busy}>
      {!projectId && <><h3>Exact project set</h3>{choices.map(c => <label key={c.id} style={{ display: 'block' }}><input type="checkbox" checked={definition.projects.some(p => p.projectId === c.id)} onChange={e => update({ ...definition, projects: e.target.checked ? [...definition.projects, { projectId: c.id, workflow: '', environments: [] }] : definition.projects.filter(p => p.projectId !== c.id) })} /> {c.name} ({c.slug})</label>)}</>}
      <label>Dependency updates <select value={definition.categories.dependencies} onChange={e => update({ ...definition, categories: { ...definition.categories, dependencies: e.target.value } })}><option value="">None</option><option value="patch">Patch versions</option><option value="minor">Patch and minor versions</option></select></label>
      {(['baseImages', 'securityFixes', 'waysOfWorking'] as const).map((key, i) => <label key={key} style={{ display: 'block' }}><input type="checkbox" checked={definition.categories[key]} onChange={e => update({ ...definition, categories: { ...definition.categories, [key]: e.target.checked } })} />{['Verified base-image updates within the same version', 'Security fixes within the permitted dependency or image bounds', 'Governed routine ways-of-working transitions'][i]}</label>)}
      <label>Delivery <select value={definition.delivery} onChange={e => update({ ...definition, delivery: e.target.value, projects: definition.projects.map(p => ({ ...p, environments: [] })) })}><option value="full">Validate, land, deploy and verify health</option><option value="land-only">Accept and land only · running software unchanged</option></select></label>
      {definition.projects.map(p => {
        const choice = choices.find(c => c.id === p.projectId)
        return choice ? <ProjectSelection key={choice.id} choice={choice} value={p} full={definition.delivery === 'full'} update={next => update({ ...definition, projects: definition.projects.map(old => old.projectId === next.projectId ? next : old) })} /> : <p key={p.projectId}>Project {p.projectId} is not in this project list. The captured scope is retained; use the CLI to inspect or revise it.</p>
      })}
      <button onClick={preview}>Read the exact consent</button>
    </fieldset>
    {err && <p className="error">{err}</p>}{notice && <p role="status">{notice}</p>}
    {question && <section className="card"><h2>Confirm standing consent</h2><pre style={{ whiteSpace: 'pre-wrap' }}>{question.shown}</pre><button disabled={busy} onClick={enable}>I agree to this standing consent</button></section>}
  </div>
}
