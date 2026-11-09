import { ProjectMark } from '../ProjectListRow'
import { useHolds } from '../instancePages'
import { useNotOff } from '../features'
import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { post } from '../api'
import { useAction, useGet } from '../data'
import { fmtBytes, fmtStamp, fmtWhen } from '../format'
import { ListTable, type ListColumn } from '../ListTable'
import { MoreMenu, type MoreItem } from '../MoreMenu'
import { RuntimeStatus, hostOnlyWords, type RuntimeObservation } from '../runtime'
import type { AllBackups, TargetCapacity } from '../wire.gen'
import './Deployments.css'

// A deployment as a table row: its app and project, its own name and label,
// where it runs and what is observed of it now.
export type DeploymentTableRow = RuntimeObservation & {
  slug: string; app: string; icon?: string; deployment: string; label: string; static?: boolean; url?: string; hostOnly?: boolean
}

export const deploymentHref = (r: { slug: string; deployment: string }) => `/projects/${r.slug}/deployments/${encodeURIComponent(r.deployment)}`

// DeploymentTable is the one list of deployments, the fleet's and a
// project's alike: the app (when it lists more than one), the deployment,
// where it runs, its status, what it uses now, its last backup and its less
// common acts. A row opens the deployment. Too narrow for them, it drops
// where it runs and what it uses; each deployment's page has both.
// release, on a project's own deployments page, adds what each runs and how
// its releases come.
export function DeploymentTable({ rows, withApp = false, release }: { rows: DeploymentTableRow[]; withApp?: boolean; release?: (row: DeploymentTableRow) => ReactNode }) {
  // While deployment backups are off, nothing here offers or reports them.
  const backups = useNotOff('backups')
  const columns: ListColumn<DeploymentTableRow>[] = [
    ...(withApp ? [{ key: 'project', label: 'Project', phone: 'name' as const, render: (r: DeploymentTableRow) => <><ProjectMark small name={r.app} slug={r.slug} icon={r.icon} /><Link to={`/projects/${r.slug}`}><strong>{r.app}</strong></Link></> }] : []),
    { key: 'deployment', label: 'Deployment', phone: withApp ? undefined : 'name', render: r => <><Link to={deploymentHref(r)}>{withApp ? r.label : <strong>{r.label}</strong>}</Link>{r.url && <> <OpenApp url={r.url} hostOnly={r.hostOnly} label={r.label} /></>}{r.targetName && <span className="record-phone muted"> · {r.targetName}</span>}</> },
    ...(release ? [{ key: 'release', label: 'Release', className: 'fleet-wraps', render: release }] : []),
    { key: 'target', label: 'Target', kind: 'technical', wideOnly: true, className: 'fleet-roomy', render: r => r.targetName ? <Link to={`/targets/${encodeURIComponent(r.targetName)}`}>{r.targetName}</Link> : <span className="muted">—</span> },
    { key: 'status', label: 'Status', phone: 'state', render: r => <RuntimeStatus observation={r} /> },
    { key: 'resources', label: 'Resources', className: 'fleet-roomy', render: r => <Usage row={r} /> },
    ...(backups ? [{ key: 'backup', label: 'Last backup', kind: 'time' as const, render: (r: DeploymentTableRow) => <LastBackup row={r} /> }] : []),
    { key: 'more', label: '', phone: 'action', render: r => <RowMenu row={r} /> },
  ]
  return <div className="fleet"><ListTable label="Deployments" className={`fleet-table${withApp ? ' fleet-table-apps' : ''}${release ? ' fleet-table-releases' : ''}`} columns={columns} rows={rows} rowKey={r => `${r.slug}/${r.deployment}`} rowTo={deploymentHref} /></div>
}

