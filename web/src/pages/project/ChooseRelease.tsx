import { PRODUCT } from '../../brand'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { post, type ChangeView } from '../../api'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { Modal } from '../../Modal'
import type { DeploymentBackups, DeploymentProtection, DeploymentView, ReleaseBundle, ReleaseDetail, ReleaseListed } from '../../wire.gen'
import { fmtStamp } from '../../format'

export type ReleaseChoice = { release: ReleaseListed } | { bundle: ReleaseBundle } | { before: ReleaseListed }

export function ChooseRelease({ slug, name, displayLabel, releases, current, choice, onChoose }: { slug: string; name: string; displayLabel?: string; releases: ReleaseListed[]; current: boolean; choice: ReleaseChoice | 'list' | null; onChoose: (choice: ReleaseChoice | 'list' | null) => void }) {
  const bundles = useGet<ReleaseBundle[]>(`/projects/${slug}/release-bundles`)
  const deployments = useGet<DeploymentView[]>(`/projects/${slug}/deployments`)
  const deployment = deployments.data?.find(d => d.name === name)
  const label = displayLabel || deployment?.displayLabel || name
  const deploymentLabel = (key: string) => deployments.data?.find(d => d.name === key)?.displayLabel || key
  const running = releases.find(r => r.deployment === name && r.state === 'released')
  return <>
    <button onClick={() => onChoose('list')} disabled={!current}>Deploy a release…</button>
    {choice === 'list' && <Modal title="Deploy a release…" onClose={() => onChoose(null)}>
      <p>Choose the code to run on {label}. Each deployment has its own release and approval.</p>
      <ReadStatus q={bundles} what="release bundles" /><StaleNote q={bundles} />
      <ReadStatus q={deployments} what="deployments" /><StaleNote q={deployments} />
      {bundles.data?.map(bundle => {
        const ran = releases.filter(r => r.bundleId === bundle.id && r.state === 'released')
        // The first release records the changes new in this bundle; a later
        // redeploy can have no new changes on its destination.
        const selected = ran.at(-1)
        const now = [...new Set(ran.filter(r => r.serving).map(r => r.deployment))]
        const uncertain = [...new Set(ran.filter(r => r.observed && releases.find(latest => latest.deployment === r.deployment && latest.state === 'released')?.id === r.id).map(r => r.deployment))]
        const before = [...new Set(ran.filter(r => !r.serving && !now.includes(r.deployment) && !uncertain.includes(r.deployment)).map(r => r.deployment))]
        return <div className="card stack stack-2" key={bundle.id}>
          <div><strong>{selected ? `Release ${selected.number}` : 'Unreleased bundle'}</strong> · built {fmtStamp(bundle.createdAt)}</div>
          {selected ? selected.changes.map(c => <div key={c.id}><Link to={`/changes/${c.id}`}>{c.title}</Link></div>) : <BundleChange bundle={bundle} />}
          {now.length > 0 && <div>Runs on {now.map(deploymentLabel).join(', ')}.</div>}
          {uncertain.length > 0 && <div>Last deployed on {uncertain.map(deploymentLabel).join(', ')}; current state unknown.</div>}
          {before.length > 0 && <div>Previously ran on {before.map(deploymentLabel).join(', ')}.</div>}
          {ran.length === 0 && <div>Has not run on a deployment.</div>}
          <code className="small muted">{bundle.contractRevision.slice(0, 12)}</code>
          <div><button disabled={!current || !bundles.current || !deployments.current} onClick={() => onChoose(selected ? { release: selected } : { bundle })}>Deploy</button></div>
        </div>
      })}
      {bundles.data?.length === 0 && <p>No release bundles yet.</p>}
    </Modal>}
    {choice && choice !== 'list' && <ConfirmRelease key={JSON.stringify(choice)} slug={slug} name={name} label={label} deployment={deployment} choice={choice} running={running} current={current && deployments.current} onClose={() => onChoose(null)} />}
  </>
}

