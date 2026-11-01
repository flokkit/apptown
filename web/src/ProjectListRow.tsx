import type { CSSProperties, ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { Status } from './Status'
import { RuntimeStatus } from './runtime'
import { relTime } from './format'
import type { ProjectRow } from './wire.gen'

// Production liveness is separate from the aggregate status, which may
// describe a preview that needs attention while the app is still running.
export function appRunning(project: ProjectRow, refreshFailed = false): boolean {
  return !refreshFailed && project.prodRunning === true
}

export function projectHue(slug: string): number {
  let hash = 0
  for (const char of slug) hash = (Math.imul(hash, 31) + char.codePointAt(0)!) | 0
  hash = Math.imul(hash ^ (hash >>> 16), 0x45d9f3b)
  return ((hash ^ (hash >>> 16)) >>> 0) % 360
}

// Three cells at every width: authored text never changes the action's column.
export function ProjectListRow({ project: p, refreshFailed = false, reason, children }: {
  project: ProjectRow; refreshFailed?: boolean; reason?: string; children?: ReactNode
}) {
  const observation = refreshFailed ? { ...p, observation: 'unknown' as const, observationStale: true, observationIssue: reason } : p
  const age = p.hasDeployments === false
    ? p.lastRunAt ? `Work updated ${relTime(p.lastRunAt)}` : 'No changes yet'
    : p.observedAt ? `Checked ${relTime(p.observedAt)}` : 'Not checked yet'
  return <div className="project-row" role="listitem">
    <div className="project-identity">
      <span className={`project-avatar${p.icon ? '' : ' project-letter'}`} aria-hidden="true"
        style={{ '--project-hue': projectHue(p.slug) } as CSSProperties}>{p.icon || p.name.slice(0, 1).toUpperCase()}</span>
      <div className="project-copy">
        <Link to={`/projects/${p.slug}`} className="project-name"><strong>{p.name}</strong></Link>
        {p.blurb && <div className="muted small project-description">{p.blurb}</div>}
        {children}
      </div>
    </div>
    <div className="project-state">
      {p.hasDeployments === false ? <Status word="none" /> : <RuntimeStatus observation={observation} />}
      <div className="muted small">{age}</div>
    </div>
    <div className="project-action">
      {appRunning(p, refreshFailed) && p.productionUrl && <a className="btn primary" href={p.productionUrl} target="_blank" rel="noreferrer" aria-label={`Open app: ${p.name || p.slug} (opens in a new tab)`}>Open app <span aria-hidden="true">↗</span></a>}
    </div>
  </div>
}
