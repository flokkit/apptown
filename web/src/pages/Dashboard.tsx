import { Status } from '../Status'
import { useAttention, attentionState, attentionSummary, useOperator } from '../attention'
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
import { waitingWhy } from '../parts/runActivity'
import { ActiveRunList, ChangeRow, RunRow } from '../parts/runActivity'
import type { Operation, ProjectPage, SetupState } from '../wire.gen'
import { endedWords } from './DataProtection'
import { LocalTrialNote } from '../LocalTrialNote'

// Home opens apps first, then shows work that needs attention.

export type Activity = {
  agents: { project: string; runId: string | null; changeId: string | null; title: string; step: string; type: string; elapsedS: number }[]
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
        <div className="card tone-warn" style={{ marginBottom: 10 }}>
          ⚠ {PRODUCT} is listening on a non-loopback address with open auth · anyone who can
          reach the port has full control. Bind to 127.0.0.1, or put an authenticating proxy
          in front and use trusted-header auth.
        </div>
      )}
      {setup.needsModel && (
        <div className="card tone-accent" style={{ padding: 18, marginBottom: 12 }}>
          <strong style={{ fontSize: 'var(--fs-md)' }}>Add a model API key to start building</strong>
          <div className="muted small" style={{ margin: '4px 0 10px' }}>
            It is kept in {PRODUCT}'s secrets file, is not shown again, and is sent only to steps
            that use that provider.
          </div>
          <div className="row">
            <select value={provider} onChange={(e) => setProvider(e.target.value)}>
              <option value="anthropic">Anthropic</option>
              <option value="openrouter">OpenRouter</option>
            </select>
            <input type="password" value={key} onChange={(e) => setKey(e.target.value)}
              placeholder="paste your API key" style={{ flex: 1, maxWidth: 420 }}
              onKeyDown={(e) => e.key === 'Enter' && key && save()} />
            <button className="primary" disabled={busy || !key} onClick={save}>{busy ? '…' : 'Save'}</button>
          </div>
          {err && <div className="error small" style={{ marginTop: 6 }}>{err}</div>}
          <div style={{ marginTop: 10 }}><LocalTrialNote /></div>
        </div>
      )}
      {setup.agentImage === 'building' && (
        <div className="card" style={{ marginBottom: 10 }} >
          <Status word={"working"} /> Building the agent image (first run only) · a few
          minutes; agents queue until it finishes.
        </div>
      )}
      {setup.agentImage === 'pulling' && (
        <div className="card" style={{ marginBottom: 10 }} >
          <Status word={"working"} /> Downloading the agent image (first run only) · a few
          minutes; agents queue until it finishes.
        </div>
      )}
      {(setup.agentImage === 'failed' || setup.agentImage === 'missing') && (
        <div className="card tone-bad" style={{ marginBottom: 10 }}>
          ✗ The agent image is not available: {setup.agentImageError || 'unknown reason'}
        </div>
      )}
    </>
  )
}

// The sentence at the top of the home page: what needs you, what is
// running, how many apps are live. It counts only what was read: an
// unread or unreadable count is left out or said to be unknown, never
// shown as 0, and "All quiet" needs both needs-you and running to be read
// and empty.
type HomeStatusInput = {
  needsYou: number | null; running: number; deployments: Activity['deployments']; operations: { count: number | null; known: boolean; unknown?: number }
}

export function homeStatus(input: HomeStatusInput): string {
  const s = homeStatusParts(input).map(p => p.text).join(' · ')
  return s.charAt(0).toUpperCase() + s.slice(1)
}

// homeStatusParts is the sentence in parts, each with where it is shown in
// full: what waits on you and what is running are the sections below, the
// conditions to fix are Attention, and running apps are the Apps list. A
// part links only to a page that lists exactly what it counts.
export function homeStatusParts({ needsYou, running, deployments, operations }: HomeStatusInput): { text: string; to?: string }[] {
  const parts: { text: string; to?: string }[] = []
  if (needsYou === null) parts.push({ text: 'could not read what is waiting on you' })
  else if (needsYou > 0) parts.push({ text: `${needsYou} waiting on you` })
  if (running > 0) parts.push({ text: `${running} running` })
  if (operations.count || operations.unknown) parts.push({ text: attentionSummary(operations.count, operations.unknown ?? 0), to: '/attention' })
  if (needsYou === 0 && running === 0 && operations.known && operations.count === 0) parts.unshift({ text: 'All quiet' })
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
  // The projects that are running, in the list's own word: projects.
  if (live.size > 0) parts.push({ text: `${live.size} running`, to: '/deployments/table' })
  if (unknown.size > 0) parts.push({ text: `${unknown.size} running status unknown`, to: '/deployments/table' })
  return parts
}

