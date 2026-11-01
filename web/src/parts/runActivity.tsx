import { GlanceRow } from '../GlanceRow'
import { Status } from '../Status'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { ChangeState, Doing, Release, RunStatus, post } from '../api'
import { useAction } from '../data'
import { fmtDuration, fmtWhen, relTime } from '../format'
import type { NextAct, ProjectOverviewView } from '../wire.gen'
import { CloseIt } from '../CloseIt'

// Shared presentation pieces for run activity and change history, used by
// the project's Overview tab and the front page.

export type RunRow = {
  // changeId is null, and changeNumber 0, for a run with no change (a
  // scheduled uplift, a system run).
  runId: string | null; runNumber: number; changeId: string | null; changeNumber: number; workflow: string; title: string
  status: RunStatus; statusLabel: import('../wire.gen').DisplayStatus; stage: string; state?: Doing; detail?: string
  project?: string; startedAt: string; durationS: number
  // What the server offers the reader on the row's change (abandon, for a blocked one they may close).
  next: NextAct[]
}

// History is CHANGE history: one row per finished change — what it was,
// which workflows carried it, how it ended, total wall time. The change page
// holds the runs for the drill-in.
export type ChangeRow = {
  changeId: string | null; changeNumber: number; title: string; type: string; state: ChangeState; closedAs?: string; stateLabel: string; statusLabel: import('../wire.gen').DisplayStatus; completedRuns?: number; merged?: boolean
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
            <Status word={r.statusLabel} />
            <strong>{r.changeNumber ? <span className="muted" style={{ fontWeight: 400 }}>#{r.changeNumber} </span> : null}{r.title || `run ${r.runNumber}`}</strong>
            <span className="muted small">{showProject ? `${r.project} · ` : ''}{relTime(r.startedAt)}</span>
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
        const closable = (r.next ?? []).some((a) => a.act === 'abandon')
        const link = (
          <Link key={r.runId} to={`/changes/${r.changeId}`} className={closable ? 'row' : 'card row'} style={{ display: 'flex', flex: closable ? 1 : undefined }}>
            {body}
          </Link>
        )
        if (!closable) return link
        // The row stays a link; closing it is beside it, not inside it.
        return (
          <div key={r.runId} className="card row" style={{ display: 'flex' }}>
            {link}
            <CloseIt changeId={r.changeId} />
          </div>
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
      <thead><tr><th>When</th>{showProject && <th>Project</th>}<th>What happened</th><th>Outcome</th><th>Took</th></tr></thead>
      <tbody>
        {events.map((e) => e.kind === 'change' ? (
          <GlanceRow name={showProject ? 2 : 1} state={showProject ? 3 : 2} fact={0} labels={["When", ...(showProject ? ["Project"] : []), "Change", "State", "Duration"]} key={`c${e.c.changeId}`}>
            <td className="muted small" style={{ whiteSpace: 'nowrap' }}>{fmtWhen(e.when)}</td>
            {showProject && <td className="muted small">{e.c.project}</td>}
            <td className="stack-lead"><Link className="tap-open" to={`/changes/${e.c.changeId}`}><span className="muted small">#{e.c.changeNumber}</span> {e.c.title}</Link></td>
            <td><Status word={e.c.statusLabel} /></td>
            {e.c.statusLabel === 'landed'
              ? <td className="muted small" data-label="took">{fmtDuration(e.c.tookS)}</td>
              : <td className="muted small stack-hide" />}
          </GlanceRow>
        ) : (
          <GlanceRow name={showProject ? 2 : 1} state={showProject ? 3 : 2} fact={0} labels={["When", ...(showProject ? ["Project"] : []), "Change", "State", "Duration"]} key={`r${e.r.number}`} style={{ background: 'var(--ok-bg)' }}>
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
            <td><Status word={e.r.statusLabel} /></td>
            <td className="muted small stack-hide">—</td>
          </GlanceRow>
        ))}
      </tbody>
    </table>
  )
}
