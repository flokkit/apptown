import { useNarrow } from '../../chrome'
import { Status, STATUS_LIGHT } from '../../Status'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import type { ConformanceView, ProjectStanding } from '../../wire.gen'
import { fmtWhen } from '../../format'
import { shortSha, StandingChip, LaneBar } from './parts'

// --- Projects (the front page) ---------------------------------------------------------------

function ProjectRow({ p, base }: { p: ProjectStanding; base: string }) {
  const narrow = useNarrow()
  if (narrow) return <Link to={`${base}/projects/${p.slug}`} className="card" style={{ display: 'block', color: 'inherit', textDecoration: 'none' }}>
    <div className="host-row"><strong>{p.name || p.slug}</strong><StandingChip label={p.statusLabel} help={p.sentence} /><span className="muted small">{p.total} checks</span></div>
  </Link>
  return (
    <Link to={`${base}/projects/${p.slug}`} className="card" style={{ marginBottom: 0, padding: '10px 14px', textDecoration: 'none', color: 'inherit', display: 'block' }}>
      <div className="row" style={{ justifyContent: 'space-between', gap: 'var(--space-3)', alignItems: 'center' }}>
        <div className="row" style={{ gap: 'var(--space-3)', alignItems: 'center', minWidth: 0 }}>
          <span style={{ minWidth: 200 }}>
            <strong style={{ fontSize: 'var(--fs-base)' }}>{p.name || p.slug}</strong>
            {p.name && p.name !== p.slug && <span className="mono muted small" style={{ marginLeft: 'var(--space-2)' }}>{p.slug}</span>}
          </span>
          <StandingChip label={p.statusLabel} help={p.sentence} />
        </div>
        <LaneBar lanes={p.lanes} total={p.total} />
      </div>
      <div className="muted small mt-1">
        <span title={p.adopted.join(', ')}>{p.adopted.length ? `${p.adopted.length} adopted blocks` : 'Nothing adopted'}</span>
        {p.sha && <> · judged <span className="mono">{shortSha(p.sha)}</span> {fmtWhen(p.judgedAt)}</>}
      </div>
    </Link>
  )
}

export function ProjectsList({ view, base }: { view: ConformanceView; base: string }) {
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
      {loud.map((p) => <ProjectRow key={p.slug} p={p} base={base} />)}
      {quiet.length > 0 && (
        <div className="card" style={{ marginBottom: 0, padding: '10px 14px', cursor: 'pointer' }} onClick={() => setShowQuiet(!showQuiet)}>
          <span className="muted small">{showQuiet ? '▾' : '▸'} </span>
          <Status word={quiet[0].statusLabel} tone={STATUS_LIGHT[quiet[0].statusLabel]} />
          <span className="small">{quiet.length} project{quiet.length === 1 ? '' : 's'} meet everything they adopted{showQuiet ? ':' : ` · ${quiet.map((p) => p.name || p.slug).join(', ')}`}</span>
        </div>
      )}
      {showQuiet && quiet.map((p) => <ProjectRow key={p.slug} p={p} base={base} />)}
      {none.length > 0 && <div className="muted small" style={{ padding: '4px 14px' }}>Nothing adopted: {none.map((p) => p.name || p.slug).join(', ')}.</div>}
    </div>
  )
}
