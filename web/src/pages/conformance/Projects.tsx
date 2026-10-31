import { useNarrow } from '../../chrome'
import { Status, STATUS_LIGHT } from '../../Status'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import type { ConformanceView, ProjectStanding } from '../../wire.gen'
import { relTime } from '../../format'
import { StandingChip, LaneBar, laneOf } from './parts'

// --- Projects (the front page) ---------------------------------------------------------------

export function projectFact(p: ProjectStanding, view: ConformanceView): string {
  const observations = view.byProject[p.slug] ?? []
  const lane = (v: typeof observations[number]) => laneOf(v, view.adjudications ?? {})
  const problem = observations.find(v => lane(v) === 'findings')
    ?? observations.find(v => lane(v) === 'broken')
    ?? observations.find(v => lane(v) === 'flagged')
  if (problem) {
    const kind = lane(problem)
    const definition = Object.values(view.checkDefs ?? {}).find(d => d.block === problem.block && d.version === problem.version && d.check === problem.check)
    if (definition?.title) return `${kind === 'findings' ? 'Fix' : kind === 'flagged' ? 'Review' : 'Check could not run'}: ${definition.title}`
    if (kind === 'flagged') return 'Review the possible problems.'
    if (kind === 'broken') return 'A check could not run. Open for details.'
    if (problem.detail && problem.detail.length <= 100) return problem.detail
    return 'Open the failed check to see what needs fixing.'
  }
  if (p.lanes.unassessed || p.lanes.broken) return 'Some checks have no current result.'
  if (p.lanes.flagged) return 'Review the possible problems.'
  if (!p.total) return 'No checks recorded.'
  return p.standing === 'conforms-with-exceptions' ? 'Exceptions are approved.' : 'Checks are up to date.'
}

function ProjectRow({ p, fact, base, onCheck }: { p: ProjectStanding; fact: string; base: string; onCheck?: (slug: string) => void }) {
  const narrow = useNarrow()
  if (narrow) return <div className="card"><Link to={`${base}/projects/${p.slug}`} style={{ display: 'block', color: 'inherit', textDecoration: 'none' }}>
    <div className="host-row"><strong>{p.name || p.slug}</strong><StandingChip label={p.statusLabel} help={p.sentence} /><span className="muted small">{fact}</span></div>
  </Link>{onCheck && p.standing === 'conforms-so-far' && <details><summary>Actions</summary><button onClick={() => onCheck(p.slug)}>Check now</button></details>}</div>
  return (
    <div className="card" style={{ marginBottom: 0, padding: '10px 14px', textDecoration: 'none', color: 'inherit', display: 'block' }}>
      <Link to={`${base}/projects/${p.slug}`} style={{ color: 'inherit', textDecoration: 'none' }}><div className="row" style={{ justifyContent: 'space-between', gap: 'var(--space-3)', alignItems: 'center' }}>
        <div className="row" style={{ gap: 'var(--space-3)', alignItems: 'center', minWidth: 0 }}>
          <span style={{ minWidth: 200 }}>
            <strong style={{ fontSize: 'var(--fs-base)' }}>{p.name || p.slug}</strong>
            {p.name && p.name !== p.slug && <span className="mono muted small" style={{ marginLeft: 'var(--space-2)' }}>{p.slug}</span>}
          </span>
          <StandingChip label={p.statusLabel} help={p.sentence} />
        </div>
        <span className="conformance-extra"><LaneBar lanes={p.lanes} total={p.total} /></span>
      </div>
      <div className="muted small mt-1">
        {fact} · {p.judgedAt ? `Checked ${relTime(p.judgedAt)}` : 'Not checked yet — open to check now'}
      </div>
      </Link>
      {onCheck && p.standing === 'conforms-so-far' && <button onClick={() => onCheck(p.slug)}>Check now</button>}
    </div>
  )
}

export function ProjectsList({ view, base, onCheck }: { view: ConformanceView; base: string; onCheck?: (slug: string) => void }) {
  const [showQuiet, setShowQuiet] = useState(false)
  const [q, setQ] = useState('')
  const needle = q.trim().toLowerCase()
  const hit = (p: ProjectStanding) => !needle || [p.name, p.slug, p.sentence, ...p.adopted].join(' ').toLowerCase().includes(needle)
  const all = view.projects.filter(hit)
  const loud = all.filter((p) => p.standing !== 'conforms' && p.standing !== 'nothing-adopted')
  const quiet = all.filter((p) => p.standing === 'conforms')
  const none = all.filter((p) => p.standing === 'nothing-adopted')
  if (view.projects.length === 0) return <div className="muted" style={{ padding: '40px 0', textAlign: 'center' }}>No repo-backed projects yet.</div>
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--space-2)' }}>
      {view.projects.length > 8 && (
        <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Find a project by name, slug, standing or way of working…"
          style={{ maxWidth: 420, marginBottom: 'var(--space-1)' }} />
      )}
      {needle && all.length === 0 && <div className="muted small" style={{ padding: '8px 14px' }}>Nothing matches "{q}".</div>}
      {loud.map((p) => <ProjectRow key={p.slug} p={p} fact={projectFact(p, view)} base={base} onCheck={onCheck} />)}
      {quiet.length > 0 && (
        <div className="card" style={{ marginBottom: 0, padding: '10px 14px', cursor: 'pointer' }} onClick={() => setShowQuiet(!showQuiet)}>
          <span className="muted small">{showQuiet ? '▾' : '▸'} </span>
          <Status word={quiet[0].statusLabel} tone={STATUS_LIGHT[quiet[0].statusLabel]} />
          <span className="small">{quiet.length} project{quiet.length === 1 ? '' : 's'} meet everything they adopted{showQuiet ? ':' : ` · ${quiet.map((p) => p.name || p.slug).join(', ')}`}</span>
        </div>
      )}
      {showQuiet && quiet.map((p) => <ProjectRow key={p.slug} p={p} fact={projectFact(p, view)} base={base} onCheck={onCheck} />)}
      {none.length > 0 && <div className="muted small" style={{ padding: '4px 14px' }}>Nothing adopted: {none.map((p) => p.name || p.slug).join(', ')}.</div>}
    </div>
  )
}
