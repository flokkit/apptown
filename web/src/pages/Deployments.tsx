import './Deployments.css'
import { ListRow } from '../ListRow'
import { Section } from '../Section'
import { Status } from '../Status'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { FleetDeploymentStatus } from '../deploymentStatus'
import { fmtStamp } from '../format'
import { BackupsView } from './Backups'

// Fleet pages use the same deployment and release reads as their project pages.
import type { Departures, ProjectRow, DeploymentView } from '../wire.gen'
import { PageHead } from '../PageHead'
import { deploymentName } from '../deploymentName'

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
          <div role="list">
            {shown.map((d, i) => {
              const release = d.releaseNumber ? `release #${d.releaseNumber}` : d.label
              const to = d.releaseNumber ? `/projects/${d.slug}/releases/${d.releaseNumber}` : `/projects/${d.slug}`
              return (
                <ListRow key={i} listItem className="departure-row"
                  identity={<>
                    <span>{d.icon && <span aria-hidden className="deployments-span">{d.icon}</span>}<Link className="int-name" to={`/projects/${d.slug}`}>{d.name || d.slug}</Link></span>
                    {/* A release says where it went; anything else carries its own words. */}
                    <div className="muted small">{d.releaseNumber ? `${release} to ${d.tier}` : release}{d.who ? ` · by ${d.who}` : ''}{d.url ? <> · <a href={d.url} target="_blank" rel="noreferrer">open</a></> : null}</div>
                  </>}
                  state={<><Status word={d.state} help={d.reason && d.reason.toLowerCase() !== d.state ? d.reason : undefined} />
                    <div className="muted small deployments-div">{d.at ? fmtStamp(d.at) : 'not yet'}</div></>}
                  action={<Link className="btn" to={to}>{d.state === 'failed' ? 'Review failure' : d.releaseNumber ? 'View release' : 'View app'}</Link>} />
              )
            })}
          </div>
      </Section>
    })()}
    {moves.length> count && <button  onClick={() => setCount(n => n + 10)} className="deployments-departures-board-button">Show 10 more</button>}
    </>
  )
}

function FleetTable() {
  const q = useGet<ProjectRow[]>('/projects', { poll: 8000 })
  const [filter, setFilter] = useState('')
  if (!q.data) return <ReadStatus q={q} what="the projects' deployments" />
  return <div>
    <StaleNote q={q} />
    <input aria-label="Find a deployment" value={filter} onChange={e => setFilter(e.target.value)} placeholder="Find a deployment" />
    <Section title="Deployments"><div role="list">
      {[...q.data].sort((a, b) => a.name.localeCompare(b.name)).map(p => <ProjectFleet key={p.slug} project={p} search={filter.trim().toLowerCase()} />)}
      {!q.data.length && <p className="muted small">No deployments yet.</p>}
    </div></Section>
  </div>
}

function ProjectFleet({ project: p, search }: { project: ProjectRow; search: string }) {
  const q = useGet<DeploymentView[]>(`/projects/${p.slug}/deployments`, { poll: 8000 })
  if (!q.data) return <ReadStatus q={q} what={`${p.name}'s deployments`} />
  return <><StaleNote q={q} />{q.data.filter(e => `${p.name} ${deploymentName(e)}`.toLowerCase().includes(search))
    .sort((a, b) => deploymentName(a).localeCompare(deploymentName(b))).map(e => <ListRow listItem key={e.id || e.name}
      identity={<><Link to={`/projects/${p.slug}/deployments/${e.name}`}><strong>{deploymentName(e)}</strong></Link><div className="muted small">{p.name}</div></>}
      state={<FleetDeploymentStatus slug={p.slug} id={e.id} />}
      action={<Link className="btn" to={`/projects/${p.slug}/deployments/${e.name}`}>View deployment</Link>} />)}</>
}
