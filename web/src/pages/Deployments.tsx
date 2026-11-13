import { ProjectMark } from '../ProjectListRow'
import './Deployments.css'
import { ListTable } from '../ListTable'
import { Section, SectionFilter } from '../Section'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useGet } from '../data'
import { failedRuntimeRefresh } from '../runtime'
import { DeploymentTable, type DeploymentTableRow } from './DeploymentTable'
import { Status } from '../Status'
import { ReadStatus, StaleNote } from '../ReadGate'
import { fmtStamp } from '../format'
import { BackupsView } from './Backups'

// Fleet pages use the same deployment and release reads as their project pages.
import type { Departures, TierMap } from '../wire.gen'
import { PageHead } from '../PageHead'

export default function Deployments({ page = 'deployments' }: { page?: 'releases' | 'deployments' | 'backups' }) {
  return <div>
    <PageHead page={page} />
    {page === 'releases' ? <DeparturesBoard /> : page === 'backups' ? <BackupsView /> : <FleetTable />}
  </div>
}

function DeparturesBoard() {
  const [count, setCount] = useState(10)
  const q = useGet<Departures>('/tiers/departures', { poll: 8000 })
  const data = q.data
  if (!data) return <ReadStatus q={q} what="the releases" />
  const moves = (data.departures ?? []).filter(d => d.releaseNumber)
  return (
    <>
    <StaleNote q={q} />
    {/* One line per deploy, newest first: the app, which release went
        where and who sent it, then how it went and when. Each row carries
        its own date, so there are no day headings. */}
    {moves.length === 0 ? <p className="muted small">No releases yet.</p> : (() => {
      const shown = moves.slice(0, count)
      return <Section title="" label="Deploys">
        <ListTable label="Releases" rows={shown} rowKey={d => `${d.slug}/${d.tier}/${d.releaseNumber}/${d.at}`}
          rowTo={d => `/projects/${d.slug}/releases/${d.releaseNumber}`} columns={[
            { key: 'change', label: 'Change', phone: 'name', render: d => <>{d.changes?.length ? d.changes.map(c => <div key={c.id}><Link to={`/changes/${c.id}`}>#{c.number} {c.title}</Link></div>) : 'No change recorded'}{d.commitSha && <code>{d.commitSha.slice(0, 12)}</code>}</> },
            { key: 'project', label: 'Project', render: d => <><ProjectMark small name={d.name || d.slug} slug={d.slug} icon={d.icon} /><Link className="int-name" to={`/projects/${d.slug}`}>{d.name || d.slug}</Link></> },
            { key: 'deployment', label: 'Deployment', render: d => <>{d.tier.replace(/ deployment$/, '')}{d.url && <> <a href={d.url} target="_blank" rel="noreferrer" title={d.url} aria-label={`open ${d.name || d.slug} ${d.tier}`}>↗</a></>}</> },
            { key: 'release', label: 'Release', render: d => <Link to={`/projects/${d.slug}/releases/${d.releaseNumber}`}>#{d.releaseNumber}</Link> },
            { key: 'by', label: 'By', render: d => <span className="muted">{d.who || '—'}</span> },
            { key: 'status', label: 'Status', phone: 'state', render: d => <Status word={d.state} help={d.reason && d.reason.toLowerCase() !== d.state ? d.reason : undefined} /> },
            { key: 'when', label: 'When', kind: 'time', render: d => d.at ? fmtStamp(d.at) : 'not yet' },
          ]} />
      </Section>
    })()}
    {moves.length> count && <button  onClick={() => setCount(n => n + 10)} className="deployments-departures-board-button">Show 10 more</button>}
    </>
  )
}

// The fleet is one table, one row per standing deployment (DeploymentTable).
function FleetTable() {
  const q = useGet<TierMap>('/tiers', { poll: 8000 })
  const [filter, setFilter] = useState('')
  if (!q.data) return <ReadStatus q={q} what="the deployments" />
  const search = filter.trim().toLowerCase()
  const rows: DeploymentTableRow[] = q.data.tiers.filter(t => t.name !== 'preview' && !t.future)
    .flatMap(t => (t.apps ?? []).filter(a => a.deployment).map(a => ({ ...(q.refreshFailed ? failedRuntimeRefresh(a, q.reason) : a), app: a.name, deployment: a.deployment!, label: a.deploymentLabel || a.deployment!, url: a.url, hostOnly: a.hostOnly, backups: a.backups })))
    .filter(a => `${a.app} ${a.label}`.toLowerCase().includes(search))
    .sort((a, b) => a.app.localeCompare(b.app) || a.label.localeCompare(b.label))
  return <div>
    <StaleNote q={q} />
    <Section title="Deployments" count={rows.length} action={<SectionFilter value={filter} onChange={setFilter} placeholder="Filter deployments" />}>
      {rows.length ? <DeploymentTable rows={rows} withApp /> : <p className="muted small">{search ? `Nothing matches “${filter}”.` : 'No deployments yet.'}</p>}
    </Section>
  </div>
}
