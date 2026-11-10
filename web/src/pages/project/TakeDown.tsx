import { useState } from 'react'
import { Link } from 'react-router-dom'
import { del } from '../../api'
import { useAction, useGet } from '../../data'
import { Modal } from '../../Modal'
import { PRODUCT } from '../../brand'
import { fmtBytes } from '../../format'
import { deploymentName } from '../../deploymentName'
import type { DeploymentProtection, DeploymentRemoved, DeploymentView, OperationAdmitted } from '../../wire.gen'

export function tearDownWords(kind?: string) {
  switch (kind) {
    case 'do-app':
      return 'The app is deleted first, so nothing writes, and so are its images when no other deployment of this project on that account uses them. With a backup store attached and a copy runner named, a final copy of its database goes to the store and is checked by filling a new database from it; only then is the database cluster deleted, and the copy is kept 30 days to start a new deployment from. If the copy falls short, the cluster stays. When no final copy can be taken (no store, no copy runner), the take-down is refused unless you choose to go without one, and the cluster stays.'
    case 'fly-machines':
      return `Its app parts stop first. If it holds data, a final backup goes to the backup store before the Fly app and its volumes are deleted. Without a store, choose to keep the app and its volumes with every Machine stopped. A Machine or volume ${PRODUCT} did not make stops the take-down.`
    case 's3-bucket':
      return 'The bucket and every file in it are deleted, and the site stops answering. A static site holds no data, so there is nothing to back up.'
    case 'portainer':
      return `Its data is backed up first, through Portainer; then the stack ${PRODUCT} made and the volumes its deploys made are deleted. A volume that was there before its first deploy is copied and kept. When ${PRODUCT} cannot reach the environment for its data, the take-down is refused unless you choose to go without a final copy, and the volumes stay.`
    default:
      return `Its data is backed up first; then everything ${PRODUCT} made for it is deleted. Stop anything outside this deployment that writes to its data before you go on.`
  }
}

// TakeDown asks whether to take a deployment down, saying what that does to
// its data from AppTown's current answer. A cloud deployment's answer is read
// when the question opens (a cached one is not current), and only a current
// answer may state what confirming means: a stale or failed read could offer the choice to go
// without a final copy after one became possible, or the other way round.
// When no final copy can be taken, confirming is that explicit choice, and
// only then does the request carry it, naming the deployment.
export function TakeDown({ slug, env: e, onClose }: { slug: string; env: DeploymentView; onClose: (done?: DeploymentRemoved | OperationAdmitted) => void }) {
  const { run, busy, err } = useAction(undefined, { affects: [`/projects/${slug}/deployments`] })
  const holdsData = e.target?.kind === 'fly-machines' || (e.target?.kind === 'do-app' && !!e.needs && e.needs.length > 0)
  const prot = useGet<DeploymentProtection>(holdsData ? `/projects/${slug}/deployments/${e.name}/protection` : null, { poll: 5000 })
  const [torn, setTorn] = useState<DeploymentRemoved | OperationAdmitted | null>(null)
  const known = !holdsData || (prot.current && !!prot.data)
  const noFinalCopy = known && !!prot.data?.takeDown && !prot.data.operations?.finalCopy.available
  const tearDown = async () => {
    const r = await run(() => del<DeploymentRemoved | OperationAdmitted>(`/projects/${slug}/deployments/${e.name}`, noFinalCopy ? { withoutFinalCopy: e.name } : undefined))
    if (r) setTorn(r)
  }
  const name = deploymentName(e)
  if (torn) return <Modal title={`${name} is being taken down`} onClose={() => onClose(torn)} actions={<button className="primary" onClick={() => onClose(torn)}>Done</button>}>
    {'operationId' in torn
      ? <p>Taking it down: the app goes first so nothing writes, then a final copy is taken and checked before the database cluster is deleted. <Link to={`/operations/${torn.operationId}`}>Follow it</Link>.</p>
      : <p>
        Taken down.
        {torn.finalBackup && <> Its data is in a final backup ({fmtBytes(torn.finalBackup.bytes)}), kept with the project.</>}
        {torn.deleted.length > 0 && <> Deleted: {torn.deleted.join('; ')}.</>}
        {torn.orphans.length > 0 ? <> Left at the target: {torn.orphans.join('; ')}.</> : ' Nothing was left behind.'}
      </p>}
  </Modal>
  return <Modal title={`Take down ${name}?`} onClose={() => onClose()} actions={<>
    <button className="danger" disabled={!known || busy} onClick={tearDown}>{noFinalCopy ? 'Take it down without a final copy' : 'Take it down'}</button>
    <button className="subtle" onClick={() => onClose()}>Cancel</button>
  </>}>
    {known
      ? <p>{prot.data?.takeDown || tearDownWords(e.target?.kind)}{e.aiModels?.length ? ' Its AI models key is revoked.' : ''}</p>
      : <p className="muted">Reading what a take-down would do to its data…</p>}
    {err && <p className="error" role="alert">{err}</p>}
  </Modal>
}
