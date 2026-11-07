import { type CSSProperties, type ReactNode } from 'react'
import { ListRow } from './ListRow'
import { ChangeButton } from './pages/project/ChangeButton'
import { Link } from 'react-router-dom'
import { RuntimeStatus, failedRuntimeRefresh } from './runtime'
import { relTime } from './format'
import type { ProjectRow } from './wire.gen'

// Production liveness is separate from the aggregate status, which may
// describe a preview that needs attention while the app is still running.
// Only an app seen stopped or unreachable, or a list that failed to
// refresh, loses its link: not knowing yet (just after a restart, or while
// a reading is late) is not stopped, and the address still works.
export function mayOpenApp(project: ProjectRow, refreshFailed = false): boolean {
  // A running app keeps its link even when a preview is what is unreachable.
  return !refreshFailed && (project.prodRunning ?? project.observation !== 'unreachable')
}

export function projectHue(slug: string): number {
  let hash = 0
  for (const char of slug) hash = (Math.imul(hash, 31) + char.codePointAt(0)!) | 0
  hash = Math.imul(hash ^ (hash >>> 16), 0x45d9f3b)
  return ((hash ^ (hash >>> 16)) >>> 0) % 360
}

// Identity yields space to actions; narrow lists stack the cells in reading order.
export function ProjectListRow({ project: p, refreshFailed = false, reason, children }: {
  project: ProjectRow; refreshFailed?: boolean; reason?: string; children?: ReactNode
}) {
  const observation = refreshFailed ? failedRuntimeRefresh(p, reason || 'The project list could not be read') : p
  return <ListRow listItem className="project-row" identity={<div className="project-identity">
      {/* The icon opens the project too; the name stays the one keyboard stop. */}
      <Link to={`/projects/${p.slug}`} tabIndex={-1} aria-hidden="true" className={`project-avatar${p.icon ? '' : ' project-letter'}`}
        style={{ '--project-hue': projectHue(p.slug) } as CSSProperties}>{p.icon || p.name.slice(0, 1).toUpperCase()}</Link>
      <div className="project-copy">
        <Link to={`/projects/${p.slug}`} className="project-name"><strong>{p.name}</strong></Link>
        {p.blurb && <div className="muted small project-description">{p.blurb}</div>}
        {children}
      </div>
    </div>} state={<div className="project-state">
      {p.hasDeployments === false ? <strong className="small" style={{ color: 'var(--muted)' }}>Not deployed</strong> : <RuntimeStatus observation={observation} />}
      {p.hasDeployments === false && <div className="muted small">{p.lastRunAt ? `Work updated ${relTime(p.lastRunAt)}` : 'No changes yet'}</div>}
    </div>} action={<div className="project-action">
      {mayOpenApp(p, refreshFailed) && p.productionUrl && <a className="btn primary" href={p.productionUrl} target="_blank" rel="noreferrer" aria-label={`Open app: ${p.name || p.slug} (opens in a new tab)`}>Open app <span aria-hidden="true">↗</span></a>}
      {!refreshFailed && p.canChange && <ChangeButton slug={p.slug} chatAvailable={p.chatAvailable} compact />}
    </div>} />
}
