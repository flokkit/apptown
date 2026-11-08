import { ListTable } from '../../ListTable'
import './list-migrations.css'
import { Section, Group, SectionFilter } from '../../Section'
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
  if (p.notKnown.length && !p.lanes.findings) return `Not known: ${p.notKnown.map((u) => u.detail).join('; ')}`
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

// One table per group of projects: name, what to do next, its standing, and
// when it was checked. Each check's evidence is on the project's page; the row
// opens it. A finding count is the status, in the failed tone.
function ProjectTable({ rows, view, base, label }: { rows: ProjectStanding[]; view: ConformanceView; base: string; label: string }) {
  return <ListTable label={label} rows={rows} rowKey={p => p.slug} rowTo={p => `${base}/projects/${p.slug}`} columns={[
    { key: 'project', label: 'Project', phone: 'name', render: p => <Link to={`${base}/projects/${p.slug}`}>{p.name || p.slug}</Link> },
    { key: 'next', label: 'What to do', phone: 'detail', render: p => <span className="muted">{[projectFact(p, view).replace(/\.$/, ''), p.lanes.broken > 0 ? `${p.lanes.broken} couldn't run` : ''].filter(Boolean).join(' · ')}</span> },
    { key: 'status', label: 'Status', phone: 'state', render: p => {
      const findings = p.standing === 'does-not-conform' ? p.lanes.findings : 0
      return findings > 0 ? <Status word="needs review" reason={`${findings} ${findings === 1 ? 'finding' : 'findings'}`} help={p.sentence} /> : <Status word={p.statusLabel} help={p.sentence} />
    } },
    { key: 'checked', label: 'Checked', phone: 'time', kind: 'time', render: p => p.judgedAt ? relTime(p.judgedAt) : 'not yet' },
  ]} />
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
    <Section title={view.lens.kind === 'current' ? 'Current state' : 'Projects in this snapshot'} count={view.projects.length || undefined}
      action={view.projects.length > 8 && <SectionFilter value={q} onChange={setQ} placeholder="Filter projects" />}>
      {view.projects.length === 0 && <p className="muted small">No repo-backed projects yet.</p>}
      {needle && all.length === 0 && <p className="muted small">Nothing matches "{q}".</p>}
      {loud.length > 0 && <ProjectTable label="Projects to look at" rows={loud} view={view} base={base} />}
      {quiet.length > 0 && <Group label={`Meet everything they adopted · ${quiet.length}`}>
        <ProjectTable label="Projects that meet everything they adopted" rows={quiet} view={view} base={base} />
      </Group>}
      {none.length > 0 && <p className="muted small">Nothing adopted: {none.map((p) => p.name || p.slug).join(', ')}.</p>}
    </Section>
  )
}
