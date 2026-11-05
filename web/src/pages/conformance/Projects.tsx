import { Section, Group } from '../../Section'
import { Status } from '../../Status'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import type { ConformanceView, ProjectStanding } from '../../wire.gen'
import { relTime } from '../../format'
import { laneOf } from './parts'

// --- Projects (the front page) ---------------------------------------------------------------

export function projectFact(p: ProjectStanding, view: ConformanceView): string {
  const observations = view.byProject[p.slug] ?? []
  const lane = (v: typeof observations[number]) => laneOf(v)
  if (p.notKnown.length && !p.lanes.findings) return `Not known: ${p.notKnown.join('; ')}`
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

// One row per project: its standing in the status column, its name, and one
// meta line (what to do, what could not run, when it was checked).
function ProjectRow({ p, fact, base }: { p: ProjectStanding; fact: string; base: string }) {
  const findings = p.standing === 'does-not-conform' ? p.lanes.findings : 0
  const meta = [fact.replace(/\.$/, ''), p.lanes.broken > 0 ? `${p.lanes.broken} couldn't run` : '', p.judgedAt ? `checked ${relTime(p.judgedAt)}` : 'not checked yet'].filter(Boolean).join(' · ')
  return (
    <div className="card row" style={{ display: 'flex' }}>
      <Link to={`${base}/projects/${p.slug}`} className="row grow" style={{ display: 'flex', minWidth: 0 }}>
        <span className="row-status">{findings > 0
          ? <span className="finding-count" title={p.sentence}>{findings} {findings === 1 ? 'finding' : 'findings'}</span>
          : <Status word={p.statusLabel} help={p.sentence} />}</span>
        <strong>{p.name || p.slug}</strong>
        <span className="muted small">{meta}</span>
      </Link>
      <span className="row-action"><Link className="btn" to={`${base}/projects/${p.slug}`}>View checks</Link></span>
    </div>
  )
}

export function ProjectsList({ view, base }: { view: ConformanceView; base: string }) {
  const [q, setQ] = useState('')
  const needle = q.trim().toLowerCase()
  const hit = (p: ProjectStanding) => !needle || [p.name, p.slug, p.sentence, ...p.adopted].join(' ').toLowerCase().includes(needle)
  const all = view.projects.filter(hit)
  const loud = all.filter((p) => p.standing !== 'conforms' && p.standing !== 'nothing-adopted')
  const quiet = all.filter((p) => p.standing === 'conforms')
  const none = all.filter((p) => p.standing === 'nothing-adopted')
  return (
    <Section title="Projects" count={view.projects.length || undefined}>
      {view.projects.length === 0 && <p className="muted small">No repo-backed projects yet.</p>}
      {view.projects.length > 8 && (
        <input aria-label="Find a project" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Find a project by name, slug, standing or way of working…"
          style={{ maxWidth: 420, width: '100%', margin: '12px 0 4px' }} />
      )}
      {needle && all.length === 0 && <p className="muted small">Nothing matches "{q}".</p>}
      {loud.map((p) => <ProjectRow key={p.slug} p={p} fact={projectFact(p, view)} base={base} />)}
      {quiet.length > 0 && <Group label={`Meet everything they adopted · ${quiet.length}`}>
        {quiet.map((p) => <ProjectRow key={p.slug} p={p} fact={projectFact(p, view)} base={base} />)}
      </Group>}
      {none.length > 0 && <p className="muted small">Nothing adopted: {none.map((p) => p.name || p.slug).join(', ')}.</p>}
    </Section>
  )
}
