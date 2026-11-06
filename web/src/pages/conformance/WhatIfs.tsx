import { Section } from '../../Section'
import { GlanceRow } from '../../GlanceRow'
import { Status } from '../../Status'
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
      <Section title="What-ifs" count={list.length || undefined} action={<Link to="/packs">Packs →</Link>}>
      {list.length === 0 && <p className="muted small">No what-ifs yet. Start one from a pack's version.</p>}
      {list.length > 0 && (
        <div style={{ overflowX: 'auto' }}>
          <table className="small stack" style={{ width: '100%' }}>
            <thead><tr><th>Pack version</th><th>Asked</th><th>State</th><th>Answers</th></tr></thead>
            <tbody>
              {list.slice(0, limit).map((w) => {
                const counts: Record<string, number> = {}
                for (const p of w.projects) counts[p.outcome] = (counts[p.outcome] ?? 0) + 1
                return (
                  <GlanceRow labels={["Pack", "Asked", "State", "Answers"]} key={w.id}>
                    <td><Link to={`/conformance/what-ifs/${w.id}`}>{w.pack} v{w.version}</Link>{w.advanceWorkflows && <section><h3>Other changes</h3>and other workflows to newest</section>}</td>
                    <td>{w.requestedBy}<div className="muted">{relTime(w.createdAt)}</div></td>
                    <td><Status word={w.statusLabel} help={w.detail || undefined} /></td>
                    <td>
                      <section><h3>{w.projects.length} apps · Results</h3><div className="row" style={{ flexWrap: 'wrap', gap: 4 }}>
                        {OUTCOME_ORDER.filter((o) => counts[o]).map((o) => <span key={o}>{counts[o]} <Status word={w.outcomeLabels[o]} help={OUTCOME_WORDS[o]} /></span>)}
                        {w.movesBackProjects > 0 && <span className="badge moves-back">{w.movesBackProjects} move rules back</span>}
                      </div></section>
                    </td>
                  </GlanceRow>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      </Section>
      {list.length > limit && <button style={{ marginTop: 10 }} onClick={() => setLimit(n => n + 10)}>Show more</button>}
    </div>
  )
}
