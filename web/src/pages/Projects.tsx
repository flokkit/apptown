import { useNarrow } from '../chrome'
import { Section, Group } from '../Section'
import { RuntimeObservation } from '../runtime'
import { ProjectListRow } from '../ProjectListRow'
import { useEffect, useRef, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import type { ProjectCleanupList, ProjectPage, ProjectRow } from '../wire.gen'

// Projects: find yours. The big find filters the grid as you type; the
// bar's small find on other pages hands a query here as ?q=.

// The server's row, with its observation read as the hand-written union.
type ProjRow = Omit<ProjectRow, keyof RuntimeObservation> & RuntimeObservation
type ProjPage = Omit<ProjectPage, 'projects'> & { projects: ProjRow[] }


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
        <span className="grow" />
        <button className="subtle" onClick={onDismiss}>Dismiss</button>
      </div>
    )
  }
  if (c?.state === 'needs-attention') {
    return (
      <div className="card tone-bad" style={{ maxWidth: 760 }}>
        <strong>{slug}</strong> was deleted, but cleaning up after it stopped: {c.detail}.{' '}
        <Link to="/targets">Try again on Infrastructure</Link>.
      </div>
    )
  }
  return (
    <div className="card tone-busy" style={{ maxWidth: 760 }}>
      <strong>{slug}</strong> is being removed: its app, previews and folders are being cleaned up.
      {q.err && <div className="error small mt-1" >The cleanup's progress could not be read: {q.err}</div>}
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
  // While a new filter's answer is on its way, keep showing the last one:
  // the page, and the filter being typed in, stay put.
  const last = useRef<ProjPage | undefined>(undefined)
  if (pageQ.data) last.current = pageQ.data
  const page = pageQ.data ?? last.current
  const projects = page?.projects
  // The filter is where a person starts on this page; a phone would raise
  // its keyboard over the list, so it waits for a tap there.
  const filterRef = useRef<HTMLInputElement>(null)
  const narrow = useNarrow()
  const listed = !!projects
  useEffect(() => { if (!narrow && listed) filterRef.current?.focus() }, [narrow, listed])

  if (!projects) return <ReadStatus q={pageQ} what="the projects" />

  const q = filter.trim().toLowerCase()
  const shown = projects
  const byNs = new Map<string, ProjRow[]>()
  for (const p of shown) byNs.set(p.namespace, [...(byNs.get(p.namespace) ?? []), p])
  const namespaces = [...byNs.keys()].sort((a, b) => (a || 'zz').localeCompare(b || 'zz'))
  const grouped = namespaces.length > 1 || (namespaces.length === 1 && namespaces[0] !== '')

  const ProjectRows = ({ rows }: { rows: typeof projects }) => (
    <div className="project-rows" role="list">
      {rows.map(p => <ProjectListRow key={p.id} project={p} refreshFailed={pageQ.refreshFailed} reason={pageQ.reason}>
        <div className="small muted project-summary">{p.activeItems > 0 && <>{p.activeItems} in progress · </>}{p.doneRuns} shipped{p.lastChangeTitle && <> · {p.lastChangeTitle}</>}</div>
      </ProjectListRow>)}
    </div>
  )

  return (
    <div>
      {/* The same shape as Home: the page and its main action, then the list
          as a section with its count and its filter, rows on one surface. */}
      <div className="row mb-1" style={{ gap: 10 }}>
        <h1 style={{ margin: 0 }} className="grow">Projects</h1>
        <Link to="/projects/_new"><button className="primary">+ Create project</button></Link>
      </div>
      <StaleNote q={pageQ} />
      {removed && <RemovalNotice slug={removed} onDismiss={() => { params.delete('removed'); setParams(params, { replace: true }) }} />}
      <Section title="All projects" label="All projects" count={page?.total}
        action={<input ref={filterRef} aria-label="filter projects" className="section-filter" value={filter}
          onChange={(e) => { setFilter(e.target.value); setOffset(0) }} placeholder="Filter projects" />}>
        {page?.total === 0 && !q && <p className="muted">No projects yet.</p>}
        {q !== '' && shown.length === 0 && <p className="muted">Nothing matches “{filter}”.</p>}
        {!pageQ.data && <ReadStatus q={pageQ} what="the projects" className="small" />}
        {namespaces.map((ns) => (
          grouped
            ? <Group key={ns || '_'} label={<>{ns || 'ungrouped'} <span className="section-count">{(byNs.get(ns) ?? []).length}</span></>}>
                <ProjectRows rows={byNs.get(ns) ?? []} />
              </Group>
            : <ProjectRows key={ns || '_'} rows={byNs.get(ns) ?? []} />
        ))}
      </Section>

      {/* Paging only when the list does not fit on one page; the section's
          count already says how many there are. */}
      {(offset > 0 || (page?.total ?? 0) > 50) && <div className="row mt-3" style={{ gap: 12 }}>
        <button disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - 50))}>Previous</button>
        <span className="muted small">{projects.length ? `${offset + 1}–${offset + projects.length} of ${page?.total}` : `0 of ${page?.total}`} projects</span>
        <button disabled={offset + 50 >= (page?.total ?? 0)} onClick={() => setOffset(offset + 50)}>Next</button>
      </div>}
    </div>
  )
}
