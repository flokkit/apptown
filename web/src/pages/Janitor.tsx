import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { post } from '../api'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import type { ProjectDetail, ProjectRow, DeploymentView, ProjectLanes, StandingDefinition, StandingProject, StandingQuestion, StandingRevision, StandingStatus } from '../wire.gen'

export default function Janitor() {
  const { slug } = useParams()
  const project = useGet<ProjectDetail>(slug ? `/projects/${slug}` : null)
  return <div><h1>Janitor</h1><p>Coming soon</p><p className="muted">Automatic updates are not available yet.</p>
    {slug && <Link to={`/projects/${slug}`}>Back to the project</Link>}
    {slug && !project.data ? <ReadStatus q={project} what="the project" /> : <SavedRules projectId={project.data?.id ?? undefined} />}
  </div>
}

// Previously recorded consent can still be read, revised and disabled. No new consent
// is offered while automatic processing is unavailable.
function SavedRules({ projectId }: { projectId?: string }) {
  const q = useGet<StandingStatus[]>(`/standing-approvals${projectId ? `?projectId=${projectId}` : ''}`)
  const [editing, setEditing] = useState<StandingRevision | null>(null)
  const [history, setHistory] = useState<string | null>(null)
  const { run, busy, err } = useAction()
  return <><ReadStatus q={q} what="saved consent" /><StaleNote q={q} />{err && <p className="error">{err}</p>}
    {!!q.data?.length && <details><summary>Saved consent ({q.data.length})</summary>
      {q.data.map(s => <section className="card" key={s.rule.ruleId}>
        <p>{s.rule.enabled ? 'Enabled' : 'Disabled'} by {s.rule.enabled ? s.rule.issuer.name : s.rule.changedBy.name}. {s.reason}</p>
        <details><summary>Details</summary><pre style={{ whiteSpace: 'pre-wrap' }}>{s.rule.question.shown}</pre>
          <button disabled={busy} onClick={() => setEditing(s.rule)}>Revise consent</button>{' '}
          <button onClick={() => setHistory(history === s.rule.ruleId ? null : s.rule.ruleId)}>History</button>
          {history === s.rule.ruleId && s.rule.ruleId && <RuleHistory id={s.rule.ruleId} />}
        </details>
        {s.rule.enabled && <button disabled={busy} onClick={() => void run(() => post(`/standing-approvals/${s.rule.ruleId}/disable`, { expectedRevision: s.rule.revision }))}>Disable</button>}
      </section>)}
    </details>}
    {editing && <details open><summary>Revise saved consent</summary><SavedConsentEditor key={`${editing.ruleId}:${editing.revision}`} rule={editing} onClose={() => setEditing(null)} /></details>}
  </>
}

