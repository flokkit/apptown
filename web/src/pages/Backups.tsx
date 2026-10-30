import { Status, STATUS_LIGHT } from '../Status'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { post } from '../api'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { fmtBytes, fmtWhen } from '../format'
import { QuietConfirm } from '../QuietConfirm'
import type { AllBackups, BackupItem, BackupTaken, OperationAdmitted, ProjectBackup } from '../wire.gen'
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
  const { data, reload } = q
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
      <div className="muted small">
        Keeps newest {data.keep} · 14 for production · final sets until their date
      </div>
      <div className="row" style={{ gap: 10, alignItems: 'center', minHeight: 24 }}>
        {note && <span className="small">{note.done ? '✓ ' : '! '}{note.text}{note.operation && <> <Link to={`/operations/${note.operation}`}>Follow it</Link></>}</span>}
        {actErr && <span className="error small">{actErr}</span>}
      </div>
      {latest.size === 0 && <div className="muted">No backups yet. The first is taken within a day.</div>}
      <h3 style={{ margin: '12px 0 6px' }}>Latest per deployment</h3>
      <table className="small stack">
        <thead><tr><th>Project</th><th>Deployment</th><th>Kept on</th><th>Latest</th><th>Status</th><th>Contents</th><th>Size</th><th>Trigger</th><th></th></tr></thead>
        <tbody>
          {[...latest.values()].map((b) => (
            <tr key={b.id}>
              <td className="stack-lead"><Link to={`/projects/${b.project}`}>{b.project}</Link></td>
              <td data-label="deployment">{b.deployment}</td>
              <td data-label="store"><KeptOn b={b} /></td>
              <td data-label="taken" title={b.takenAt}>{fmtWhen(b.takenAt)}</td>
              <td className="stack-end"><Status word={b.statusLabel} tone={STATUS_LIGHT[b.statusLabel]} help={b.error || b.status} /></td>
              <td data-label="contents"><Contents items={b.items} /></td>
              <td data-label="size">{fmtBytes(b.bytes)}</td>
              <td data-label="trigger">{b.trigger}</td>
              <td className="row" style={{ gap: 6 }}>
                {managed(b)
                  ? b.trigger === 'take-down' && <Link className="small" to={`/projects/${b.project}/settings/deployments`}>Start a new deployment from this</Link>
                  : b.trigger === 'take-down'
                  ? <RestoreInto b={b} busy={busy} onRestore={(into) => restore(b, into)} />
                  : <>
                    <button className="subtle" disabled={busy} onClick={() => takeNow(b.project, b.deployment)}>Back up now</button>
                    <RestoreOver b={b} onYes={() => restore(b)} />
                  </>}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <h3 style={{ margin: '18px 0 6px' }}>All sets</h3>
      <table className="small stack">
        <thead><tr><th>project</th><th>deployment</th><th>kept on</th><th>taken</th><th>status</th><th>size</th><th>trigger</th><th>by</th><th></th></tr></thead>
        <tbody>
          {data.backups.map((b) => (
            <tr key={b.id} className={b.status === 'pruned' ? 'muted' : ''}>
              <td className="stack-lead">{b.project}</td>
              <td data-label="deployment">{b.deployment}</td>
              <td>{b.status === 'pruned' ? '' : <><KeptOn b={b} /><details className="small"><summary>Location</summary><code>{setPlace(b, data.where)}</code></details></>}</td>
              <td data-label="taken" title={b.takenAt}>{fmtWhen(b.takenAt)}</td>
              <td className="stack-end"><Status word={b.statusLabel} tone={STATUS_LIGHT[b.statusLabel]} help={b.error || b.status} /></td>
              <td>{b.status === 'pruned' ? '' : fmtBytes(b.bytes)}</td>
              <td data-label="trigger">{b.trigger}</td>
              <td data-label="by">{b.actor}</td>
              <td>{b.status !== 'pruned' && b.status !== 'failed' && !managed(b) && (b.trigger === 'take-down'
                ? <RestoreInto b={b} busy={busy} onRestore={(into) => restore(b, into)} />
                : <RestoreOver b={b} onYes={() => restore(b)} />
              )}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// itemBadge is how an item was copied, as a fact: a database's own dump, a
// service's own copy, or files, which a running service may change mid-copy.
function itemBadge(i: BackupItem): { text: string; tone: string } | null {
  if (i.error) return { text: 'not copied', tone: 'fail' }
  if (i.copy === 'declared') return { text: i.format ? 'database dump' : "service's own copy", tone: 'pass' }
  if (i.copy === 'files') return { text: 'file copy, app stopped', tone: 'pass' }
  if (i.copy === 'live-files') return { text: 'file copy · may be inconsistent', tone: 'pending' }
  return null
}

// Contents is a set's items, each with its badge and what its copy leaves out.
function Contents({ items }: { items: BackupItem[] }) {
  return <>{items.map((i) => {
    const badge = itemBadge(i)
    return (
      <div key={i.kind + i.name} title={i.error || i.how}>
        {itemName(i)} {badge && <span className={`chip ${badge.tone}`}>{badge.text}</span>}
        {i.caveat && <div className="muted small">not in it: {i.caveat}</div>}
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
  return <QuietConfirm label={`Restore over ${b.deployment}`} ask={`Restore ${b.project}/${b.deployment} from the set taken ${fmtWhen(b.takenAt)}? A safety set is taken first; the app is stopped while it is replaced.${caveatNote(b.items)}`}
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
      <input value={into} onChange={(e) => setInto(e.target.value.trim())} placeholder="a deployment" style={{ width: 120 }}
        title={`a deployment of ${b.project}; what is there is copied to a safety set first, then replaced`} />
      <button className="subtle" disabled={busy || into === ''} onClick={() => onRestore(into)}>Restore over {into || '…'}</button>
      {caveatNote(b.items) && <span className="muted small">{caveatNote(b.items).trim()}</span>}
    </span>
  )
}
