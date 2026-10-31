import { GlanceRow } from '../../GlanceRow'
import { Status, STATUS_LIGHT } from '../../Status'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { relTime } from '../../format'
import type { RecentWhatIfs } from '../../wire.gen'
import { OUTCOME_ORDER, OUTCOME_WORDS } from '../WhatIfPage'

// WhatIfsList is the recent what-ifs, newest first: which pack version, who
// asked and when, how far it got and what it answered per project, and how
// many projects it moves rules back on. Each opens its own page, where
// adopting from it happens.
export function WhatIfsList() {
  const [limit, setLimit] = useState(10)
  const q = useGet<RecentWhatIfs>('/conformance/what-ifs', { poll: 5000 })
  if (!q.data) return <ReadStatus q={q} what="the what-ifs" />
  const list = q.data.whatIfs
  return (
    <div>
      <StaleNote q={q} />
      <details className="small"><summary>About what-ifs</summary><p className="muted small" style={{ maxWidth: 720 }}>
        A what-if asks what taking a pack version would change on each project, without changing anything. Start one from a <Link to="/packs">pack</Link>'s version.
      </p></details>
      {list.length === 0 && <div className="muted">No what-ifs yet.</div>}
      {list.length > 0 && (
        <div style={{ overflowX: 'auto' }}>
          <table className="small stack" style={{ width: '100%' }}>
            <thead><tr><th>Pack version</th><th>Asked</th><th>State</th><th>Answers</th></tr></thead>
            <tbody>
              {list.slice(0, limit).map((w) => {
                const counts: Record<string, number> = {}
                for (const p of w.projects) counts[p.outcome] = (counts[p.outcome] ?? 0) + 1
                return (
                  <GlanceRow name={0} state={2} summary={<>{w.projects.length} projects</>} labels={["Pack", "Asked", "State", "Answers"]} key={w.id}>
                    <td><Link to={`/conformance/what-ifs/${w.id}`}>{w.pack} v{w.version}</Link>{w.advanceWorkflows && <details><summary>Other changes</summary>and other workflows to newest</details>}</td>
                    <td>{w.requestedBy}<div className="muted">{relTime(w.createdAt)}</div></td>
                    <td><Status word={w.statusLabel} tone={STATUS_LIGHT[w.statusLabel]} help={w.detail || w.state} /></td>
                    <td>
                      <details><summary>{w.projects.length} apps · Results</summary><div className="row" style={{ flexWrap: 'wrap', gap: 4 }}>
                        {OUTCOME_ORDER.filter((o) => counts[o]).map((o) => <span key={o}>{counts[o]} <Status word={w.outcomeLabels[o]} tone={STATUS_LIGHT[w.outcomeLabels[o]]} help={OUTCOME_WORDS[o]} /></span>)}
                        {w.movesBackProjects > 0 && <span className="chip moves-back">{w.movesBackProjects} move rules back</span>}
                      </div></details>
                    </td>
                  </GlanceRow>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      {list.length > limit && <button onClick={() => setLimit(n => n + 10)}>Show more</button>}
    </div>
  )
}
