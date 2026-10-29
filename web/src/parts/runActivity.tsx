import { useState } from 'react'
import { Link } from 'react-router-dom'
import { ChangeState, Doing, Release, RunStatus, post } from '../api'
import { chipFor } from '../states'
import { useAction } from '../data'
import { count, fmtDuration, fmtWhen } from '../format'
import type { ProjectOverviewView } from '../wire.gen'

// Shared presentation pieces for run activity and change history, used by
// the project's Overview tab and the front page.

export type RunRow = {
  // changeId is null, and changeNumber 0, for a run with no change (a
  // scheduled uplift, a system run).
  runId: string | null; runNumber: number; changeId: string | null; changeNumber: number; workflow: string; title: string
  status: RunStatus; stage: string; state?: Doing; detail?: string
  project?: string; startedAt: string; durationS: number
}

// History is CHANGE history: one row per finished change — what it was,
// which workflows carried it, how it ended, total wall time. The change page
// holds the runs for the drill-in.
export type ChangeRow = {
  changeId: string | null; changeNumber: number; title: string; type: string; state: ChangeState; closedAs?: string; stateLabel: string; completedRuns?: number; merged?: boolean
  project?: string; workflows: string; runs: number; when: string; tookS: number
}

export type OpenChange = { changeId: string | null; changeNumber: number; title: string; state: ChangeState; editing: number }

// The server's overview, with the run rows' state read as the Doing union.
export type OverviewData = Omit<ProjectOverviewView, 'active'> & { active: RunRow[] }

export function ActiveRunList({ runs, showProject }: { runs: RunRow[]; showProject?: boolean }) {
  if (runs.length === 0) return null
  return (
    <div>
      {runs.map((r) => {
        const body = (
          <>
            {r.state
              ? <span className={`chip ${chipFor(r.state)}`}>{r.state}{r.detail ? `: ${r.detail}` : ''}</span>
              : <span className="chip running">in progress</span>}
            <strong>{r.changeNumber ? <span className="muted" style={{ fontWeight: 400 }}>#{r.changeNumber} </span> : null}{r.title || `run ${r.runNumber}`}</strong>
            <span className="muted small">{showProject ? `${r.project} · ` : ''}{r.workflow} · {r.stage} · {fmtDuration(r.durationS)}</span>
          </>
        )
        // A run with no change (a scheduled uplift, a system run) has no
        // page of its own — its one door is here: stop it.
        if (!r.changeId) {
          return (
            <div key={r.runId} className="card row" style={{ display: 'flex' }}>
              {body}
              <span style={{ flex: 1 }} />
              <StopRun runId={r.runId!} />
            </div>
          )
        }
        return (
          <Link key={r.runId} to={`/changes/${r.changeId}`} className="card row" style={{ display: 'flex' }}>
            {body}
          </Link>
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
    <span className="row" style={{ gap: 6 }}>
      <span className="small">Stop this run? Nothing is deleted.</span>
      <button className="danger" disabled={busy} onClick={() => void run(() => post(`/runs/${runId}/abandon`, { reason: 'stopped from the web UI' }))}>{busy ? '…' : 'Stop run'}</button>
      <button className="subtle" onClick={() => setAsk(false)}>Cancel</button>
      {err && <span className="error small">{err}</span>}
    </span>
  )
}


// History interleaves BOTH event kinds: changes are the work, releases
// are the work reaching people — one timeline, each release row a permalink.
export function ChangeHistoryTable({ changes, showProject, releases, slug }: {
  changes: ChangeRow[]; showProject?: boolean; releases?: Release[]; slug?: string
}) {
  type Ev = { kind: 'change'; when: string; c: ChangeRow } | { kind: 'release'; when: string; r: Release }
  const events: Ev[] = [
    ...changes.map((c): Ev => ({ kind: 'change', when: c.when, c })),
    ...(slug ? (releases ?? []).map((r): Ev => ({ kind: 'release', when: r.releasedAt || r.createdAt, r })) : []),
  ].sort((a, b) => (a.when < b.when ? 1 : -1))
  if (events.length === 0) return <div className="muted small">No finished changes yet.</div>
  return (
    <table className="stack">
      <thead><tr><th>When</th>{showProject && <th>Project</th>}<th>What happened</th><th>How it went</th><th>Outcome</th><th>Took</th></tr></thead>
      <tbody>
        {events.map((e) => e.kind === 'change' ? (
          <tr key={`c${e.c.changeId}`}>
            <td className="muted small" style={{ whiteSpace: 'nowrap' }}>{fmtWhen(e.when)}</td>
            {showProject && <td className="muted small">{e.c.project}</td>}
            <td className="stack-lead"><Link className="tap-open" to={`/changes/${e.c.changeId}`}><span className="muted small">#{e.c.changeNumber}</span> {e.c.title}</Link></td>
            <td className="muted small"><span title={e.c.workflows || undefined}>{e.c.completedRuns === undefined ? count(e.c.runs, 'run') : count(e.c.completedRuns, 'completed run')}{e.c.completedRuns !== undefined && e.c.runs > e.c.completedRuns && ` · ${e.c.runs - e.c.completedRuns} unfinished`}</span></td>
            <td><span className={`chip ${e.c.state === 'shipped' ? 'pass' : ''}`}>{e.c.stateLabel}</span></td>
            <td className="muted small" data-label="took">{fmtDuration(e.c.tookS)}</td>
          </tr>
        ) : (
          <tr key={`r${e.r.number}`} style={{ background: 'var(--ok-bg)' }}>
            <td className="muted small" style={{ whiteSpace: 'nowrap' }}>{fmtWhen(e.when)}</td>
            {showProject && <td className="muted small" />}
            <td className="stack-lead">
              <Link className="tap-open" to={`/projects/${slug}/releases/${e.r.number}`}>
                {e.r.deployment === 'lan' ? '🏢 ' : '📦 '}Release #{e.r.number}
              </Link>
              {(e.r.changes ?? []).length > 0 && (
                <span className="muted small"> · {(e.r.changes ?? []).length} change{(e.r.changes ?? []).length === 1 ? '' : 's'}</span>
              )}
            </td>
            <td className="muted small">{e.r.deployment === 'lan' ? 'released to the intranet' : `released to ${e.r.deployment}`}{e.r.approvedBy ? ` by ${e.r.approvedBy}` : ''}</td>
            <td><span className={`chip ${e.r.state === 'released' ? 'pass' : e.r.state === 'failed' ? 'fail' : 'running'}`}>{e.r.state}</span></td>
            <td className="muted small stack-hide">—</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}
