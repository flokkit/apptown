import { GlanceRow } from '../GlanceRow'
import { Status, STATUS_LIGHT } from '../Status'
import { useState } from 'react'
import { Link, NavLink, useParams } from 'react-router-dom'
import { useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { NotAvailable, useInstancePages } from '../instancePages'
import { AppAddress, RuntimeStatus } from '../runtime'
import { fmtWhen } from '../format'
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

import type { Departures, TierApp, TierMap, TierView as Tier } from '../wire.gen'
import { PageHead } from '../PageHead'

type Row = { slug: string; name: string; icon?: string; cells: Record<string, TierApp[]> }
type Show = 'all' | 'running' | 'stopped' | 'nowhere'

const host = (u: string) => u.replace(/^https?:\/\//, '').replace(/\/$/, '')

export default function Deployments() {
  const { gtab } = useParams()
  const tab = gtab === 'table' ? 'table' : gtab === 'infrastructure' ? 'infrastructure' : gtab === 'backups' ? 'backups' : 'departures'
  // The hosts and every deployment's backups are infrastructure: someone who
  // cannot read it sees their own projects' deployments and departures only.
  const infra = useInstancePages()('infrastructure')
  const instanceTab = tab === 'infrastructure' || tab === 'backups'
  return (
    <div>
      <PageHead page="deployments" />
      <div className="tabs">
        <NavLink to="/deployments/departures" className={() => 'tab' + (tab === 'departures' ? ' active' : '')}>Departures</NavLink>
        <NavLink to="/deployments/table" className={() => 'tab' + (tab === 'table' ? ' active' : '')}>Deployments</NavLink>
        {infra && <Link to="/infrastructure" className="tab">Infrastructure ↗</Link>}
        {infra && <NavLink to="/deployments/backups" className={() => 'tab' + (tab === 'backups' ? ' active' : '')}>Backups</NavLink>}
      </div>
      {instanceTab && infra === false ? <NotAvailable page="infrastructure" heading={false} />
        : instanceTab && infra === undefined ? null
        : tab === 'departures' ? <DeparturesBoard /> : tab === 'infrastructure' ? <InfrastructureView /> : tab === 'backups' ? <BackupsView /> : <FleetTable />}
    </div>
  )
}

function DeparturesBoard() {
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
    <table className="stack" style={{ fontVariantNumeric: 'tabular-nums' }}>
      <thead><tr><th>When</th><th>App</th><th>To</th><th>What</th><th>Who</th><th>Status</th></tr></thead>
      <tbody>
        {moves.map((d, i) => (
          <GlanceRow name={1} state={5} fact={0} labels={["When", "App", "To", "Release", "Who", "State"]} key={i}>
            <td className="muted small" style={{ whiteSpace: 'nowrap' }}>{fmtWhen(d.at)}</td>
            <td className="stack-lead"><span aria-hidden style={{ marginRight: 6 }}>{d.icon}</span><Link className="tap-open" to={`/projects/${d.slug}`}>{d.name || d.slug}</Link></td>
            <td data-label="to">{d.tier}</td>
            <td className="muted small">
              {d.url ? <a href={d.url} target="_blank" rel="noreferrer">{d.label}</a> : d.label}
            </td>
            <td className="muted small" data-label="by">{d.who || '—'}</td>
            <td className="stack-end"><Status word={d.state} tone={STATUS_LIGHT[d.state]} /></td>
          </GlanceRow>
        ))}
        {moves.length === 0 && <tr><td colSpan={6} className="muted small">{unlisted ? 'No releases yet.' : 'No movement yet.'}</td></tr>}
      </tbody>
    </table>
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
  const SHOW_LABEL: Record<Show, string> = { all: 'all', running: 'running', stopped: 'stopped', nowhere: 'not running anywhere' }

  const phoneState = (r: Row) => {
    const project = projects.find(p => p.slug === r.slug)
    if (!project) return <Status word="unknown" tone="unknown" />
    return project.deployed ? <RuntimeStatus observation={project} /> : <span className="muted small">Not deployed</span>
  }

  const cell = (t: Tier, apps: TierApp[] | undefined) => {
    if (t.future) return <span className="muted">—</span>
    if (t.observed && (!apps || apps.length === 0)) return <span className="muted" title={t.reason}>?</span>
    if (!apps || apps.length === 0) return <span className="muted">—</span>
    if (t.name === 'preview') {
      return (
        <span className="row" style={{ gap: 4, flexWrap: 'wrap' }}>
          {apps.map((a, i) => <span key={i} className="row" style={{ gap: 4 }}>
            <RuntimeStatus observation={a} />
            {a.changeId ? <Link to={`/changes/${a.changeId}`}>{a.changeNumber ? `preview · change ${a.changeNumber}` : 'preview'}</Link> : <span>preview</span>}
          </span>)}
        </span>
      )
    }
    return (
      <span className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
        {apps.map((a, i) => (
          <span key={a.deploymentId ?? i} className="row" style={{ gap: 6 }}>
            {a.deploymentLabel && <span className="small">{a.deploymentLabel}</span>}
            <RuntimeStatus observation={a} />
            {a.url
              ? <AppAddress url={a.url} hostOnly={a.hostOnly} className="small" title={a.url}>
                  {a.hostOnly ? null : a.static && a.running ? 'Open app' : host(a.url)}
                </AppAddress>
              : null}
            {a.detail && <details className="small"><summary>Details</summary>{a.detail}</details>}
          </span>
        ))}
      </span>
    )
  }

  return (
    <div>
      <StaleNote q={[tiersQ, projectsQ]} />
      <div className="row" style={{ margin: '0 0 10px', gap: 8, flexWrap: 'wrap' }}>
        <input value={filter} onChange={(e) => setFilter(e.target.value)}
          placeholder="Find an app" style={{ width: 280 }} />
        {(['all', 'running', 'stopped', 'nowhere'] as Show[]).map((k) => (
          <button key={k} className={'subtle' + (show === k ? ' active' : '')}
            style={{ fontWeight: show === k ? 700 : 400 }} onClick={() => setShow(k)}>
            {SHOW_LABEL[k]} <span className="muted small">{counts[k]}</span>
          </button>
        ))}
        <span style={{ flex: 1 }} />
        <span className="muted small">{shown.length} of {all.length} apps</span>
      </div>

      <div style={{ overflowX: 'auto' }}>
        <table className="stack fleet-table">
          <thead>
            <tr>
              <th>App</th>
              {tiers.map((t, i) => (
                <th key={t.name} title={t.gloss} style={{ opacity: t.future ? 0.5 : 1, whiteSpace: 'nowrap' }}>
                  {i > 0 && <span className="muted" style={{ marginRight: 8 }}>→</span>}
                  {t.name === 'lan' ? '🏢 ' : t.name === 'net' ? '🌍 ' : ''}{t.name === 'preview' ? t.title : `${t.title} deployment`}
                  {!t.future && (
                    <span className="muted small" style={{ marginLeft: 6, fontWeight: 400, textTransform: 'none' }} title={t.reason}>
                      {t.observed ? `running: ${t.observed}` : `${t.apps.filter((a) => a.running).length} running`}
                    </span>
                  )}
                  {t.future && <span className="muted small" style={{ marginLeft: 6, fontWeight: 400, textTransform: 'none' }}>someday</span>}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {shown.map((r) => (
              <GlanceRow name={0} status={phoneState(r)} summary={<>{Object.values(r.cells).flat().length} {Object.values(r.cells).flat().length === 1 ? 'deployment' : 'deployments'}</>} labels={["App", ...tiers.map(t => t.title)]} key={r.slug}>
                <td className="stack-lead" style={{ whiteSpace: 'nowrap' }}>
                  <span aria-hidden style={{ marginRight: 6 }}>{r.icon || '▫️'}</span>
                  <Link to={`/projects/${r.slug}`}>{r.name}</Link>
                </td>
                {tiers.map((t) => <td key={t.name} data-label={t.title}>{cell(t, r.cells[t.name])}</td>)}
              </GlanceRow>
            ))}
            {shown.length === 0 && (
              <tr><td colSpan={1 + tiers.length} className="muted small">{q ? 'Nothing matches.' : tiers.some((t) => t.observed) ? 'No apps running.' : 'No apps yet.'}</td></tr>
            )}
          </tbody>
        </table>
      </div>

    </div>
  )
}
