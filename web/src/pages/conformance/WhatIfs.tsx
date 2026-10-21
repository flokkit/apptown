import { Link } from 'react-router-dom'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { relTime } from '../../format'
import type { RecentWhatIfs } from '../../wire.gen'
import { OUTCOME_ORDER, OUTCOME_TONE, OUTCOME_WORDS } from '../WhatIfPage'

// WhatIfsList is the recent what-ifs, newest first: which pack version, who
// asked and when, how far it got and what it answered per project, and how
// many projects it moves rules back on. Each opens its own page, where
// adopting from it happens.
export function WhatIfsList() {
  const q = useGet<RecentWhatIfs>('/conformance/what-ifs', { poll: 5000 })
  if (!q.data) return <ReadStatus q={q} what="the what-ifs" />
  const list = q.data.whatIfs
  return (
    <div>
      <StaleNote q={q} />
      <p className="muted small" style={{ maxWidth: 720 }}>
        A what-if asks what taking a pack version would change on each project, without changing anything. Start one from a <Link to="/packs">pack</Link>'s version.
      </p>
      {list.length === 0 && <div className="muted">No what-ifs yet.</div>}
      {list.length > 0 && (
        <div style={{ overflowX: 'auto' }}>
          <table className="small" style={{ width: '100%' }}>
            <thead><tr><th>Pack version</th><th>Asked</th><th>State</th><th>Answers</th></tr></thead>
            <tbody>
              {list.map((w) => {
                const counts: Record<string, number> = {}
                for (const p of w.projects) counts[p.outcome] = (counts[p.outcome] ?? 0) + 1
                return (
                  <tr key={w.id}>
                    <td><Link to={`/conformance/what-ifs/${w.id}`}>{w.pack} v{w.version}</Link>{w.advanceWorkflows && <div className="muted">and other workflows to newest</div>}</td>
                    <td>{w.requestedBy}<div className="muted">{relTime(w.createdAt)}</div></td>
                    <td>{w.state === 'running' ? `running · ${w.projectsDone} of ${w.projectsTotal}` : w.state}</td>
                    <td>
                      <div className="row" style={{ flexWrap: 'wrap', gap: 4 }}>
                        {OUTCOME_ORDER.filter((o) => counts[o]).map((o) => <span key={o} className={`chip ${OUTCOME_TONE[o]}`}>{counts[o]} {OUTCOME_WORDS[o]}</span>)}
                        {w.movesBackProjects > 0 && <span className="chip moves-back">{w.movesBackProjects} move rules back</span>}
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
