import { ListRow } from '../ListRow'
import './runActivity.css'
import { ChangeStatus } from '../Status'
import { WaitingStatus, hasDetails } from '../Approvers'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { ChangeState, Doing, RunStatus, post } from '../api'
import { useAction } from '../data'
import { relTime } from '../format'
import type { NextAct, ProjectOverviewView } from '../wire.gen'

// Shared presentation pieces for run activity and change history, used by
// the project's Overview tab and the front page.

export type RunRow = {
  // changeId is null, and changeNumber 0, for a run with no change (a
  // scheduled uplift, a system run).
  runId: string | null; runNumber: number; changeId: string | null; changeNumber: number; workflow: string; title: string
  status: RunStatus; stage: string; state?: Doing; detail?: string
  project?: string; startedAt: string; durationS: number
  // What the server offers the reader on the row's change (abandon, for a blocked one they may close).
  next: NextAct[]
  waitingOn: import('../wire.gen').WaitingOn
}

// History is CHANGE history: one row per finished change — what it was,
// which workflows carried it, how it ended, total wall time. The change page
// holds the runs for the drill-in.
export type ChangeRow = {
  changeId: string | null; changeNumber: number; title: string; type: string; state: ChangeState; closedAs?: string; stateLabel: string; completedRuns?: number; merged?: boolean
  waitingOn: import('../wire.gen').WaitingOn
  project?: string; workflows: string; runs: number; when: string; tookS: number
}

export type OpenChange = { changeId: string | null; changeNumber: number; title: string; state: ChangeState; editing: number }

// The server's overview, with the run rows' state read as the Doing union.
export type OverviewData = Omit<ProjectOverviewView, 'active'> & { active: RunRow[] }

export function ActiveRunList({ runs, showProject, names = {} }: { runs: RunRow[]; showProject?: boolean; names?: Record<string, string> }) {
  if (runs.length === 0) return null
  return (
    <div>
      {runs.map((r) => {
        // An approval's status opens who can approve, so it sits outside
        // the row's link.
        // A status that opens its details (who can approve, what went wrong)
        // sits outside the row's link.
        const approval = hasDetails(r.waitingOn) && !!r.changeId
        const status = approval ? <WaitingStatus waiting={r.waitingOn} changeId={r.changeId} /> : <ChangeStatus waiting={r.waitingOn} />
        const title = <strong>{r.changeNumber ? <span className="muted">#{r.changeNumber} </span> : null}{r.title || `run ${r.runNumber}`}</strong>
        const meta = <span className="muted small">{showProject ? `${names[r.project ?? ''] ?? r.project} · ` : ''}{relTime(r.startedAt)}</span>
        // A run with no change (a scheduled uplift, a system run) has no
        // page of its own — its one door is here: stop it.
        if (!r.changeId) {
          return <ListRow key={r.runId} className="card row run-list-row" state={<span className="row-status">{status}</span>} identity={<>{title}{meta}</>} action={<StopRun runId={r.runId!} />} />
        }
        // The row opens the change; the title stays a real link. A stuck
        // change also offers its next step, the same button every list row
        // uses; closing it is a considered act on the change's own page. A
        // failed change always has a next step, whether or not this list was
        // sent the change's offered acts.
        const stuck = approval || r.waitingOn.kind === 'check' || r.waitingOn.you === 'yes' || (r.next ?? []).some((a) => a.act === 'abandon')
        const to = `/changes/${r.changeId}`
        return (
          <ListRow key={r.runId} to={to} className="card row run-list-row" state={<span className="row-status">{status}</span>}
            identity={<><Link to={to} className="run-link">{title}</Link>{meta}</>}
            action={stuck && <span className="row-action"><Link to={to} className="btn">{r.waitingOn.kind === 'check' && r.waitingOn.you === 'yes' ? 'Fix it' : r.waitingOn.kind === 'approval' && r.waitingOn.you === 'yes' ? 'Review' : 'Open'}</Link></span>} />
        )
      })}
    </div>
  )
}

function StopRun({ runId }: { runId: string }) {
  const [ask, setAsk] = useState(false)
  const { run, busy, err } = useAction(() => setAsk(false))
  if (!ask) return <button className="subtle" onClick={() => setAsk(true)}>stop</button>
  return (
    <span className="row run-stop">
      <span className="small">Stop this run? Nothing is deleted.</span>
      <button className="danger" disabled={busy} onClick={() => void run(() => post(`/runs/${runId}/abandon`, { reason: 'stopped from the web UI' }))}>{busy ? '…' : 'Stop run'}</button>
      <button className="subtle" onClick={() => setAsk(false)}>Cancel</button>
      {err && <span className="error small">{err}</span>}
    </span>
  )
}


