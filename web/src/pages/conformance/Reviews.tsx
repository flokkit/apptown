import { PhoneDetails } from '../../GlanceRow'
import { Status } from '../../Status'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { ConformanceReviewList } from '../../wire.gen'
import { fmtWhen } from '../../format'

// --- Reviews ------------------------------------------------------------------------------------

// A fleet review looked at the estate; a single-project review is a
// project page's re-run or a remediation's verification. The Projects tab
// already shows each project's current standing from whichever review
// last touched it, so the Reviews tab is the history of FLEET reviews
// (the single-project ones are mostly noise here).
export function isFleetReview(r: { status: string; projects: number; projectsTotal: number }) {
  // A queued review's projects are counted when it starts.
  return r.status === 'queued' || (r.status === 'running' ? r.projectsTotal > 1 : r.projects > 1)
}

export function ReviewsList() {
  const [limit, setLimit] = useState(10)
  const q = useGet<ConformanceReviewList>('/conformance/reviews')
  const { data } = q
  if (!data) return <ReadStatus q={q} what="the fleet reviews" className="small" />
  const all = data.reviews ?? []
  const rows = all.filter(isFleetReview)
  const single = all.length - rows.length
  if (rows.length === 0) return <><StaleNote q={q} /><div className="muted" style={{ padding: '30px 0', textAlign: 'center' }}>No fleet review yet.{single ? ` ${single} single-project review${single === 1 ? '' : 's'} on file · see each project's page.` : ''}</div></>
  const trend = [...rows].reverse().slice(-40)
  const max = Math.max(1, ...trend.map((r) => r.findings + r.flagged))
  return (
    <div>
      <StaleNote q={q} />
      <PhoneDetails>{trend.length > 1 && (
        <div className="card" style={{ marginBottom: 'var(--space-4)', padding: '10px 14px' }}>
          <div className="muted small mb-2">Findings per fleet review, oldest → newest (red findings, amber AI flags; grey = no recorded problems).</div>
          <div style={{ display: 'flex', gap: 3, alignItems: 'flex-end', height: 40 }}>
            {trend.map((r) => {
              const tot = r.findings + r.flagged
              return (
                <Link key={r.id} to={`/conformance/reviews/${r.id}`} title={`#${r.number} · ${fmtWhen(r.finishedAt || r.startedAt)} · ${r.findings} findings · ${r.flagged} flagged · ${r.judged}/${r.total} judged`}
                  style={{ width: 12, height: tot === 0 ? 3 : Math.max(6, (tot / max) * 40), background: r.status === 'error' || r.findings ? 'var(--bad)' : r.flagged ? 'var(--warn)' : 'var(--border-strong)', borderRadius: 2, display: 'block' }} />
              )
            })}
          </div>
        </div>
      )}
      </PhoneDetails>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--space-2)' }}>
        {rows.slice(0, limit).map((r) => (
          <div key={r.id} className={`card${r.status === 'queued' || r.status === 'running' ? ' tone-busy' : ''}`} style={{ marginBottom: 0, textDecoration: 'none', color: 'inherit', display: 'block' }}>
            <div className="host-row">
              <Link to={`/conformance/reviews/${r.id}`}><strong>{fmtWhen(r.finishedAt || r.startedAt)}</strong></Link>
              <Status word={r.statusLabel} help={r.status === 'error' ? 'This review could not finish. Open the review for details.' : undefined} />
              <span className="small">{r.status === 'running' ? `${r.projectsDone} of ${r.projectsTotal} apps checked` : [r.status === 'done' ? `${r.findings} confirmed problem${r.findings === 1 ? '' : 's'} at this review` : '', r.errors > 0 ? 'Checks could not run' : r.judged < r.total ? 'Not fully checked' : '', r.flagged > 0 ? `${r.flagged} possible problem${r.flagged === 1 ? '' : 's'} to review` : ''].filter(Boolean).join(' · ')}</span>
            </div>
            <details className="small"><summary>Details</summary>Started by {r.actor}.
              {r.delta && r.delta.comparedProjects > 0 && (
                <span className="small muted" title={`compared over ${r.delta.comparedProjects} projects with the review before it`}>
                  <span style={{ color: r.delta.fixed ? 'var(--ok)' : undefined }}>{r.delta.fixed} fixed</span> · <span style={{ color: r.delta.new ? 'var(--bad)' : undefined }}>{r.delta.new} new</span>
                </span>
              )} <Link to={`/conformance/reviews/${r.id}`}>Open review</Link>
            </details>
          </div>
        ))}
      </div>
      {rows.length > limit && <button onClick={() => setLimit(n => n + 10)}>Show more</button>}
      {single > 0 && (
        <div className="muted small mt-3">
          {single} single-project review{single === 1 ? '' : 's'} appear on their project or remediation page.
        </div>
      )}
    </div>
  )
}
