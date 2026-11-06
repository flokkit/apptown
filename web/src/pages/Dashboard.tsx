import './Dashboard.css'
import { Status } from '../Status'
import { useAttention, attentionState, useOperator } from '../attention'
import { PRODUCT } from '../brand'
import { RuntimeObservation } from '../runtime'
import { ProjectListRow } from '../ProjectListRow'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Dashboard as DashboardData, post } from '../api'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { relTime } from '../format'
import { Section } from '../Section'
import { ActiveRunList, ChangeRow, RunRow } from '../parts/runActivity'
import type { Operation, ProjectPage, SetupState, WaitingRelease } from '../wire.gen'
import { endedWords } from './DataProtection'
import { LocalTrialNote } from '../LocalTrialNote'

// Home opens apps first, then shows work that needs attention.

export type Activity = {
  agents: { project: string; runId: string | null; changeId: string | null; title: string; step: string; type: string; elapsedS: number }[]
  releaseApprovals?: WaitingRelease[]
  waitingOnYou: RunRow[]
  active: RunRow[]
  history: ChangeRow[]
  deployments: (RuntimeObservation & { project: string; name: string; url: string; hostOnly?: boolean; running: boolean | null })[]
}
type Setup = SetupState

// First-run onboarding: a fresh AppTown says what
// it needs instead of sitting silently inert.
function SetupCard() {
  const { data: setup, reload } = useGet<Setup>('/setup', { poll: 6000 })
  const [provider, setProvider] = useState('anthropic')
  const [key, setKey] = useState('')
  const { run, busy, err } = useAction(reload)
  if (!setup) return null
  const save = async () => {
    if (await run(() => post('/setup/model', { provider, key })) !== undefined) setKey('')
  }
  return (
    <>
      {setup.openNonLoopback && (
        <div className="card tone-warn dashboard-setup-card-card-tone-warn">
          ⚠ {PRODUCT} is listening on a non-loopback address with open auth · anyone who can
          reach the port has full control. Bind to 127.0.0.1, or put an authenticating proxy
          in front and use trusted-header auth.
        </div>
      )}
      {setup.needsModel && (
        <div className="card tone-accent mb-3 dashboard-setup-card-card-tone-accent">
          <strong className="dashboard-setup-card-strong">Add a model API key to start building</strong>
          <div className="muted small dashboard-setup-card-div">
            It is kept in {PRODUCT}'s secrets file, is not shown again, and is sent only to steps
            that use that provider.
          </div>
          <div className="row">
            <select aria-label="Model provider" value={provider} onChange={(e) => setProvider(e.target.value)}>
              <option value="anthropic">Anthropic</option>
              <option value="openrouter">OpenRouter</option>
            </select>
            <input aria-label="API key" type="password" value={key} onChange={(e) => setKey(e.target.value)}
              placeholder="paste your API key"
              onKeyDown={(e) => e.key === 'Enter' && key && save()} className="grow dashboard-setup-card-input" />
            <button className="primary" disabled={busy || !key} onClick={save}>{busy ? '…' : 'Save'}</button>
          </div>
          {err && <div className="error small dashboard-setup-card-error">{err}</div>}
          <div className="dashboard-setup-card-div-2"><LocalTrialNote /></div>
        </div>
      )}
      {setup.agentImage === 'building' && (
        <div className="card dashboard-setup-card-card-tone-warn">
          <Status word={"working"} /> Building the agent image (first run only) · a few
          minutes; agents queue until it finishes.
        </div>
      )}
      {setup.agentImage === 'pulling' && (
        <div className="card dashboard-setup-card-card-tone-warn">
          <Status word={"working"} /> Downloading the agent image (first run only) · a few
          minutes; agents queue until it finishes.
        </div>
      )}
      {(setup.agentImage === 'failed' || setup.agentImage === 'missing') && (
        <div className="card tone-bad dashboard-setup-card-card-tone-warn">
          ✗ The agent image is not available: {setup.agentImageError || 'unknown reason'}
        </div>
      )}
    </>
  )
}

