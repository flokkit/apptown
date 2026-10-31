import { GlanceRow } from '../GlanceRow'
import { Status } from '../Status'
import { RuntimeObservation, RuntimeStatus } from '../runtime'
import { useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { relTime } from '../format'
import type { ProjectCleanupList, ProjectPage, ProjectRow } from '../wire.gen'

// Projects: find yours. The big find filters the grid as you type; the
// bar's small find on other pages hands a query here as ?q=.

// The server's row, with its observation read as the hand-written union.
type ProjRow = Omit<ProjectRow, keyof RuntimeObservation> & RuntimeObservation
type ProjPage = Omit<ProjectPage, 'projects'> & { projects: ProjRow[] }

const rel = (ts: string): string => relTime(ts) || 'No changes yet'

// After a delete (?removed=<slug>): the records are gone at once, and a
// queued cleanup takes down the app and removes previews, volumes and
// folders. This follows that cleanup until it is done or needs a person.
function RemovalNotice({ slug, onDismiss }: { slug: string; onDismiss: () => void }) {
  const [done, setDone] = useState(false)
  const q = useGet<ProjectCleanupList>('/project-cleanups', { poll: done ? 0 : 3000 })
  // The newest cleanup for this address is this deletion's (the list is newest first).
  const c = q.data?.cleanups.find((x) => x.slug === slug)
  const finished = c?.state === 'done'
  useEffect(() => { if (finished) setDone(true) }, [finished])
  if (c?.state === 'done') {
    return (
      <div className="card tone-ok row" style={{ maxWidth: 760 }}>
        <span><strong>{slug}</strong> has been removed: its app, previews and folders are cleaned up.</span>
        <span style={{ flex: 1 }} />
        <button className="subtle" onClick={onDismiss}>Dismiss</button>
      </div>
    )
  }
  if (c?.state === 'needs-attention') {
    return (
      <div className="card tone-bad" style={{ maxWidth: 760 }}>
        <strong>{slug}</strong> was deleted, but cleaning up after it stopped: {c.detail}.{' '}
        <Link to="/infrastructure">Try again on Infrastructure</Link>.
      </div>
    )
  }
  return (
    <div className="card tone-busy" style={{ maxWidth: 760 }}>
      <strong>{slug}</strong> is being removed: its app, previews and folders are being cleaned up.
      {q.err && <div className="error small" style={{ marginTop: 4 }}>The cleanup's progress could not be read: {q.err}</div>}
    </div>
  )
}

export default function Projects() {
  // The big find: filters the grid as you type. The bar's small find on
  // other pages hands a query here as ?q=.
  const [params, setParams] = useSearchParams()
  const removed = params.get('removed')
  const [filter, setFilter] = useState(params.get('q') ?? '')
  const [offset, setOffset] = useState(0)
  const pageQ = useGet<ProjPage>(`/projects?limit=50&offset=${offset}&q=${encodeURIComponent(filter.trim())}`, { poll: 5000 })
  const page = pageQ.data
  const projects = page?.projects

  if (!projects) return <ReadStatus q={pageQ} what="the projects" />

  const q = filter.trim().toLowerCase()
  const shown = projects
  const byNs = new Map<string, ProjRow[]>()
  for (const p of shown) byNs.set(p.namespace, [...(byNs.get(p.namespace) ?? []), p])
  const namespaces = [...byNs.keys()].sort((a, b) => (a || 'zz').localeCompare(b || 'zz'))
  const grouped = namespaces.length > 1 || (namespaces.length === 1 && namespaces[0] !== '')

  return (
    <div>
      <div className="row" style={{ marginBottom: 12, gap: 10 }}>
        <h1 style={{ margin: 0 }}>Projects</h1>
        <input
          aria-label="filter projects"
          value={filter}
          onChange={(e) => { setFilter(e.target.value); setOffset(0) }}
          placeholder="Filter projects"
          style={{ flex: 1, maxWidth: 480, fontSize: 'var(--fs-base)', padding: '9px 12px', boxSizing: 'border-box' }}
        />
        <span style={{ flex: 1 }} />
        <Link to="/projects/_new"><button className="primary btn-big">+ New project</button></Link>
      </div>
      <StaleNote q={pageQ} />
      {removed && <RemovalNotice slug={removed} onDismiss={() => { params.delete('removed'); setParams(params, { replace: true }) }} />}
      {page?.total === 0 && !q && (
        <div className="card" style={{ padding: 18, maxWidth: 560 }}>
          <strong>No projects yet.</strong>
        </div>
      )}
      {q !== '' && shown.length === 0 && (
        <div className="muted" style={{ padding: 12 }}>Nothing matches “{filter}”.</div>
      )}
      {namespaces.map((ns) => (
        <section key={ns || '_'} className="conn-section">
          {grouped && (
            <div className="conn-section-head">
              <h2>{ns || 'ungrouped'}</h2>
              <span className="muted small">{(byNs.get(ns) ?? []).length} project{(byNs.get(ns) ?? []).length === 1 ? '' : 's'}</span>
            </div>
          )}
          <table className="stack project-list">
            <thead><tr><th>Application</th><th>Activity</th><th>Updated</th><th>State</th></tr></thead>
            <tbody>{(byNs.get(ns) ?? []).map((p) => <GlanceRow name={0} state={3} summary={<>{p.activeItems ?? 0} in progress</>} labels={["Application", "Activity", "Updated", "State"]} key={p.id}>
              <td className="stack-lead"><Link className="tap-open" to={`/projects/${p.slug}`}><strong>{p.icon && <span aria-hidden>{p.icon} </span>}{p.name}</strong></Link>{p.blurb && <div className="small muted project-description">{p.blurb}</div>}</td>

              <td className="project-extra" data-label="activity">{p.activeItems ?? 0} in progress · {p.doneRuns} shipped</td>
              <td data-label="updated" className="muted small">{rel(p.lastRunAt)}</td>
              <td className="stack-end">{p.hasDeployments !== false ? <RuntimeStatus observation={p} /> : <Status word="none" text="Not deployed" tone="" />}</td>
            </GlanceRow>)}</tbody>
          </table>
        </section>
      ))}

      <div className="row" style={{ gap: 12, marginTop: 16 }}>
        <button disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - 50))}>Previous</button>
        <span className="muted small">{projects.length ? `${offset + 1}–${offset + projects.length} of ${page?.total}` : `0 of ${page?.total}`} projects</span>
        <button disabled={offset + 50 >= (page?.total ?? 0)} onClick={() => setOffset(offset + 50)}>Next</button>
      </div>
    </div>
  )
}
