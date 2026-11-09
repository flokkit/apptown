import { useState } from 'react'
import { post } from '../../api'
import { useAction, useGet } from '../../data'
import { Modal } from '../../Modal'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { fmtBytes, fmtWhen } from '../../format'
import { deploymentName } from '../../deploymentName'
import { caveatNote, managedCopy } from '../Backups'
import type { AllBackups, DeploymentView, OperationAdmitted, ProjectBackup } from '../../wire.gen'

// restorable is what a deployment can be restored from: its own copies,
// and the final copies kept from the project's taken-down deployments.
// A managed database's copy is never restored over what is there; a new
// deployment starts from it instead.
export function restorable(all: ProjectBackup[], slug: string, name: string): ProjectBackup[] {
  return all.filter(b => b.project === slug && (b.status === 'ok' || b.status === 'partial') && !managedCopy(b) &&
    (b.deployment === name || b.trigger === 'take-down'))
}

// RestoreFromBackup is where a deployment is restored: pick a copy, read
// what restoring does, confirm. A safety copy is taken first; the app is
// stopped while its data is replaced.
export function RestoreFromBackup({ slug, env, onClose }: { slug: string; env: DeploymentView; onClose: (operationId?: string) => void }) {
  const q = useGet<AllBackups>('/backups')
  const [picked, setPicked] = useState<string | null>(null)
  const { run, busy, err } = useAction(undefined, { affects: ['/backups', `/projects/${slug}/deployments`] })
  const name = deploymentName(env)
  const copies = q.data ? restorable(q.data.backups, slug, env.name) : []
  const chosen = copies.find(b => b.id === picked)
  const restore = async (b: ProjectBackup) => {
    const r = await run(() => post<OperationAdmitted>(`/projects/${slug}/deployments/${env.name}/backups/${b.id}/restore`, { confirm: env.name }))
    if (r) onClose(r.operationId ?? undefined)
  }
  return <Modal title={`Restore ${name} from a backup`} onClose={() => onClose()} actions={<>
    <button className="danger" disabled={!chosen || busy || !q.current} onClick={() => chosen && void restore(chosen)}>Restore</button>
    <button onClick={() => onClose()}>Cancel</button>
  </>}>
    <ReadStatus q={q} what="the backups" /><StaleNote q={q} />
    {q.data && copies.length === 0 && <p className="muted">No copy to restore {name} from.</p>}
    {copies.length > 0 && <fieldset className="stack stack-2 restore-copies">
      <legend className="muted">Copy</legend>
      {copies.map(b => <label key={b.id} className="row row-2">
        <input type="radio" name="copy" checked={picked === b.id} onChange={() => setPicked(b.id)} />
        <span>{fmtWhen(b.takenAt)} · {b.beforeRelease ? `before release ${b.beforeRelease}` : b.trigger} · {fmtBytes(b.bytes)} · {b.store === 'store' ? 'backup store' : 'this host'}
          {b.deployment !== env.name && <span className="muted"> · from {b.deploymentLabel || b.deployment}, taken down</span>}
          {b.status === 'partial' && <span className="muted"> · partial</span>}</span>
      </label>)}
    </fieldset>}
    {chosen && <p>Restore {name} from the copy taken {fmtWhen(chosen.takenAt)}? A safety copy of what is there now is taken first; the app is stopped while its data is replaced.{caveatNote(chosen.items)}</p>}
    {err && <p className="error" role="alert">{err}</p>}
  </Modal>
}