// Counts describe only what was read. Unknown never becomes zero.
type HomeStatusInput = {
  needsYou: number | null; running: number; deployments: Activity['deployments']; operations: { count: number | null; known: boolean; unknown?: number }
}
type HomeStatusPart = { label: string; value: number | string; to: string }

export function HomeStatus(input: HomeStatusInput) {
  const parts = homeStatusParts(input)
  const quiet = parts.length === 0 && input.needsYou === 0 && input.running === 0 && input.operations.known && input.operations.count === 0
  return <div className="dash-lead">
    {quiet ? 'All quiet' : parts.map(p => {
      const content = <><span className="muted">{p.label}</span><span className="home-stat-value">{p.value}</span></>
      return p.to.startsWith('#')
        ? <a className="home-stat" key={p.label} href={p.to}>{content}</a>
        : <Link className="home-stat" key={p.label} to={p.to}>{content}</Link>
    })}
  </div>
}

// Each count links to the list of exactly what it counts.
export function homeStatusParts({ needsYou, running, deployments, operations }: HomeStatusInput): HomeStatusPart[] {
  const parts: HomeStatusPart[] = []
  if (needsYou === null || needsYou > 0) parts.push({ label: 'Waiting on you', value: needsYou ?? "couldn't read", to: '#waiting-on-you' })
  if (running > 0) parts.push({ label: 'Other work', value: running, to: '#other-work' })
  // An app is live when one of its deployments was seen running on the
  // latest check. One whose status is not current (stale or unknown) and
  // has no live deployment is counted apart, so unknown never reads as down.
  const live = new Set<string>(), unknown = new Set<string>()
  for (const d of deployments) {
    if (d.running === true && !d.observationStale) live.add(d.project)
  }
  for (const d of deployments) {
    if (!live.has(d.project) && (d.observationStale || d.running === null)) unknown.add(d.project)
  }
  if (deployments.length > 0) parts.push({ label: 'Apps live', value: live.size, to: '/deployments/table' })
  if (unknown.size > 0) parts.push({ label: 'Apps not checked', value: unknown.size, to: '/deployments/table' })
  if (operations.count) parts.push({ label: 'Problems', value: operations.count, to: '/attention' })
  // Conditions AppTown could not read are listed with their reasons on
  // Attention; a bare count of them on Home tells a person nothing to do.
  return parts
}

// History has no stop-actor field, so stopped work cannot be attributed here.
export function homeFinished(history: ChangeRow[]): ChangeRow[] {
  // What changed lately, across everyone: landed changes only. A failed
  // change that needs something is already waiting on someone.
  return history.filter(c => c.statusLabel === 'landed')
}

