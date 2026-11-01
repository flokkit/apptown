import { useState } from 'react'
import { GlanceRow, PhoneDetails } from '../../GlanceRow'
import { Link } from 'react-router-dom'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { Status, STATUS_LIGHT } from '../../Status'
import { AppAddress, RuntimeStatus } from '../../runtime'
import { fmtWhen } from '../../format'
import type { Backup, DeploymentBackups, DeploymentView, ProjectDetail } from '../../wire.gen'
import type { Release } from '../../api'
import { ActiveRunList, type OverviewData } from '../../parts/runActivity'
import { BigChangeBox, ReleaseRows } from './Overview'
import { ScheduleRows } from './Schedules'
import { PastChats } from '../change/Conversation'

type ActivityEntry = { id: string; when: string; title: string; source: string; word: keyof typeof STATUS_LIGHT; to: string; help?: string }

// Keep each recorded fact attached to its source. A runtime observation is
// neither release evidence nor an approval.
export function overviewActivity(data: OverviewData, deployments: DeploymentView[], backups: Backup[], releases: Release[], slug: string): ActivityEntry[] {
  const released = new Set(releases.flatMap(r => (r.changes ?? []).map(c => c.id)))
  return [
    ...data.history.filter(c => !released.has(c.changeId)).map(c => ({ id: `change-${c.changeId}`, when: c.when, title: c.title, source: `Change #${c.changeNumber}`, word: c.statusLabel, to: `/changes/${c.changeId}` })),
    ...releases.map(r => ({ id: `release-${r.number}`, when: r.releasedAt || r.createdAt, title: r.changes?.length === 1 ? `Change ${r.changes[0].number} → Release ${r.number}` : `Release ${r.number}`, source: r.deployment, word: r.statusLabel, to: `/projects/${slug}/releases/${r.number}` })),
  ].sort((a, b) => b.when.localeCompare(a.when))
}

