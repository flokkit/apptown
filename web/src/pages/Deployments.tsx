import { ListRow } from '../ListRow'
import { Section } from '../Section'
import { Status } from '../Status'
import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { NotAvailable, useInstancePages } from '../instancePages'
import { FleetDeploymentStatus } from '../deploymentStatus'
import { fmtStamp, lastSeen } from '../format'
import { InfrastructureView } from './Infrastructure'
import { BackupsView } from './Backups'
import type { ProjectRow } from '../wire.gen'

// Deployments: where software runs. The tiers are the promotion
// stages (design/subsystems/deployment.md), with independently named customer placements,
// and they read left to right as COLUMNS, one row per app. A picture of
// four lanes was lovely at five apps and a forty-screen scroll at a
// hundred; a matrix is the same ladder, and 500 apps is 500 rows behind a
// find box. Every project is a row, so "not running anywhere" is visible
// rather than absent. Two tabs, two readings of the same world:
// DEPARTURES — movement through the tiers, newest first, the board you
// could leave on a wall monitor; ENVIRONMENTS — the fleet as a table.

import type { Departures, TierApp, TierMap } from '../wire.gen'
import { PageHead } from '../PageHead'

type Row = { slug: string; name: string; icon?: string; cells: Record<string, TierApp[]> }
type Show = 'all' | 'running' | 'stopped' | 'nowhere'


export default function Deployments() {
  const { gtab } = useParams()
  const tab = gtab === 'table' ? 'table' : gtab === 'infrastructure' ? 'infrastructure' : gtab === 'backups' ? 'backups' : 'departures'
  // The hosts and every deployment's backups are infrastructure: someone who
  // cannot read it sees their own projects' deployments and departures only.
  const infra = useInstancePages()('infrastructure')
  const instanceTab = tab === 'infrastructure' || tab === 'backups'
  return (
    <div>
      {/* The menu's Deployments drawer holds these pages; the title says which. */}
      <PageHead page="deployments" sub={{ departures: 'Departures', table: 'Apps', infrastructure: 'Infrastructure', backups: 'Backups' }[tab]} />
      {instanceTab && infra === false ? <NotAvailable page="infrastructure" heading={false} />
        : instanceTab && infra === undefined ? null
        : tab === 'departures' ? <DeparturesBoard /> : tab === 'infrastructure' ? <InfrastructureView /> : tab === 'backups' ? <BackupsView /> : <FleetTable />}
    </div>
  )
}

function DeparturesBoard() {
  const [count, setCount] = useState(10)
  const q = useGet<Departures>('/tiers/departures', { poll: 8000 })
  const data = q.data
  if (!data) return <ReadStatus q={q} what="the departures board" />
  const moves = data.departures ?? []
  // Previews that could not be listed are not on the board; say so rather
  // than let the board read as "no previews".
  const unlisted = data?.previewsObserved
  return (
    <>
    <StaleNote q={q} />
    {unlisted && <p className="muted small" title={data?.previewsReason}>Running previews are not shown: they could not be listed ({unlisted}).</p>}
    {/* One line per deploy, newest first: the app, which release went
        where and who sent it, then how it went and when. Each row carries
        its own date, so there are no day headings. */}
    {moves.length === 0 ? <p className="muted small">No deploys yet.</p> : (() => {
      const shown = moves.slice(0, count)
      return <Section title="" label="Deploys">
          <div role="list">
            {shown.map((d, i) => {
              const release = d.releaseNumber ? `release #${d.releaseNumber}` : d.label
              const to = d.releaseNumber ? `/projects/${d.slug}/releases/${d.releaseNumber}` : `/projects/${d.slug}`
              return (
                <ListRow key={i} listItem className="departure-row"
                  identity={<>
                    <span>{d.icon && <span aria-hidden style={{ marginRight: 6 }}>{d.icon}</span>}<Link className="int-name" to={`/projects/${d.slug}`}>{d.name || d.slug}</Link></span>
                    {/* A release says where it went; anything else carries its own words. */}
                    <div className="muted small">{d.releaseNumber ? `${release} to ${d.tier}` : release}{d.who ? ` · by ${d.who}` : ''}{d.url ? <> · <a href={d.url} target="_blank" rel="noreferrer">open</a></> : null}</div>
                  </>}
                  state={<><Status word={d.state} help={d.reason && d.reason.toLowerCase() !== d.state ? d.reason : undefined} />
                    <div className="muted small" style={{ fontVariantNumeric: 'tabular-nums' }}>{d.at ? fmtStamp(d.at) : 'not yet'}</div></>}
                  action={<Link className="btn" to={to}>{d.state === 'failed' ? 'Review failure' : d.releaseNumber ? 'View release' : 'View app'}</Link>} />
              )
            })}
          </div>
      </Section>
    })()}
    {moves.length > count && <button style={{ marginTop: 10 }} onClick={() => setCount(n => n + 10)}>Show 10 more</button>}
    </>
  )
}