function ConfirmRelease({ slug, name, label, deployment, choice, running, current, onClose }: { slug: string; name: string; label: string; deployment?: DeploymentView; choice: ReleaseChoice; running?: ReleaseListed; current: boolean; onClose: () => void }) {
  const detail = useGet<ReleaseDetail>(running ? `/projects/${slug}/releases/${running.number}` : null)
  const protection = useGet<DeploymentProtection>(running && deployment?.target && ['docker-host', 'do-app'].includes(deployment.target.kind) ? `/projects/${slug}/deployments/${name}/protection` : null)
  const takesBackup = protection.current && !protection.data?.unknown && !protection.data?.reading?.unknown && !protection.data?.reading?.stale && !!protection.data?.items.length && protection.data.operations?.copy.available
  const { run, busy, err } = useAction()
  const [accepted, setAccepted] = useState<number>()
  const paired = 'before' in choice
  const code = 'release' in choice ? `release ${choice.release.number}` : 'this bundle'
  const title = paired ? `Roll back to before release ${choice.before.number}` : 'release' in choice ? `Deploy release ${choice.release.number} again` : 'Deploy this bundle'
  const confirm = async () => {
    const body = 'before' in choice ? { beforeRelease: choice.before.number } : 'release' in choice ? { releaseNumber: choice.release.number } : { bundleId: choice.bundle.id }
    const result = await run(() => post<{ number: number }>(`/projects/${slug}/deployments/${name}/deploy`, body))
    if (result) setAccepted(result.number)
  }
  return <Modal title={title} onClose={onClose}>
    {accepted ? <p role="status">Release {accepted} requested. Its approval and progress appear on this deployment.</p> : <>
      {paired ? <p>{PRODUCT} takes a safety copy, stops the app and restores this backup. It leaves the app stopped until the selected earlier release starts. If that release fails before starting, the restored data stays and the app stays stopped.</p> : <>
        <p>This deploys {code}'s code to {label}. Your data stays as it is now; {PRODUCT} does not undo data changes or migrations.</p>
        <p>{PRODUCT} can't tell whether the older code works with today's data. It may run its own start-up steps.</p>
        {running ? <><ReadStatus q={detail} what="the current release's backup" /><StaleNote q={detail} />
          {detail.data?.backup ? <p>Backup taken before release {running.number}: <Link to={`/backups#backup-${detail.data.backup.id}`}>{detail.data.backup.id} · {detail.data.backup.status}</Link></p> : detail.data && <p>No backup was taken before release {running.number}: {noBackupReason(detail.data.backupReason)}.</p>}</> : <p>No earlier successful release is recorded on {label}, so there is no backup from one.</p>}
      </>}
      {deployment?.approvalRole && <p>Needs approval from {deployment.approvalRole}.</p>}
      {!paired && takesBackup && <p>A backup is taken first.</p>}
      {deployment?.feed === 'main' && <p>The next change that lands deploys over this release.</p>}
      {err && <p role="alert">{err}</p>}
      <button className="primary" disabled={busy || !current || (!paired && !!running && !detail.current)} onClick={confirm}>{paired ? `Roll back to before release ${choice.before.number} on ${label}` : `Deploy ${code} to ${label}`}</button>
    </>}
  </Modal>
}

// The paired door belongs to the pre-deploy backup, and requires recorded
// backup-time release evidence rather than adjacent release numbers.
export function RollbackBefore({ slug, name, release, current, onChoose }: { slug: string; name: string; release: ReleaseListed; current: boolean; onChoose: () => void }) {
  const backups = useGet<DeploymentBackups>(`/projects/${slug}/deployments/${name}/backups`)
  const backup = backups.data?.backups.find(b => b.id === release.backupId)
  return backup?.runningReleaseId && backup.status === 'ok' ? <button disabled={!current || !backups.current} onClick={onChoose}>Roll back to before release {release.number}</button> : null
}

function noBackupReason(reason?: string) {
  return (reason || 'the reason was not recorded').replace(/^no copy (?:was|could be) taken before this release: /, '').replace(/[.]$/, '')
}

function BundleChange({ bundle }: { bundle: ReleaseBundle }) {
  const change = useGet<ChangeView>(bundle.builtFrom?.changeId ? `/changes/${bundle.builtFrom.changeId}` : null)
  return <><ReadStatus q={change} what="the bundle's change" /><StaleNote q={change} />{change.data && <Link to={`/changes/${change.data.change.id}`}>{change.data.change.title}</Link>}</>
}