// OpenApp is where a deployment answers, as a small link that opens it; the
// address is its title. One that answers on AppTown's host only is not a
// link, which would fail for anyone elsewhere: its title says where it works.
function OpenApp({ url, hostOnly, label }: { url: string; hostOnly?: boolean; label: string }) {
  if (hostOnly) return <span className="muted fleet-open" title={hostOnlyWords(url)}>↗</span>
  return <a className="fleet-open" href={url} target="_blank" rel="noopener noreferrer" title={url} aria-label={`Open ${label}: ${url}`}>↗</a>
}

// What a deployment uses now, from its host's reading: one read per host,
// shared by every row on it. A host that is not read (a cloud platform, or
// a reader without access to hosts) leaves it unknown, never zero.
function Usage({ row }: { row: DeploymentTableRow }) {
  const q = useGet<TargetCapacity>(row.targetName ? `/targets/${encodeURIComponent(row.targetName)}/capacity` : null, { poll: 30000 })
  const t = q.data?.tenants?.find(t => t.kind === 'deployment' && t.project === row.slug && t.deployment === row.deployment)
  if (!t || q.refreshFailed || t.stale || !t.used.known) return <span className="muted" title={t?.used.reason || q.reason || undefined}>—</span>
  const cpu = t.cpuUsed?.known ? `${Number(t.cpuUsed.value.toFixed(2))} cores · ` : ''
  return <span className="fleet-usage">{cpu}{fmtBytes(t.used.value)}</span>
}

// The deployment's newest backup, from the fleet's one backups read. A static
// site holds no data, so it has nothing to back up rather than no backup.
function LastBackup({ row }: { row: DeploymentTableRow }) {
  const q = useGet<AllBackups>(row.static ? null : '/backups', { poll: 30000 })
  if (row.static) return <span className="muted">no data</span>
  if (!Array.isArray(q.data?.backups)) return <span className="muted" title={q.reason || undefined}>—</span>
  const last = q.data.backups.filter(b => b.project === row.slug && b.deployment === row.deployment && b.status !== 'pruned')
    .sort((a, b) => b.takenAt.localeCompare(a.takenAt))[0]
  // After a failed refresh the last answer stays, said as old: that backup was
  // taken, but whether it is still the newest is not known.
  const old = q.refreshFailed ? <span className="muted small"> · old reading</span> : null
  if (!last) return <span className="muted">none{old}</span>
  return <span className={last.status === 'failed' ? 'error' : undefined} title={fmtStamp(last.takenAt)}>{last.status === 'failed' ? 'failed · ' : ''}{fmtWhen(last.takenAt)}{old}</span>
}

function RowMenu({ row }: { row: DeploymentTableRow }) {
  const backups = useNotOff('backups')
  const operate = useHolds('deployment:operate', row.slug)
  const deploy = useHolds('deployment:deploy', row.slug)
  const kind = useGet<TargetCapacity>(row.targetName ? `/targets/${encodeURIComponent(row.targetName)}/capacity` : null, { poll: 30000 }).data?.kind
  const base = `/projects/${row.slug}/deployments/${row.deployment}`
  const { run, busy, err } = useAction(undefined, { affects: ['/tiers', `/projects/${row.slug}/deployments`] })
  const items: MoreItem[] = [
    ...(operate && kind && kind !== 's3-bucket' ? [{ label: row.observation === 'stopped' ? 'Start' : 'Restart', disabled: busy, onClick: () => run(() => post(`${base}/${row.observation === 'stopped' ? 'start' : 'restart'}`, {})) }] : []),
    ...(deploy && backups && kind === 'docker-host' ? [{ label: 'Back up now', disabled: busy, onClick: () => run(() => post(`${base}/backups`, {})) }] : []),
    ...(operate ? [{ label: 'Refresh status', disabled: busy, onClick: () => run(() => post(`${base}/check`, {})) }] : []),
  ]
  if (items.length === 0) return null
  return <span className="row row-2">{err && <span className="error small" role="alert" title={err}>failed</span>}<MoreMenu label={`${row.app} ${row.label}`} items={items} /></span>
}
