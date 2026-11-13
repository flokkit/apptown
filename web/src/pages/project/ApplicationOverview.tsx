import { useNotOff } from '../../features'
import { useHolds } from '../../instancePages'
import { ListRow } from '../../ListRow'
import { failedRuntimeRefresh } from '../../runtime'
import './ApplicationOverview.css'
import { PageHead } from '../../PageHead'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useGet } from '../../data'
import { DeploymentTable } from '../DeploymentTable'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { ChangeStatus, Status, STATUS_LIGHT } from '../../Status'
import { AppAddress } from '../../runtime'
import { fmtWhen } from '../../format'
import type { Backup, DeploymentBackups, DeploymentView, ProjectDetail } from '../../wire.gen'
import type { Release } from '../../api'
import { ActiveRunList, type OverviewData } from '../../parts/runActivity'
import { BigChangeBox, ReleaseRows } from './Overview'
import { ScheduleRows } from './Schedules'
import { Section } from '../../Section'
import './overview.css'

type ActivityEntry = { id: string; when: string; number?: number; title: string; source: string; to: string } & (
  { waitingOn: import('../../wire.gen').WaitingOn } | { word: keyof typeof STATUS_LIGHT; help?: string }
)

// Keep each recorded fact attached to its source. A runtime observation is
// neither release evidence nor an approval.
export function overviewActivity(data: OverviewData, deployments: DeploymentView[], backups: Backup[], releases: Release[], slug: string): ActivityEntry[] {
  const seen = new Set<string | null>()
  const current: ActivityEntry[] = []
  for (const c of [...data.active.filter(c => c.changeId), ...data.open]) {
    if (!c.changeId || seen.has(c.changeId)) continue
    seen.add(c.changeId)
    current.push({ id: `change-${c.changeId}`, when: 'startedAt' in c ? c.startedAt : '', number: c.changeNumber, title: c.title, source: '', waitingOn: c.waitingOn, to: `/changes/${c.changeId}` })
  }
  const past: ActivityEntry[] = [
    ...data.history.filter(c => !seen.has(c.changeId)).map(c => ({ id: `change-${c.changeId}`, when: c.when, number: c.changeNumber, title: c.title, source: '', waitingOn: c.waitingOn, to: `/changes/${c.changeId}` })),
    ...releases.map(r => ({
      id: `release-${r.number}`, when: r.releasedAt || r.createdAt,
      title: `Release ${r.number} to ${r.deployment}`, source: '',
      word: r.statusLabel, to: `/projects/${slug}/releases/${r.number}`,
    })),
  ]
  return [...current, ...past.sort((a, b) => b.when.localeCompare(a.when))]
}

