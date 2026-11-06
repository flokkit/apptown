import { Section } from '../Section'
import { GlanceRow } from '../GlanceRow'
import { Status } from '../Status'
import { useEffect, useState } from 'react'
import { Link, useLocation } from 'react-router-dom'
import { post } from '../api'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { fmtBytes, fmtWhen } from '../format'
import { QuietConfirm } from '../QuietConfirm'
import type { BackupStoreState, AllBackups, BackupItem, BackupTaken, OperationAdmitted, ProjectBackup } from '../wire.gen'
import { StoreSection } from './DataProtection'

// Backups: what AppTown holds of each deployment's state, newest first.
// Taken daily, after every release, and on demand; the newest seven stay.
// A take-down's final set stays with its project until the project is
// purged, and restores into any deployment of the project. A restore takes a safety set first and
// replaces what is there, so it asks for the deployment's name.


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
  const { data, reload } = q
  const [historyCount, setHistoryCount] = useState(10)
  const { hash } = useLocation()
  const linkedIndex = data?.backups.findIndex(b => `#backup-${b.id}` === hash) ?? -1
  const shown = Math.max(historyCount, linkedIndex + 1)
  useEffect(() => { if (linkedIndex >= 0) document.getElementById(hash.slice(1))?.scrollIntoView?.() }, [hash, linkedIndex])
  const [note, setNote] = useState<{ text: string; done: boolean; operation?: string } | null>(null)
  const { run, busy, err: actErr } = useAction()
  if (!data) return <ReadStatus q={q} what="the backups" />
  const latest = new Map<string, ProjectBackup>()
  for (const b of data.backups) { const k = b.project + '/' + b.deployment; if (!latest.has(k) && b.status !== 'pruned') latest.set(k, b) }
  const takeNow = async (project: string, deployment: string) => {
    setNote(null)
    const r = await run(() => post<BackupTaken>(`/projects/${project}/deployments/${deployment}/backups`, {}))
    if (r?.backup) { setNote({ text: `${project}/${deployment}: ${r.backup.status}, ${fmtBytes(r.backup.bytes)}`, done: r.backup.status === 'ok' }); reload() }
  }
  // A restore runs as an operation: the answer is the operation, whose
  // page shows each step and each item's outcome as it finishes.
  const restore = async (b: ProjectBackup, into = b.deployment) => {
    setNote(null)
    const r = await run(() => post<OperationAdmitted>(`/projects/${b.project}/deployments/${into}/backups/${b.id}/restore`, { confirm: into }))
    if (r) {
      setNote({ text: `${b.project}/${into}: restore from the set taken ${fmtWhen(b.takenAt)} started.`, done: true, operation: r.operationId ?? undefined })
      reload()
    }
  }

  return (
    <div>
      <StaleNote q={q} />
      <StoreSection canAttach />
      {note && <p>{note.text}{note.operation && <> <Link to={`/operations/${note.operation}`}>Follow it</Link></>}</p>}
      {actErr && <p className="error">{actErr}</p>}
      <Section title="Latest backups" count={latest.size || undefined} note={store.data?.store ? data.retention : undefined}>
      {latest.size === 0 ? <p className="muted small">No backups yet.</p> : <>
      <table className="stack small">
        <thead><tr><th>App</th><th>State</th><th>Taken</th><th></th></tr></thead>
        <tbody>{[...latest.values()].map(b => <GlanceRow key={b.id} labels={['App', 'State', 'Taken', 'Actions']}>
          <td><Link to={`/projects/${b.project}`}>{b.project} · {b.deploymentLabel || b.deployment}</Link></td>
          <td><Status word={b.statusLabel} help={b.error || undefined} /></td>
          <td>{fmtWhen(b.takenAt)}</td>
          <td><section><h3>Actions and details</h3>
            <p>{b.deploymentLabel || b.deployment} · <KeptOn b={b} /> · {fmtBytes(b.bytes)}</p>
            <Contents items={b.items} />
            {managed(b) ? b.trigger === 'take-down' && <Link to={`/projects/${b.project}/deployments`}>Start a new deployment from this</Link>
              : b.trigger === 'take-down' ? <RestoreInto b={b} busy={busy} onRestore={into => restore(b, into)} /> : <>
                <button className="subtle" disabled={busy} onClick={() => takeNow(b.project, b.deployment)}>Back up now</button>
                <RestoreOver b={b} onYes={() => restore(b)} />
              </>}
          </section></td>
        </GlanceRow>)}</tbody>
      </table>
      </>}
      </Section>
      {data.backups.length > 0 && <Section title="All backups" count={data.backups.length}>
        <table className="stack small"><thead><tr><th>App</th><th>State</th><th>Taken</th><th>Details</th></tr></thead>
          <tbody>{data.backups.slice(0, shown).map(b => <GlanceRow id={`backup-${b.id}`} key={b.id} labels={['App', 'State', 'Taken', 'Details']}>
            <td>{b.project} · {b.deploymentLabel || b.deployment}</td>
            <td><Status word={b.statusLabel} help={b.error || (b.status === 'pruned' ? 'This old copy has been removed.' : undefined)} /></td>
            <td>{fmtWhen(b.takenAt)}</td>
            <td>{b.deploymentLabel || b.deployment} · {b.beforeRelease ? <Link to={`/projects/${b.project}/releases/${b.beforeRelease}`}>before release {b.beforeRelease}</Link> : b.trigger} · {b.actor}{b.status === 'pruned' ? <p>Copy removed</p> : <>
              <KeptOn b={b} /><section><h3>Location</h3><code>{setPlace(b, data.where)}</code></section>
              {(b.status === 'ok' || b.status === 'partial') && !managed(b) && (b.trigger === 'take-down'
                ? <RestoreInto b={b} busy={busy} onRestore={into => restore(b, into)} />
                : <RestoreOver b={b} onYes={() => restore(b)} />)}
            </>}</td>
          </GlanceRow>)}</tbody>
        </table>
      </Section>}
      {data.backups.length > shown && <button style={{ marginTop: 10 }} onClick={() => setHistoryCount(shown + 10)}>Show 10 more</button>}

    </div>
  )
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

