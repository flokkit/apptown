import './ApplicationOverview.css'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { ChangeStatus, Status, STATUS_LIGHT } from '../../Status'
import { DeploymentStatus } from '../../deploymentStatus'
import { AppAddress } from '../../runtime'
import { fmtWhen } from '../../format'
import type { Backup, DeploymentBackups, DeploymentView, ProjectDetail } from '../../wire.gen'
import type { Release } from '../../api'
import { ActiveRunList, type OverviewData } from '../../parts/runActivity'
import { BigChangeBox, ReleaseRows } from './Overview'
import { ScheduleRows } from './Schedules'
import { Section } from '../../Section'
import './overview.css'

type ActivityEntry = { waitingOn?: import('../../wire.gen').WaitingOn; id: string; when: string; number?: number; title: string; source: string; word: keyof typeof STATUS_LIGHT; to: string; help?: string; release?: { label: string; to: string } }

// Keep each recorded fact attached to its source. A runtime observation is
// neither release evidence nor an approval.
export function overviewActivity(data: OverviewData, deployments: DeploymentView[], backups: Backup[], releases: Release[], slug: string): ActivityEntry[] {
  const released = new Set(releases.flatMap(r => (r.changes ?? []).map(c => c.id)))
  return [
    ...data.history.filter(c => !released.has(c.changeId)).map(c => ({ id: `change-${c.changeId}`, when: c.when, number: c.changeNumber, title: c.title, source: '', word: c.statusLabel, waitingOn: c.waitingOn, to: `/changes/${c.changeId}` })),
    // A release reads as what it shipped: its change's title leads and opens
    // the change; the release is its own link in the small print. A release
    // of several changes opens the release, which lists them.
    ...releases.map(r => {
      const shipped = r.changes ?? []
      const release = `/projects/${slug}/releases/${r.number}`
      const one = shipped.length === 1 && shipped[0].id ? shipped[0] : undefined
      return {
        id: `release-${r.number}`, when: r.releasedAt || r.createdAt,
        number: shipped.length === 1 ? shipped[0].number : undefined,
        title: shipped.length === 0 ? `Release ${r.number}` : shipped.length === 1 ? shipped[0].title : `${shipped[0].title} and ${shipped.length - 1} more`,
        source: r.deployment,
        word: r.statusLabel, to: one ? `/changes/${one.id}` : release,
        release: shipped.length > 0 ? { label: `Release ${r.number}`, to: release } : undefined,
      }
    }),
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
  const events = data ? overviewActivity(data, deployments, backups, releasesQ.data ?? [], slug) : []
  const open = data ? data.active.length + (data.open?.length ?? 0) : null
  return <div className="application-overview">
    <header className="application-heading">
      <div className="application-title"><h1>{project.name}</h1>{app?.url && <AppAddress className="btn primary application-open" url={app.url} hostOnly={app.hostOnly}>Open app ↗</AppAddress>}</div>
      {project.blurb && <p className="muted">{project.blurb}</p>}
      {app?.url && <AppAddress className="application-address" url={app.url} hostOnly={app.hostOnly}>{app.url.replace(/^https?:\/\//, '')}</AppAddress>}
    </header>
    <Section title="Changes" count={open ?? undefined} action={(project.lifecycle?.state ?? 'active') === 'active' ? <BigChangeBox slug={slug} inline /> : undefined}>
      <ReadStatus q={activityQ} what="activity" />
      {data && !open && <p className="muted">No open changes.</p>}
      {data && <ActiveRunList runs={data.active} />}
      {/* An open change with no run yet: the same row as a run, its number
          before its title, its one action on the right. */}
      {data?.open?.map(c => (
        <div key={c.changeId} className="card row application-overview-card">
          <Link to={`/changes/${c.changeId}`} className="row grow application-overview-card">
            <span className="row-status"><ChangeStatus waiting={c.waitingOn} /></span>
            <strong><span className="muted application-overview-span">#{c.changeNumber} </span>{c.title}</strong>
            {c.editing > 0 && <span className="muted small">{c.editing} file{c.editing === 1 ? '' : 's'} being edited</span>}
          </Link>
          <span className="row-action">{c.waitingOn.kind === 'chat' ? <Link to={`/projects/${slug}/console?change=${c.changeId}`} className="btn">Continue chat</Link> : <Link to={`/changes/${c.changeId}`} className="btn">Open</Link>}</span>
        </div>
      ))}
    </Section>
    <ScheduleRows slug={slug} />
    <Section title="Activity">
      <ReadStatus q={releasesQ} what="releases" /><StaleNote q={releasesQ} />
      {events.slice(0, activityCount).map(e => (
        <div key={e.id} className="card row application-overview-card">
          <Link to={e.to} className="row grow application-overview-card">
            <span className="row-status">{e.waitingOn ? <ChangeStatus waiting={e.waitingOn} /> : <Status word={e.word} help={e.help} />}</span>
            <strong>{e.number !== undefined && <span className="muted application-overview-span">#{e.number} </span>}{e.title}</strong>
            <span className="muted small">{fmtWhen(e.when)}</span>
          </Link>
          <span className="muted small">{e.release && <><Link to={e.release.to}>{e.release.label}</Link>{e.source ? ' · ' : ''}</>}{e.source}</span>
        </div>
      ))}
      {events.length > activityCount && <div className="card"><button className="link" onClick={() => setActivityCount(n => n + 10)}>Show 10 more</button></div>}
      {data && !events.length && <p className="muted">No activity yet.</p>}
    </Section>
    {/* Every deployment, named, with its state and address: what used to be
        an unlabelled strip under the title. */}
    <Section title="Deployments" count={deployments.length || undefined}
      action={<Link to={`/projects/${slug}/settings/deployments`}>Manage →</Link>}>
      <ReadStatus q={deploymentsQ} what="deployments" /><StaleNote q={deploymentsQ} />
      {deploymentsQ.data && !deployments.length && <p className="muted">Not deployed yet.</p>}
      {deployments.map(d => (
        <div key={d.id} className="card row application-overview-card">
          <span className="row-status"><DeploymentStatus slug={slug} env={d} /></span>
          <strong>{d.displayLabel || d.name}</strong>
          <span className="muted small">
            {d.url ? <AppAddress url={d.url} hostOnly={d.hostOnly}>{d.url.replace(/^https?:\/\//, '')}</AppAddress> : 'no address yet'}
            {d.targetName && ` · ${d.targetName}`}
          </span>
        </div>
      ))}
      <ReleaseRows slug={slug} releases={releasesQ.data ?? []} />
    </Section>
    {/* Omitted while there is nothing to back up: no deployment, or one
        whose backup plan is empty and which has no copies. A failed read
        still shows, so unknown never reads as none. */}
    {backupDeployment && (backups.length > 0 || (backupQ.data?.plan?.length ?? 0) > 0 || !backupQ.data) && <Section title="Backups">
      <ReadStatus q={backupQ} what="backups" /><StaleNote q={backupQ} />
      {backupQ.data && !backups.length && <p className="muted">No backups yet.</p>}
      {backups.slice(0, 1).map(b => (
        <div key={b.id} className="card row application-overview-card">
          <span className="row-status"><Status word={b.statusLabel} help={b.error} /></span>
          <span>{b.deployment} · {fmtWhen(b.takenAt)}</span>
        </div>
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