export function ApplicationOverview({ project }: { project: ProjectDetail }) {
  const [activityCount, setActivityCount] = useState(10)
  const slug = project.slug
  const activityQ = useGet<OverviewData>(`/projects/${slug}/overview`, { poll: 5000 })
  const deploymentsQ = useGet<DeploymentView[]>(`/projects/${slug}/deployments`, { poll: 5000 })
  const releasesQ = useGet<Release[]>(`/projects/${slug}/releases`)
  const deployments = (deploymentsQ.data ?? []).map(d => deploymentsQ.refreshFailed ? failedRuntimeRefresh(d, deploymentsQ.reason) : d)
  const backupsOn = useNotOff('backups')
  const backupDeployment = !backupsOn ? undefined : deployments.find(d => d.running) ?? deployments[0]
  const backupQ = useGet<DeploymentBackups>(backupDeployment ? `/projects/${slug}/deployments/${backupDeployment.name}/backups` : null, { poll: 30000 })
  const backups = (backupQ.data?.backups ?? []).map(b => backupQ.refreshFailed ? { ...b, statusLabel: 'unknown' as const } : b)
  const data = activityQ.data
  const app = deployments.find(d => d.running && !d.hostOnly) ?? deployments.find(d => d.url)
  const canStart = useHolds('change:create', slug) && (project.lifecycle?.state ?? 'active') === 'active'
  const events = data ? overviewActivity(data, deployments, backups, releasesQ.data ?? [], slug) : []
  return <div className="application-overview">
    <header className="application-heading">
      <PageHead page="projectOverview" title={project.name} actions={app?.url && <AppAddress className="btn primary application-open" url={app.url} hostOnly={app.hostOnly}>Open app ↗</AppAddress>} />
      {project.blurb && <p className="muted">{project.blurb}</p>}
      {app?.url && <AppAddress url={app.url} hostOnly={app.hostOnly} />}
    </header>
    <ScheduleRows slug={slug} />
    <Section title="Activity">
      <ReadStatus q={activityQ} what="activity" /><StaleNote q={activityQ} />
      <ReadStatus q={releasesQ} what="releases" /><StaleNote q={releasesQ} />
      {events.slice(0, activityCount).map(e => (
        <ListRow key={e.id} to={e.to} className="card row application-overview-card" state={<span className="row-status">{'waitingOn' in e ? <ChangeStatus waiting={e.waitingOn} /> : <Status word={e.word} help={e.help} />}</span>} identity={<>
          <Link to={e.to} className="row grow application-overview-link">
            <strong>{e.number !== undefined && <span className="muted">#{e.number} </span>}{e.title}</strong>
            <span className="muted small">{e.when && fmtWhen(e.when)}</span>
          </Link>
          {e.source && <span className="muted small">{e.source}</span>}</>} />
      ))}
      {events.length > activityCount && <div className="card"><button className="link" onClick={() => setActivityCount(n => n + 10)}>Show 10 more</button></div>}
      {data && !events.length && <p className="muted">No activity yet.{canStart && <> <Link to={`/projects/${slug}/changes`}>Start a change</Link></>}</p>}
    </Section>
    {/* Every deployment, named, with its state and address: what used to be
        an unlabelled strip under the title. */}
    <Section title="Deployments" count={deployments.length || undefined}
      action={<Link to={`/projects/${slug}/deployments`}>Manage →</Link>}>
      <ReadStatus q={deploymentsQ} what="deployments" /><StaleNote q={deploymentsQ} />
      {deploymentsQ.data && !deployments.length && <p className="muted">Not deployed yet.</p>}
      {deployments.length > 0 && <DeploymentTable rows={deployments.map(d => ({ ...d, slug, app: project.name, deployment: d.name, label: d.displayLabel || d.name, static: d.static, url: d.url, hostOnly: d.hostOnly, backups: d.backups }))} />}
      <ReleaseRows slug={slug} releases={releasesQ.data ?? []} />
    </Section>
    {/* Omitted while there is nothing to back up: no deployment, or one
        whose backup plan is empty and which has no copies. A failed read
        still shows, so unknown never reads as none. */}
    {backupsOn && backupDeployment && (backups.length > 0 || (backupQ.data?.plan?.length ?? 0) > 0 || !backupQ.data) && <Section title="Backups">
      <ReadStatus q={backupQ} what="backups" /><StaleNote q={backupQ} />
      {backupQ.data && !backups.length && <p className="muted">No backups yet.</p>}
      {backups.slice(0, 1).map(b => (
        <ListRow key={b.id} className="card row application-overview-card" state={<span className="row-status"><Status word={b.statusLabel} help={b.error} /></span>} identity={<span>{b.deployment} · {fmtWhen(b.takenAt)}</span>} />
      ))}
    </Section>}
    <Section title="About" action={<><Link to={`/projects/${slug}/settings/working`}>Way of working →</Link><Link to={`/projects/${slug}/settings`}>Settings →</Link></>}>
      {project.originalRequest
        ? <p className="application-overview-p"><span className="muted small mb-1 application-overview-span-2">What was first asked for</span>{project.originalRequest}</p>
        : <p className="muted">No original request recorded.</p>}
    </Section>
  </div>
}

// An open change with no run yet is worked in its chat: say so, not "waiting".

export function ProjectChanges({ project }: { project: ProjectDetail }) {
 const slug = project.slug
 const activityQ = useGet<OverviewData>(`/projects/${slug}/overview`, { poll: 5000 })
 const data = activityQ.data
 const open = data ? data.active.length + (data.open?.length ?? 0) : null
 return <><StaleNote q={activityQ} />
    <Section title="Open changes" count={open ?? undefined} action={(project.lifecycle?.state ?? 'active') === 'active' ? <BigChangeBox slug={slug} inline /> : undefined}>
      <ReadStatus q={activityQ} what="activity" />
      {data && !open && <p className="muted">No open changes.</p>}
      {data && <ActiveRunList runs={data.active} />}
      {/* An open change with no run yet: the same row as a run, its number
          before its title, its one action on the right. */}
      {data?.open?.map(c => (
        <ListRow key={c.changeId} to={`/changes/${c.changeId}`} className="card row application-overview-card" state={<span className="row-status"><ChangeStatus waiting={c.waitingOn} /></span>} identity={
          <Link to={`/changes/${c.changeId}`} className="row grow application-overview-link">
            <strong><span className="muted">#{c.changeNumber} </span>{c.title}</strong>
            {c.editing > 0 && <span className="muted small">{c.editing} file{c.editing === 1 ? '' : 's'} being edited</span>}
          </Link>} action={c.waitingOn.kind === 'chat' && <span className="row-action"><Link to={`/changes/${c.changeId}?tab=conversation`} className="btn">Continue chat</Link></span>} />
      ))}
    </Section>
<Section title="Finished changes">
 {/* The same row as an open change: number and title, then how it ended and when. */}
 {data?.history.map(c => <ListRow to={`/changes/${c.changeId}`} className="card row application-overview-card" key={c.changeId} state={<ChangeStatus waiting={c.waitingOn} />} identity={<>
   <Link to={`/changes/${c.changeId}`} className="run-link"><strong><span className="muted">#{c.changeNumber} </span>{c.title}</strong></Link>
   <div className="muted small">{fmtWhen(c.when)}</div>
 </>} />)}
 </Section></>
}