export default function Dashboard() {
  const operationsQ = useAttention()
  const operator = useOperator()
  const actQ = useGet<Activity>('/activity', { poll: 5000 })
  const dashQ = useGet<DashboardData>('/dashboard', { poll: 5000 })
  const projectsQ = useGet<ProjectPage>('/projects?limit=5&order=mine', { poll: 5000 })
  // Work rows name a project as the app list does, by its display name.
  const projectNames = Object.fromEntries((projectsQ.data?.projects ?? []).map(p => [p.slug, p.name || p.slug]))
  const act = actQ.data, dash = dashQ.data, dashErr = dashQ.err, projects = projectsQ.data?.projects ?? null
  const listed = projects ?? []
  const finished = homeFinished(act?.history ?? [])

  const ended = dash?.endedOperations ?? []
  const happening = act?.active ?? []
  const waiting = act?.waitingOnYou ?? []
  const releaseApprovals = act?.releaseApprovals ?? []
  const needsYouCount = waiting.length + ended.length + releaseApprovals.length

  // The page leads with labelled counts, then only the
  // sections that have something in them. With no projects yet it says
  // what a project here is for and offers to make one.
  const noProjects = projects !== null && listed.length === 0
  // Conditions only people who can fix them are told about; for everyone
  // else they are not part of Home's counts.
  const status = {
    operations: operator ? attentionState(operationsQ) : { count: 0, known: true, unknown: 0 },
    needsYou: act && dash ? needsYouCount : null,
    running: happening.length,
    deployments: act?.deployments ?? [],
  }
  return (
    <div>
      <SetupCard />
      <StaleNote q={[actQ, dashQ, projectsQ]} />
      <div className="dash-head">
        <div>
          <h1>Home</h1>
        </div>
        {!noProjects && <Link to="/projects/_new"><button className="primary">+ Create project</button></Link>}
      </div>

      {!noProjects && act && projects !== null && (dash || dashErr) && <HomeStatus {...status} />}
      {operator && <><ReadStatus q={operationsQ} what="hosts" /><StaleNote q={operationsQ} /></>}

      {noProjects && (
        <div className="dash-start">
          <Link to="/projects/_new"><button className="primary btn-big">+ Create project</button></Link>
        </div>
      )}


      {!act && <ReadStatus q={actQ} what="activity" />}
      {!dash && <ReadStatus q={dashQ} what="what is waiting on you" />}
      <div id="waiting-on-you" className="dashboard-work-group">{(needsYouCount > 0) && <Section title={ended.length > 0 || releaseApprovals.length > 0 ? "Waiting on you" : "Changes waiting on you"} label="Waiting on you" count={needsYouCount}>
        {releaseApprovals.map(task => <Link key={task.release.id} to={task.href} className="card row dashboard-card"><strong>Release {task.release.number} to {task.deploymentLabel} needs your approval</strong><span>{projectNames[task.project] || task.project}</span></Link>)}
        {ended.map((op) => <EndedOperation key={op.id} op={op} />)}
        <ActiveRunList runs={waiting} showProject names={projectNames} />
      </Section>}</div>
      {!noProjects && <Section title="Recent projects"
        action={<Link to="/projects">All projects{projectsQ.data ? ` (${projectsQ.data.total})` : ''} →</Link>}>
        {!projects && <ReadStatus q={projectsQ} what="the projects" />}
        <div className="project-rows" role="list">
          {listed.map(p => <ProjectListRow key={p.id} project={p} refreshFailed={projectsQ.refreshFailed} reason={projectsQ.reason} />)}
        </div>
      </Section>}

      {!noProjects && <>
      <div id="other-work" className="dashboard-work-group">{happening.length > 0 && <Section title="Other work" count={happening.length}>
        <ActiveRunList runs={happening} showProject names={projectNames} />
      </Section>}</div>

      {/* Number and title, project and when: every row here landed, so a
          status chip would say nothing. */}
      {finished.length > 0 && <Section title="Recently landed" label="Recently landed">
        {finished.slice(0, 5).map(c => (
          <Link key={c.changeId} to={`/changes/${c.changeId}`} className="card row dashboard-card">
            <strong>{c.changeNumber ? <span className="muted dashboard-span">#{c.changeNumber} </span> : null}{c.title}</strong>
            <span className="muted small">{projectNames[c.project ?? ''] ?? c.project} · {relTime(c.when)}</span>
          </Link>
        ))}
      </Section>}
      </>}
    </div>
  )
}

// EndedOperation is an operation you asked for that has ended: how it
// ended, a link to its page, and putting it away.
function EndedOperation({ op }: { op: Operation }) {
  const { run, busy } = useAction(undefined, { affects: ['/dashboard'] })
  return (
    <div className="card row dashboard-card">
      <Status word={op.statusLabel} help={op.detail} />
      <Link to={`/operations/${op.id}`}><strong>{endedWords(op)}</strong></Link>
      {op.detail && <span className="muted small">{op.detail}</span>}
      <span className="grow" />
      <button className="subtle" disabled={busy} onClick={() => run(() => post(`/operations/${op.id}/dismiss`, {}))}>Put away</button>
    </div>
  )
}
