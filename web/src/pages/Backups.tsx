import { Section } from '../Section'
import { ListTable } from '../ListTable'
import { Status } from '../Status'
import { useEffect, useState } from 'react'
import { Link, useLocation } from 'react-router-dom'
import { useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { fmtBytes, fmtWhen } from '../format'
import type { BackupStoreState, AllBackups, BackupItem, ProjectBackup } from '../wire.gen'
import { StoreSection } from './DataProtection'

// Backups: what AppTown holds of each deployment's state, newest first.
// Taken daily, after every release, and on demand; the newest seven stay.
// A take-down's final set stays with its project until the project is
// purged. The page lists; acts belong to the deployment: Back up now and
// Restore from a backup are on its ⋯ menu, and a new deployment starts
// from a copy in the project's new-deployment form. A row opens the
// deployment, or the project's deployments when it was taken down.


const itemName = (i: { kind: string; name: string }) => i.kind === 'volume' ? 'data volume' : i.name

// caveatNote is what a set's copies do not hold, as each need's kind
// declares it (object storage's ACLs and versions), for the question before a restore:
// a known loss is said where the decision is made. Empty when none.
export function caveatNote(items: { kind: string; name: string; caveat?: string }[]): string {
  const said = items.filter((i) => i.caveat).map((i) => `${itemName(i)}: ${i.caveat}`)
  return said.length ? ` Not in this set · ${said.join('; ')}.` : ''
}

export function BackupsView() {
  const q = useGet<AllBackups>('/backups', { poll: 30000 })
  // Retention means something only once copies have somewhere to go.
  const store = useGet<BackupStoreState>('/backup-store')
  const { data } = q
  const [historyCount, setHistoryCount] = useState(10)
  const { hash } = useLocation()
  const linkedIndex = data?.backups.findIndex(b => `#backup-${b.id}` === hash) ?? -1
  const shown = Math.max(historyCount, linkedIndex + 1)
  useEffect(() => { if (linkedIndex >= 0) document.getElementById(hash.slice(1))?.scrollIntoView?.() }, [hash, linkedIndex])
  if (!data) return <ReadStatus q={q} what="the backups" />
  const latest = new Map<string, ProjectBackup>()
  for (const b of data.backups) { const k = b.project + '/' + b.deployment; if (!latest.has(k) && b.status !== 'pruned') latest.set(k, b) }
  // A set whose deployment was taken down has no deployment to open, of
  // whatever kind it is: its project's deployments, where one starts from it.
  const opens = (b: ProjectBackup) => b.deploymentRemoved ? `/projects/${b.project}/deployments` : `/projects/${b.project}/deployments/${b.deployment}`

  return (
    <div>
      <StaleNote q={q} />
      <StoreSection canAttach />
      <Section title="Latest backups" count={latest.size || undefined} note={store.data?.store ? data.retention : undefined}>
      {latest.size === 0 ? <p className="muted small">No backups yet.</p> : <>
      <ListTable label="Latest backups" className="small" rows={[...latest.values()]} rowKey={b => b.id ?? `${b.project}/${b.deployment}/${b.takenAt}`} rowTo={opens} columns={[
        { key: 'project', label: 'Project', phone: 'name', render: b => <Link to={`/projects/${b.project}`}>{b.project}</Link> },
        { key: 'deployment', label: 'Deployment', wideOnly: true, className: 'record-words', render: b => <Link to={opens(b)}>{b.deploymentLabel || b.deployment}</Link> },
        { key: 'state', label: 'State', phone: 'state', render: b => <Status word={b.statusLabel} help={b.error || undefined} /> },
        { key: 'taken', label: 'Taken', kind: 'time', render: b => fmtWhen(b.takenAt) },
        { key: 'details', label: 'Details', render: b => <>
          <p>{b.deploymentLabel || b.deployment} · <KeptOn b={b} /> · {fmtBytes(b.bytes)}</p>
          <Contents items={b.items} />
        </> },
      ]} />
      </>}
      </Section>
      {data.backups.length > 0 && <Section title="All backups" count={data.backups.filter(b => b.statusLabel !== 'removed').length} note={removedNote(data.backups)}>
        <ListTable label="All backups" className="small" rows={data.backups.slice(0, shown)} rowKey={b => b.id ?? `${b.project}/${b.deployment}/${b.takenAt}`} rowId={b => `backup-${b.id}`} rowTo={opens} columns={[
          { key: 'project', label: 'Project', phone: 'name', render: b => b.project },
          { key: 'deployment', label: 'Deployment', wideOnly: true, className: 'record-words', render: b => b.deploymentLabel || b.deployment },
          { key: 'state', label: 'State', phone: 'state', render: b => <Status word={b.statusLabel} help={b.error || (b.status === 'pruned' ? 'This old copy has been removed.' : undefined)} /> },
          { key: 'taken', label: 'Taken', kind: 'time', render: b => fmtWhen(b.takenAt) },
          { key: 'details', label: 'Details', render: b => <>{b.deploymentLabel || b.deployment} · {b.beforeRelease ? <Link to={`/projects/${b.project}/releases/${b.beforeRelease}`}>before release {b.beforeRelease}</Link> : b.trigger} · {b.actor}{b.status === 'pruned' ? <p>Copy removed</p> : <>
            {' · '}<KeptOn b={b} /><section><h3>Location</h3><code>{setPlace(b, data.where)}</code></section>
          </>}</> },
        ]} />
      </Section>}
      {data.backups.length > shown && <button style={{ marginTop: 10 }} onClick={() => setHistoryCount(shown + 10)}>Show 10 more</button>}

    </div>
  )
}

// The heading counts the copies kept; sets retention removed stay listed
// for their record and are counted apart.
function removedNote(backups: ProjectBackup[]) {
  const removed = backups.filter(b => b.statusLabel === 'removed').length
  return removed > 0 ? `${removed} removed by the keep policy` : undefined
}

// itemBadge is how an item was copied, as a fact: a database's own dump, a
// service's own copy, or files, which a running service may change mid-copy.
function itemBadge(i: BackupItem): string | null {
  if (i.error) return 'not copied'
  if (i.copy === 'declared') return i.format ? 'database copy' : "service's own copy"
  if (i.copy === 'files') return 'file copy, app stopped'
  if (i.copy === 'live-files') return 'file copy · may be inconsistent'
  return null
}

// Contents is a set's items, each with its badge and what its copy leaves out.
function Contents({ items }: { items: BackupItem[] }) {
  return <>{items.map((i) => {
    const badge = itemBadge(i)
    return (
      <div key={i.kind + i.name} title={i.error || i.how}>
        {itemName(i)} {badge && <span className="badge">{badge}</span>}
        {i.caveat && <section className="muted small"><h3>Limits</h3>{i.caveat}</section>}
      </div>
    )
  })}</>
}

// KeptOn is where a set is kept: the backup store, or this host, which it
// does not outlive; a final set also says it is kept with the project.
function KeptOn({ b }: { b: ProjectBackup }) {
  return <>
    {b.store === 'store' ? 'backup store' : <span title="lost with this host's disk">this host ⚠</span>}
    {b.trigger === 'take-down' && b.status !== 'pruned' && <div className="muted small">kept with the project</div>}
  </>
}

// setPlace is a set's <project>/<deployment>/<time>, the same in the
// store and under the backups folder on this host: a folder left on the
// disk is matched to its set by it.
const setPlace = (b: ProjectBackup, where: string) => b.store === 'store' ? b.path : b.path.startsWith(where + '/') ? b.path.slice(where.length + 1) : b.path

// managedCopy is a copy of a managed database: never restored over what
// is there, only started from. A Docker host's set is restored in place
// wherever it is kept, on AppTown's disk or in the backup store.
export const managedCopy = (b: ProjectBackup) => b.items.some((i) => !!i.instance)
