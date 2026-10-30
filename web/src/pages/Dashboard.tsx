import { Status, STATUS_LIGHT } from '../Status'
import { useAttention, attentionState } from '../attention'
import { PRODUCT } from '../brand'
import { AppAddress, RuntimeObservation, RuntimeStatus } from '../runtime'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Dashboard as DashboardData, post } from '../api'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { relTime } from '../format'
import { ActiveRunList, ChangeHistoryTable, ChangeRow, RunRow } from '../parts/runActivity'
import type { DeploymentView, Operation, ProjectRow, SetupState } from '../wire.gen'
import { endedWords } from './DataProtection'
import { LocalTrialNote } from '../LocalTrialNote'

// Home (the glossary's front page of the instance): what is my whole AppTown
// doing right now — what needs me, what's running, what shipped lately. Folding this into the projects
// page buried it (people did not notice "happening now" was there), so
// it is its own page again, first in the menu.

type ProjRow = ProjectRow
export type Activity = {
  agents: { project: string; runId: string | null; changeId: string | null; title: string; step: string; type: string; elapsedS: number }[]
  active: RunRow[]
  history: ChangeRow[]
  deployments: (RuntimeObservation & { project: string; name: string; url: string; hostOnly?: boolean; running: boolean | null })[]
}
type Setup = SetupState