type ProjectChoice = { id: string; slug: string; name: string }
function ProjectSelection({ choice, value, full, update }: { choice: ProjectChoice; value: StandingProject; full: boolean; update: (next: StandingProject) => void }) {
  const lanes = useGet<ProjectLanes>(`/projects/${choice.slug}/lanes`)
  const deployments = useGet<DeploymentView[]>(`/projects/${choice.slug}/deployments`)
  return <fieldset className="card">
    <legend>{choice.name}</legend>
    <div className="small" style={{ margin: '8px 0' }}><details className="inline-detail"><summary>Technical details</summary><code>{choice.id}</code></details></div>
    {!lanes.data ? <ReadStatus q={lanes} what="the workflows" /> : <>
      <StaleNote q={lanes} />
      <label>Working workflow <select value={value.workflow} onChange={e => update({ ...value, workflow: e.target.value })}>
        <option value="">Select a workflow</option>
        {lanes.data.lanes.filter(l => l.selected && !l.withdrawn).map(l => <option key={l.name} value={l.name}>{l.name} v{l.version}</option>)}
      </select></label>
      <p className="muted small">Preview checks the current approval rules.</p>
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
          <details className="inline-detail small"><summary>Technical details</summary><p>Deployment {d.id} · target {d.targetId}</p></details>
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


function SavedConsentEditor({ rule, onClose }: { rule: StandingRevision; onClose: () => void }) {
  const projects = useGet<ProjectRow[]>('/projects')
  const choices: ProjectChoice[] = (projects.data ?? []).flatMap(p => p.id ? [{ id: p.id, slug: p.slug, name: p.name }] : [])
  const [definition, setDefinition] = useState<StandingDefinition>(rule.question.definition)
  const [question, setQuestion] = useState<StandingQuestion | null>(null)
  const [notice, setNotice] = useState('')
  const { run, busy, err } = useAction(undefined, { affects: ['/standing-approvals'] })
  const update = (next: StandingDefinition) => { setDefinition(next); setQuestion(null); setNotice('') }
  const preview = async () => { setQuestion(null); const next = await run(() => post<StandingQuestion>('/standing-approvals/preview', definition)); if (next) setQuestion(next) }
  const enable = async () => {
    if (!question) return
    const shown = question
    setQuestion(null)
    const saved = await run(() => post<StandingRevision>('/standing-approvals', { ruleId: rule.ruleId, expectedRevision: rule.revision, definition: shown.definition, shownDigest: shown.digest }))
    if (saved) onClose()
    else setNotice('Consent was not confirmed. Read the current rules before trying again: a lost response may follow a committed save.')
  }
  if (!projects.data) return <ReadStatus q={projects} what="the projects" />
  return <section><StaleNote q={projects} /><p>Automatic updates remain unavailable. Revising consent starts no work.</p><button onClick={onClose}>Cancel</button>
    <fieldset disabled={busy}>
      {definition.administrative && <><h3>Projects</h3>{choices.map(c => <label key={c.id} style={{ display: 'block' }}><input type="checkbox" checked={definition.projects.some(p => p.projectId === c.id)} onChange={e => update({ ...definition, projects: e.target.checked ? [...definition.projects, { projectId: c.id, workflow: '', environments: [] }] : definition.projects.filter(p => p.projectId !== c.id) })} /> {c.name}</label>)}</>}
      <label>Dependency updates <select value={definition.categories.dependencies} onChange={e => update({ ...definition, categories: { ...definition.categories, dependencies: e.target.value } })}><option value="">None</option><option value="patch">Small fixes</option><option value="minor">Small fixes and minor updates</option></select></label>
      {(['baseImages', 'securityFixes', 'waysOfWorking'] as const).map((key, i) => <label key={key} style={{ display: 'block' }}><input type="checkbox" checked={definition.categories[key]} onChange={e => update({ ...definition, categories: { ...definition.categories, [key]: e.target.checked } })} />{['System image fixes', 'Security fixes', 'Routine rule updates'][i]}</label>)}
      <label>Delivery <select value={definition.delivery} onChange={e => update({ ...definition, delivery: e.target.value, projects: definition.projects.map(p => ({ ...p, environments: [] })) })}><option value="full">Validate, land, deploy and verify health</option><option value="land-only">Accept and land only · running software unchanged</option></select></label>
      {definition.projects.map(p => {
        const choice = choices.find(c => c.id === p.projectId)
        return choice ? <ProjectSelection key={choice.id} choice={choice} value={p} full={definition.delivery === 'full'} update={next => update({ ...definition, projects: definition.projects.map(old => old.projectId === next.projectId ? next : old) })} /> : <p key={p.projectId}>A saved project is not available in this list. Its selection is kept.</p>
      })}
      <button onClick={preview}>Preview</button>
    </fieldset>
    {err && <p className="error">{err}</p>}{notice && <p role="status">{notice}</p>}
    {question && <section className="card"><h2>Confirm standing consent</h2><pre style={{ whiteSpace: 'pre-wrap' }}>{question.shown}</pre><button disabled={busy} onClick={enable}>I agree to this standing consent</button></section>}
  </section>
}