// RestoreOver puts a set back over its own deployment, after a safety set.
function RestoreOver({ b, onYes }: { b: ProjectBackup; onYes: () => void }) {
  return <QuietConfirm label={`Restore ${b.deploymentLabel || b.deployment} from this copy`} ask={`Restore ${b.project}/${b.deploymentLabel || b.deployment} from the set taken ${fmtWhen(b.takenAt)}? A safety set is taken first; the app is stopped while it is replaced.${caveatNote(b.items)}`}
    onYes={onYes} yes="Restore" className="subtle" />
}

// setPlace is a set's <project>/<deployment>/<time>, the same in the
// store and under the backups folder on this host: a folder left on the
// disk is matched to its set by it.
const setPlace = (b: ProjectBackup, where: string) => b.store === 'store' ? b.path : b.path.startsWith(where + '/') ? b.path.slice(where.length + 1) : b.path

// managed is a copy of a managed database: never restored over what is
// there, only started from. A Docker host's set is restored in place
// wherever it is kept, on AppTown's disk or in the backup store.
const managed = (b: ProjectBackup) => b.items.some((i) => !!i.instance)

// RestoreInto restores a taken-down deployment's final set into a
// deployment the person names: typing the name is the confirmation, since
// a restore replaces what is there.
function RestoreInto({ b, busy, onRestore }: { b: ProjectBackup; busy: boolean; onRestore: (into: string) => void }) {
  const [into, setInto] = useState('')
  return (
    <span className="row" style={{ gap: 6 }}>
      <input aria-label="Restore into deployment" value={into} onChange={(e) => setInto(e.target.value.trim())} placeholder="a deployment" style={{ width: 120 }}
        title={`a deployment of ${b.project}; what is there is copied to a safety set first, then replaced`} />
      <button className="subtle" disabled={busy || into === ''} onClick={() => onRestore(into)}>Restore {into || '…'}</button>
      {caveatNote(b.items) && <span className="muted small">{caveatNote(b.items).trim()}</span>}
    </span>
  )
}