function FleetTable() {
  const tiersQ = useGet<TierMap>('/tiers', { poll: 8000 })
  const projectsQ = useGet<ProjectRow[]>('/projects', { poll: 8000 })
  const tiers = tiersQ.data?.tiers ?? null
  const projects = projectsQ.data
  const [filter, setFilter] = useState('')
  const [show, setShow] = useState<Show>('all')

  // The rows are every project, so a projects read that failed is not shown
  // as a fleet with fewer apps.
  if (!tiers || !projects) return <ReadStatus q={[tiersQ, projectsQ]} what="the deployments" />

  // Pivot: tier lists → one row per app, one cell per tier.
  const rows = new Map<string, Row>()
  const rowFor = (slug: string, name: string, icon?: string) => {
    let r = rows.get(slug)
    if (!r) { r = { slug, name: name || slug, icon, cells: {} }; rows.set(slug, r) }
    return r
  }
  for (const p of projects) rowFor(p.slug, p.name, p.icon)
  for (const t of tiers) for (const a of t.apps) {
    const r = rowFor(a.slug, a.name, a.icon)
    r.cells[t.name] = [...(r.cells[t.name] ?? []), a]
  }
  const runningIn = (r: Row) => Object.values(r.cells).flat().some((a) => a.running)
  const stoppedIn = (r: Row) => Object.values(r.cells).flat().some((a) => a.observation === 'stopped')
  const anywhere = (r: Row) => Object.values(r.cells).flat().length > 0

  const q = filter.trim().toLowerCase()
  const all = [...rows.values()].sort((a, b) => a.name.localeCompare(b.name))
  const shown = all.filter((r) =>
    (!q || [r.name, r.slug, ...Object.values(r.cells).flat().map((a) => [a.deploymentLabel ?? '', a.detail ?? ''].join(' '))].join(' ').toLowerCase().includes(q)) &&
    (show === 'all' || (show === 'running' && runningIn(r)) || (show === 'stopped' && stoppedIn(r)) || (show === 'nowhere' && !anywhere(r))))
  const counts: Record<Show, number> = {
    all: all.length, running: all.filter(runningIn).length,
    stopped: all.filter(stoppedIn).length, nowhere: all.filter((r) => !anywhere(r)).length,
  }
  const SHOW_LABEL: Record<Show, string> = { all: 'All', running: 'Running', stopped: 'Stopped', nowhere: 'Not deployed' }

  const placements = (r: Row) => tiers.filter(t => !t.future).flatMap(t => r.cells[t.name] ?? [])
  // Use the deployment read and diagnostics shared by its project pages.
  // The first current placement represents an app with several deployments.
  const state = (r: Row) => {
    const apps = placements(r)
    if (apps.length === 0) return <span className="muted">Not deployed</span>
    return <FleetDeploymentStatus slug={r.slug} id={apps[0].deploymentId} />
  }
  const meta = (r: Row) => {
    const apps = placements(r)
    if (apps.length === 0) return ''
    const working = apps.filter(a => a.running && !a.observationStale).length
    const project = projects.find(p => p.slug === r.slug)
    const seen = project?.deployed && project.observationStale && project.observedAt ? lastSeen(project.observedAt) : ''
    return [`${apps.length} ${apps.length === 1 ? 'deployment' : 'deployments'}`, working > 0 ? `${working} working` : '', seen].filter(Boolean).join(' · ')
  }

  return (
    <div>
      <StaleNote q={[tiersQ, projectsQ]} />
      <div className="row" style={{ margin: '0 0 10px', gap: 8, flexWrap: 'wrap' }}>
        <input aria-label="Find an app" value={filter} onChange={(e) => setFilter(e.target.value)}
          placeholder="Find an app" style={{ width: 280, maxWidth: '100%' }} />
        {(['all', 'running', 'stopped', 'nowhere'] as Show[]).map((k) => (
          <button key={k} className={'subtle' + (show === k ? ' active' : '')} aria-pressed={show === k}
            style={{ fontWeight: show === k ? 700 : 400 }} onClick={() => setShow(k)}>
            {SHOW_LABEL[k]} <span className="muted small">{counts[k]}</span>
          </button>
        ))}
      </div>
      <Section title="Apps" count={shown.length === all.length ? all.length : undefined} note={shown.length !== all.length ? `${shown.length} of ${all.length} apps` : undefined}>
        {shown.map((r) => (
          <div key={r.slug} className="card row" style={{ display: 'flex' }}>
            <Link to={`/projects/${r.slug}`} className="row" style={{ display: 'flex', flex: 1, minWidth: 0 }}>
              <span className="row-status">{state(r)}</span>
              <strong>{r.icon && <span aria-hidden style={{ marginRight: 6 }}>{r.icon}</span>}{r.name}</strong>
              <span className="muted small">{meta(r)}</span>
            </Link>
            <span className="row-action"><Link className="btn" to={`/projects/${r.slug}`}>View app</Link></span>
          </div>
        ))}
        {shown.length === 0 && <p className="muted small">{q ? 'Nothing matches.' : show !== 'all' ? 'No apps here.' : 'No apps yet.'}</p>}
      </Section>
    </div>
  )
}