// homeSections splits the active runs and pending approvals into what
// needs a person (approvals, blocked runs) and what is running. needsYouCount
// is everything under "Needs you", the heading and the sentence alike: each
// change once (two approvals on one change are one change), and each ended
// operation that waits on the person.
export function homeSections<A extends { changeId: string | null }>(active: RunRow[], approvals: A[], ended: unknown[] = []) {
  const happening = active.filter((r) => r.state !== 'waiting on you' && r.state !== 'blocked')
  const blocked = active.filter((r) => r.state === 'blocked')
  // A change has one card: a working run's (possibly stale) approval waits
  // behind the run, and a blocked run's approval behind the block, which is
  // what has to be dealt with first.
  const runChanges = new Set([...happening, ...blocked].map((r) => r.changeId))
  const waiting = approvals.filter((a) => !runChanges.has(a.changeId))
  const needsYouCount = new Set([
    ...waiting.map((a) => `c${a.changeId}`),
    ...blocked.map((r) => r.changeId ? `c${r.changeId}` : `r${r.runId}`),
  ]).size + ended.length
  return { happening, blocked, waiting, needsYouCount }
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

  // One work item, one card. "Running" is one card PER RUN (its current
  // step is shown inside the card), never one per running step — a run churning
  // through auto-rework used to surface once per in-flight step here AND again
  // as a needs-you card. A run that is actively working shows here; suppress its
  // (possibly stale, mid-rework) pending approval from "Needs you" so the
  // same item never appears in two sections at once.
  //
  // A blocked run is not running: it waits for a person to fix, send back,
  // override or abandon it, so it is listed under "Needs you" with the
  // approvals, and counted there.
  // A copy, check or take-down you asked for that failed, or whose outcome
  // is not known, waits here until you put it away. A success needs nobody.
  const ended = dash?.endedOperations ?? []
  const { happening, blocked, waiting, needsYouCount } = homeSections(act?.active ?? [], dash?.pendingApprovals ?? [], ended)

  // The page leads with one sentence about the situation, then only the
  // sections that have something in them. With no projects yet it says
  // what a project here is for and offers to make one.
  const noProjects = projects !== null && listed.length === 0
  // Conditions only people who can fix them are told about; for everyone
  // else they are not part of Home's sentence.
  const status = homeStatusParts({
    operations: operator ? attentionState(operationsQ) : { count: 0, known: true, unknown: 0 },
    needsYou: dash ? needsYouCount : null,
    running: happening.length,
    deployments: act?.deployments ?? [],
  })
  return (
    <div>
      <SetupCard />
      <StaleNote q={[actQ, dashQ, projectsQ]} />
      <div className="dash-head">
        <div>
          <h1>Home</h1>
        </div>
        {!noProjects && <Link to="/projects/_new"><button className="primary">+ New project</button></Link>}
      </div>

      {!noProjects && act && projects !== null && (dash || dashErr) && <div className="dash-lead">{status.map((p, i) => {
        const text = i === 0 ? p.text.charAt(0).toUpperCase() + p.text.slice(1) : p.text
        return <span key={p.text}>{i > 0 && ' · '}{p.to ? <Link to={p.to}>{text} →</Link> : text}</span>
      })}</div>}
      {operator && <><ReadStatus q={operationsQ} what="hosts" /><StaleNote q={operationsQ} /></>}

      {noProjects && (
        <div className="dash-start">
          <Link to="/projects/_new"><button className="primary btn-big">+ New project</button></Link>
        </div>
      )}


      {!act && <ReadStatus q={actQ} what="activity" />}
      {!dash && <ReadStatus q={dashQ} what="what is waiting on you" />}
      {(waiting.length > 0 || blocked.length > 0 || ended.length > 0) && <Section title={ended.length > 0 ? "Waiting on you" : "Changes waiting on you"} label="Waiting on you" count={needsYouCount}>
        {ended.map((op) => <EndedOperation key={op.id} op={op} />)}
        <ActiveRunList runs={blocked} showProject names={projectNames} why />
        {waiting.map((a) => (
          <div key={a.stepId} className="card row" style={{ display: 'flex' }}>
            <Link to={`/changes/${a.changeId}`} className="row" style={{ display: 'flex', flex: 1 }}>
              <span className="row-status"><Status word={a.statusLabel} reason={waitingWhy(a.statusLabel)} /></span>
              <strong>{a.changeNumber ? <span className="muted" style={{ fontWeight: 400 }}>#{a.changeNumber} </span> : null}{a.itemTitle || `change ${a.changeNumber}`}</strong>
              <span className="muted small">{a.projectName}{act?.active.find(r => r.changeId === a.changeId)?.startedAt && ` · ${relTime(act?.active.find(r => r.changeId === a.changeId)!.startedAt)}`}</span>
            </Link>
            <span className="row-action"><Link to={`/changes/${a.changeId}`} className="btn">Review</Link></span>
          </div>
        ))}
      </Section>}
      {!noProjects && <Section title="Recent projects"
        action={<Link to="/projects">All projects{projectsQ.data ? ` (${projectsQ.data.total})` : ''} →</Link>}>
        {!projects && <ReadStatus q={projectsQ} what="the projects" />}
        <div className="project-rows" role="list">
          {listed.map(p => <ProjectListRow key={p.id} project={p} refreshFailed={projectsQ.refreshFailed} reason={projectsQ.reason} />)}
        </div>
      </Section>}

      {!noProjects && <>
      {happening.length > 0 && <Section title="Working" count={happening.length}>
        <ActiveRunList runs={happening} showProject names={projectNames} />
      </Section>}

      {/* Number and title, project and when: every row here landed, so a
          status chip would say nothing. */}
      {finished.length > 0 && <Section title="Recently landed" label="Recently landed">
        {finished.slice(0, 5).map(c => (
          <Link key={c.changeId} to={`/changes/${c.changeId}`} className="card row" style={{ display: 'flex' }}>
            <strong>{c.changeNumber ? <span className="muted" style={{ fontWeight: 400 }}>#{c.changeNumber} </span> : null}{c.title}</strong>
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
    <div className="card row" style={{ display: 'flex' }}>
      <Status word={op.statusLabel} help={op.detail} />
      <Link to={`/operations/${op.id}`}><strong>{endedWords(op)}</strong></Link>
      {op.detail && <span className="muted small">{op.detail}</span>}
      <span style={{ flex: 1 }} />
      <button className="subtle" disabled={busy} onClick={() => run(() => post(`/operations/${op.id}/dismiss`, {}))}>Put away</button>
    </div>
  )
}
