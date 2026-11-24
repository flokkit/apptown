import { useHolds } from '../../instancePages'
import { useNotOff } from '../../features'
import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { TakeDown } from './TakeDown'
import { post } from '../../api'
import { useAction } from '../../data'
import { MoreMenu, type MoreItem } from '../../MoreMenu'
import { deploymentName } from '../../deploymentName'
import type { BackupTaken, DeploymentChecked, DeploymentView, OperationAdmitted } from '../../wire.gen'

// DeploymentMenu holds a deployment's less common acts behind ⋯, beside its
// Start / Stop / Restart. Back up now is the target's own way (useBackUp);
// a restore is Restore… on a copy on the deployment's backups page.
// useBackUp is Back up now, the target's own way, for whoever may deploy:
// the menu's item and the deployment page's backups row are this one act.
// A Docker host's and a Portainer environment's data is backed up and
// restored in place; a cloud deployment's managed database is copied, and
// only ever started from. backUp is undefined where there is no such act.
// After a Docker host's backup the deployment's backups page opens at the
// new copy.
export type BackUpSubject = { name: string; generation?: number; target?: { kind: string } | null }
export function useBackUp(slug: string, env: BackUpSubject) {
  const nav = useNavigate()
  const backups = useNotOff('backups')
  const deploy = useHolds('deployment:deploy', slug)
  const action = useAction(undefined, { affects: [`/projects/${slug}/deployments`, '/backups'] })
  const base = `/projects/${slug}/deployments/${env.name}`
  const inPlace = env.target?.kind === 'docker-host' || env.target?.kind === 'portainer'
  const backUp = !backups || !deploy ? undefined : inPlace
    ? async () => { const r = await action.run(() => post<BackupTaken>(`${base}/backups`, {})); if (r?.backup) nav(`${base}/backups#backup-${r.backup.id}`) }
    : env.target?.kind === 'do-app'
    ? async () => { const r = await action.run(() => post<OperationAdmitted>(`${base}/copies`, {})); if (r?.operationId) nav(`/operations/${r.operationId}`) }
    : undefined
  return { backUp, inPlace, ...action }
}

export function DeploymentMenu({ slug, env, current, reload, onDeployRelease }: { slug: string; env: DeploymentView; current: boolean; reload: () => Promise<void>; onDeployRelease: () => void }) {
  const nav = useNavigate()
  const backups = useNotOff('backups')
  const operate = useHolds('deployment:operate', slug)
  const deploy = useHolds('deployment:deploy', slug)
  const [takingDown, setTakingDown] = useState(false)
  const { backUp, run, busy, err } = useBackUp(slug, env)
  const base = `/projects/${slug}/deployments/${env.name}`
  const items: MoreItem[] = [
    ...(deploy ? [{ label: 'Deploy a release…', onClick: onDeployRelease, disabled: !current }] : []),
    ...(backUp ? [{ label: 'Back up now', onClick: backUp, disabled: busy || !env.generation }] : []),
    ...(operate ? [{ label: 'Refresh status', disabled: busy, onClick: () => run(async () => { await post<DeploymentChecked>(`${base}/check`, {}); await reload() }) }] : []),
    ...(backups ? [{ label: 'Backups', onClick: () => nav(`${base}/backups`) }] : []),
    // One that follows main is replaced by the next landing, not taken down.
    { label: 'Take down…', hint: env.feed === 'main' ? 'follows main: change its release feed first' : undefined, danger: true, disabled: env.feed === 'main', onClick: () => setTakingDown(true) },
  ]
  return <div className="deployment-menu row row-2">
    <MoreMenu label={deploymentName(env)} items={items} />
    {takingDown && <TakeDown slug={slug} env={env} onClose={done => {
      setTakingDown(false)
      if (done?.operationId) nav(`/operations/${done.operationId}`)
    }} />}
    {err && <span className="error small" role="alert">{err}</span>}
  </div>
}