export function ApplicationOverview({ project }: { project: ProjectDetail }) {
  const [activityCount, setActivityCount] = useState(10)
  const slug = project.slug
  const activityQ = useGet<OverviewData>(`/projects/${slug}/overview`, { poll: 5000 })
  const deploymentsQ = useGet<DeploymentView[]>(`/projects/${slug}/deployments`, { poll: 5000 })
  const releasesQ = useGet<Release[]>(`/projects/${slug}/releases`)
  const deployments = (deploymentsQ.data ?? []).map(d => deploymentsQ.refreshFailed ? { ...d, observation: 'unknown' as const, observationIssue: deploymentsQ.reason, observationStale: true } : d)
  const backupDeployment = deployments.find(d => d.running) ?? deployments[0]
  const backupQ = useGet<DeploymentBackups>(backupDeployment ? `/projects/${slug}/deployments/${backupDeployment.name}/backups` : null, { poll: 30000 })
  const backups = (backupQ.data?.backups ?? []).map(b => backupQ.refreshFailed ? { ...b, statusLabel: 'unknown' as const } : b)
  const data = activityQ.data
  const app = deployments.find(d => d.running && !d.hostOnly) ?? deployments.find(d => d.url)
  const production = deployments.find(d => d.name === 'main') ?? deployments.find(d => d.kind !== 'preview')
  const events = data ? overviewActivity(data, deployments, backups, releasesQ.data ?? [], slug) : []
  const open = data ? data.active.length + (data.open?.length ?? 0) : null
  return <div className="application-overview">
    <header className="application-heading">
      <div className="application-title"><h1>{project.name}</h1>{app?.url && <AppAddress className="application-open" url={app.url} hostOnly={app.hostOnly}>Open app ↗</AppAddress>}</div>
      {project.blurb && <p className="muted">{project.blurb}</p>}
      {app?.url && <AppAddress className="application-address" url={app.url} hostOnly={app.hostOnly}>{app.url.replace(/^https?:\/\//, '')}</AppAddress>}
    </header>
    <div className="application-status">
      {production ? <RuntimeStatus observation={production} /> : deploymentsQ.data ? <span className="muted">No production deployment</span> : <ReadStatus q={deploymentsQ} what="deployments" />}
      <PhoneDetails>{deployments.filter(d => d !== production && (d.observationStale || d.observation !== 'running')).map(d => <span className="row" key={d.id}><span className="muted">{d.displayLabel || d.name}</span><RuntimeStatus observation={d} /></span>)}</PhoneDetails>
      {open !== null && <span className="muted">{open} open change{open === 1 ? '' : 's'}</span>}
      <StaleNote q={[activityQ, deploymentsQ]} />
    </div>
    <section className="application-attention">
      <div className="application-section-head"><h2>Changes</h2>{(project.lifecycle?.state ?? 'active') === 'active' && <BigChangeBox slug={slug} inline />}</div>
      <ReadStatus q={activityQ} what="activity" />
      {data && !open && <p className="muted">No open changes.</p>}
      {data && <ActiveRunList runs={data.active} />}
      {data?.open?.map(c => <Link className="application-request" key={c.changeId} to={`/changes/${c.changeId}`}><Status word={c.statusLabel} /><strong>{c.title}</strong><span className="muted">#{c.changeNumber}{c.editing > 0 ? ` · ${c.editing} files in progress` : ''}</span><span>Open →</span></Link>)}
      <ScheduleRows slug={slug} />
    </section>
    <section className="application-stream">
      <div className="application-section-head"><h2>Activity</h2></div>
      <ReadStatus q={releasesQ} what="releases" /><StaleNote q={releasesQ} />
      <table className="activity-ledger"><thead><tr><th>When</th><th>What happened</th><th>Source</th><th>State</th></tr></thead><tbody>{events.slice(0, activityCount).map(e => <GlanceRow name={1} state={3} fact={0} labels={["When", "Event", "Source", "State"]} key={e.id}><td>{fmtWhen(e.when)}</td><td><Link to={e.to}>{e.title}</Link></td><td>{e.source}</td><td><Status word={e.word} help={e.help} /></td></GlanceRow>)}</tbody></table>
      {events.length > activityCount && <button onClick={() => setActivityCount(n => n + 10)}>Show 10 more</button>}
      {data && !events.length && <p className="muted">No activity yet.</p>}
    </section>
    <details className="application-details" aria-label="Application details">
      <summary>Application details</summary>
      {project.originalRequest && <section><h3>Original request</h3><p style={{ whiteSpace: 'pre-wrap' }}>{project.originalRequest}</p></section>}
      <section><h3>Deployments</h3><ReadStatus q={deploymentsQ} what="deployments" /><StaleNote q={deploymentsQ} />
        {deploymentsQ.data && !deployments.length && <p className="muted">No deployments</p>}
        {deployments.map(d => <div className="application-deployment" key={d.id}><strong>{d.displayLabel || d.name}</strong><RuntimeStatus observation={d} />{d.url && <AppAddress url={d.url} hostOnly={d.hostOnly}>{d.url.replace(/^https?:\/\//, '')}</AppAddress>}</div>)}
        <ReleaseRows slug={slug} releases={releasesQ.data ?? []} />
      </section>
      <section><h3>Backups</h3>{backupDeployment && <ReadStatus q={backupQ} what="backups" />}<StaleNote q={backupQ} />
        {backupQ.data && !backups.length && <p className="muted">No backups yet</p>}
        {!backupDeployment && deploymentsQ.data && <p className="muted">No deployment to back up</p>}
        {backups.slice(0,1).map(b => <div key={b.id}><Status word={b.statusLabel} help={b.error} /><p>{b.deployment} · {fmtWhen(b.takenAt)}</p></div>)}
      </section>
      <section><h3>Configuration</h3><Link to={`/projects/${slug}/settings/working`}>Way of working →</Link><p><Link to={`/projects/${slug}/settings`}>Project settings →</Link></p></section>
    </details>
    <PastChats slug={slug} />
  </div>
}