const rel = (ts: string): string => relTime(ts) || 'nothing shipped yet'

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
          <Status word={"working"} tone={STATUS_LIGHT["working"]} /> Building the agent image (first run only) · a few
          minutes; agents queue until it finishes.
        </div>
      )}
      {setup.agentImage === 'pulling' && (
        <div className="card" style={{ marginBottom: 10 }} >
          <Status word={"working"} tone={STATUS_LIGHT["working"]} /> Downloading the agent image (first run only) · a few
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
export function homeStatus({ needsYou, running, deployments, operations }: {
  needsYou: number | null; running: number; deployments: Activity['deployments']; operations: { count: number | null; known: boolean }
}): string {
  const parts: string[] = []
  if (needsYou === null) parts.push('could not read what needs you')
  else if (needsYou > 0) parts.push(`${needsYou} needs you`)
  if (running > 0) parts.push(`${running} running`)
  if (operations.count) parts.push(`${operations.count} host issues`)
  else if (!operations.known) parts.push('host status unknown')
  if (needsYou === 0 && running === 0 && operations.known && operations.count === 0) parts.unshift('All quiet')
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
  if (live.size > 0) parts.push(`${live.size} ${live.size === 1 ? 'app' : 'apps'} live`)
  if (unknown.size > 0) parts.push(`${unknown.size} ${unknown.size === 1 ? 'app' : 'apps'} status unknown`)
  const s = parts.join(' · ')
  return s.charAt(0).toUpperCase() + s.slice(1)
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
  return history.filter(c => c.statusLabel === 'landed' || c.statusLabel === 'failed')
}

export default function Dashboard() {
  const operationsQ = useAttention()
  const actQ = useGet<Activity>('/activity', { poll: 5000 })
  const dashQ = useGet<DashboardData>('/dashboard', { poll: 5000 })
  const projectsQ = useGet<ProjRow[]>('/projects?recent=6', { poll: 5000 })
  const act = actQ.data, dash = dashQ.data, dashErr = dashQ.err, projects = projectsQ.data
  const recent = projects ?? []
  const finished = homeFinished(act?.history ?? [])

  if (!act) return <><h1>Home</h1><ReadStatus q={actQ} what="activity" /></>

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
  const { happening, blocked, waiting, needsYouCount } = homeSections(act.active, dash?.pendingApprovals ?? [], ended)

  // The page leads with one sentence about the situation, then only the
  // sections that have something in them. With no projects yet it says
  // what a project here is for and offers to make one.
  const noProjects = projects !== null && recent.length === 0
  const status = homeStatus({
    operations: attentionState(operationsQ),
    needsYou: dash ? needsYouCount : null,
    running: happening.length,
    deployments: act.deployments,
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

      {!noProjects && projects !== null && (dash || dashErr) && <div className="dash-lead"><Link to="/attention">{status} →</Link></div>}
      <ReadStatus q={operationsQ} what="hosts" /><StaleNote q={operationsQ} />

      {noProjects && (
        <div className="dash-start">
          <Link to="/projects/_new"><button className="primary btn-big">+ New project</button></Link>
        </div>
      )}

      {!dash && <ReadStatus q={dashQ} what="what needs you" />}
      {(waiting.length > 0 || blocked.length > 0 || ended.length > 0) && <>
        <h2 style={{ marginTop: 8 }}>Needs you ({needsYouCount})</h2>
        {ended.map((op) => <EndedOperation key={op.id} op={op} />)}
        <ActiveRunList runs={blocked} showProject />
        {waiting.map((a) => (
          <Link key={a.stepId} to={`/changes/${a.changeId}`} className="card row tone-warn" style={{ display: 'flex' }}>
            <Status word={a.statusLabel} tone={STATUS_LIGHT[a.statusLabel]} help="Waiting for approval" />
            <strong>{a.itemTitle || `change ${a.changeNumber}`}</strong>
            <span className="muted small">{a.projectName}{act.active.find(r => r.changeId === a.changeId)?.startedAt && ` · ${relTime(act.active.find(r => r.changeId === a.changeId)!.startedAt)}`}</span>
            <span style={{ flex: 1 }} />
            <span className="small">Review →</span>
          </Link>
        ))}
      </>}

      {!noProjects && <>
      {happening.length > 0 && <>
        <h2>Working ({happening.length})</h2>
        <ActiveRunList runs={happening} showProject />
      </>}

      <h2>Projects</h2>
      {!projects && <ReadStatus q={projectsQ} what="the recent projects" />}
      <div className="dash-projects">
        {recent.map((p) => {
          return (
            <div key={p.id} className="card">
              <Link to={`/projects/${p.slug}`} className="row tap-open" style={{ textDecoration: 'none', color: 'inherit' }}>
                {p.icon && <span aria-hidden>{p.icon}</span>}
                <strong>{p.name}</strong>
              </Link>
              <div className="muted small">{p.lastRunAt ? rel(p.lastRunAt) : 'nothing shipped yet'}</div>
              <HomeDeployments slug={p.slug} />
            </div>
          )
        })}
      </div>
      <div className="muted small" style={{ marginTop: 6 }}>
        <Link to="/projects">All projects →</Link> · <Link to="/deployments">Deployments →</Link>
      </div>

      {finished.length > 0 && <>
        <h2>Recently finished</h2>
        <ChangeHistoryTable changes={finished} showProject />
      </>}
      </>}
    </div>
  )
}

// EndedOperation is an operation you asked for that has ended: how it
// ended, a link to its page, and putting it away.
function EndedOperation({ op }: { op: Operation }) {
  const { run, busy } = useAction(undefined, { affects: ['/dashboard'] })
  return (
    <div className="card row tone-warn" style={{ display: 'flex' }}>
      <Status word={op.statusLabel} tone={STATUS_LIGHT[op.statusLabel]} help={op.detail} />
      <Link to={`/operations/${op.id}`}><strong>{endedWords(op)}</strong></Link>
      {op.detail && <span className="muted small">{op.detail}</span>}
      <span style={{ flex: 1 }} />
      <button className="subtle" disabled={busy} onClick={() => run(() => post(`/operations/${op.id}/dismiss`, {}))}>Put away</button>
    </div>
  )
}

function HomeDeployments({ slug }: { slug: string }) {
  const q = useGet<DeploymentView[]>(`/projects/${slug}/deployments`, { poll: 5000 })
  return <div className="row" style={{ marginTop: 8, gap: 6 }}>
    <ReadStatus q={q} what="deployments" /><StaleNote q={q} />
    {q.data?.map(d => <span key={d.id} className="row" style={{ gap: 4 }}>
      <AppAddress className="dash-app" url={d.url} hostOnly={d.hostOnly}>{d.displayLabel}</AppAddress>
      <RuntimeStatus observation={q.refreshFailed ? { ...d, observation: 'unknown', observationStale: true, observationIssue: q.reason } : d} />
    </span>)}
  </div>
}
