import { ListTable } from '../../ListTable'
import { Section } from '../../Section'
import './list-migrations.css'
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
  const result = (r: typeof rows[number]) => r.status === 'running' ? `${r.projectsDone} of ${r.projectsTotal} apps checked` : [r.status === 'done' ? `${r.findings} confirmed problem${r.findings === 1 ? '' : 's'} at this review` : '', r.errors > 0 ? 'Checks could not run' : r.judged < r.total ? 'Not fully checked' : '', r.flagged > 0 ? `${r.flagged} possible problem${r.flagged === 1 ? '' : 's'} to review` : ''].filter(Boolean).join(' · ')
  return (
    <div>
      <StaleNote q={q} />
      {trend.length > 1 && <Section title="Findings per review" note="Oldest to newest: red findings, amber possible problems, grey none recorded.">
        <div className="review-trend">
          {trend.map((r) => {
            const tot = r.findings + r.flagged
            return (
              <Link key={r.id} to={`/conformance/reviews/${r.id}`} title={`#${r.number} · ${fmtWhen(r.finishedAt || r.startedAt)} · ${r.findings} findings · ${r.flagged} flagged · ${r.judged}/${r.total} judged`}
                className={`review-trend-bar ${r.status === 'error' || r.findings ? 'bad' : r.flagged ? 'warn' : ''}`}
                style={{ height: tot === 0 ? 3 : Math.max(6, (tot / max) * 40) }} />
            )
          })}
        </div>
      </Section>}
      <Section title="Fleet reviews" count={rows.length} note={single > 0 ? `${single} single-project review${single === 1 ? '' : 's'} appear on their project or remediation page.` : undefined}>
        <ListTable label="Fleet reviews" rows={rows.slice(0, limit)} rowKey={r => String(r.id)} rowTo={r => `/conformance/reviews/${r.id}`} columns={[
          { key: 'review', label: 'Review', phone: 'name', render: r => <Link to={`/conformance/reviews/${r.id}`}>Review #{r.number}</Link> },
          { key: 'result', label: 'Result', phone: 'detail', render: r => <span className="muted">{result(r)}</span> },
          { key: 'change', label: 'Since the last', phone: 'extra', render: r => r.delta && r.delta.comparedProjects > 0
            ? <span className="muted" title={`compared over ${r.delta.comparedProjects} projects with the review before it`}><span className={r.delta.fixed ? 'review-fixed' : undefined}>{r.delta.fixed} fixed</span>{' · '}<span className={r.delta.new ? 'review-new' : undefined}>{r.delta.new} new</span></span>
            : <span className="muted">—</span> },
          { key: 'by', label: 'Started by', phone: 'extra', render: r => <span className="muted">{r.actor}</span> },
          // A review that finished with checks that could not run is incomplete, never a green finish.
          { key: 'status', label: 'Status', phone: 'state', render: r => <Status word={r.status === 'done' && r.errors > 0 ? 'incomplete' : r.statusLabel} help={r.status === 'error' ? 'This review could not finish. Open the review for details.' : undefined} /> },
          { key: 'when', label: 'When', phone: 'time', kind: 'time', render: r => fmtWhen(r.finishedAt || r.startedAt) },
        ]} />
      </Section>
      {rows.length > limit && <button onClick={() => setLimit(n => n + 10)}>Show more</button>}
    </div>
  )
}
