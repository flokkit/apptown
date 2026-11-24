import { PageBar } from '../../PageBar'
import { DeploymentRuntimeActions } from './DeploymentRuntimeActions'
import { deploymentStatus } from '../../deploymentStatus'
import type { DeploymentView } from '../../wire.gen'
import { ReleaseAssent, ReleaseWaitingReason } from './ReleaseApproval'
import { ChooseRelease, RollbackBefore, type ReleaseChoice } from './ChooseRelease'
import { useState, type ReactNode } from 'react'
import { DeploymentMenu } from './DeploymentMenu'
import { Link, useSearchParams } from 'react-router-dom'
import type { Read } from '../../data'
import { ReadStatus } from '../../ReadGate'
import type { ReleaseListed } from '../../wire.gen'
import { fmtDuration, fmtStamp } from '../../format'
import { Status } from '../../Status'
import { Section } from '../../Section'
import { DeploymentStatus } from './DeploymentStatus'
import { DeployAction } from './settings/Deployments'
import './deployment-deploys.css'

// notDeployedWords says what will put a deployment nothing has run on yet
// live, by its release feed.
export function notDeployedWords(env: DeploymentView): string {
  return env.feed === 'main'
    ? env.feedConfirmed ? 'It deploys when a change lands on main.' : 'It will deploy when a change lands on main, once someone saves its release feed in Settings.'
    : env.feed === 'promotion' ? `It deploys the release ${env.promotionSource || 'its source deployment'} last ran, when someone promotes it.`
    : 'Deploy a release to put it live.'
}

// DeploymentTitle is a deployment's bar: its project's name and its own
// (the project's muted, as context), its status and whether it is
// protected, and the page's actions.
export function DeploymentTitle({ project, name, env, actions }: { project?: string; name: string; env: DeploymentView; actions?: ReactNode }) {
  return <PageBar title={project ? <>{project} <span className="title-context">{name}</span></> : name}
    after={<><Status observation {...deploymentStatus(env)} />{env.protected && <span className="badge" title="every release waits for a person with release rights">protected</span>}</>} actions={actions} />
}

// Every deploy is a release request, including requests from a landing.
// tab picks what shows under the heading: the deployment now (status), its
// deploy history, or its settings; tabs is the strip that switches them.
// releases is the project's release list, which the page reads beside the
// deployment rather than after it.
export type DeploymentTab = 'status' | 'history' | 'settings'
type DeployHistoryProps = { slug: string; name: string; project?: string; url?: string; displayLabel?: string; env: DeploymentView; refreshFailed?: boolean; releases: Read<ReleaseListed[]>; current: boolean; reload: () => Promise<void>; settings?: ReactNode; tabs?: ReactNode; tab?: DeploymentTab }
export function DeploymentDeploys(props: DeployHistoryProps) {
  return <DeploymentHistory key={`${props.slug}/${props.name}/${props.env.id}`} {...props} />
}
function DeploymentHistory({ slug, name, project, displayLabel, env, refreshFailed = false, releases, current, reload, settings, tabs, tab = 'status' }: DeployHistoryProps) {
  const [search, setSearch] = useSearchParams()
  const [pending, setPending] = useState<string>()
  const shown = pending ? { ...env, runtimeState: pending } : env
  const [choice, setChoice] = useState<ReleaseChoice | 'list' | null>(null)
  const [count, setCount] = useState(10)
  const history = releases.data?.filter(r => r.deployment === name) ?? []
  return <>
    <DeploymentTitle project={project} name={displayLabel || name} env={env} actions={<div className="deployment-page-actions">
      {/* Nothing has run yet, so there is nothing to start, stop or restart. */}
      {!!env.generation && <DeploymentRuntimeActions key={env.id} slug={slug} env={shown} current={current} reload={reload} onPending={setPending} />}
      <DeployAction slug={slug} env={env} />
      <DeploymentMenu slug={slug} env={env} current={current} reload={reload} onDeployRelease={() => setChoice('list')} />
    </div>} />
    {tabs}
    {tab === 'status' && <DeploymentStatus slug={slug} env={env} refreshFailed={refreshFailed} releases={releases} current={current} reload={reload} onChoose={setChoice} />}
    {tab === 'settings' && settings}
    <ChooseRelease key={`${slug}/${name}`} slug={slug} name={name} displayLabel={displayLabel} releases={releases.data ?? []} current={current && releases.current} choice={choice} onChoose={setChoice} />
    {tab === 'history' && <Section title="Deploy history">
      <ReadStatus q={releases} what="releases" />
      {history.slice(0, count).map(r => { const took = deployTook(r); return <div className="card" key={r.number}>
        {r.changes.map(c => <div key={c.id}><Link to={`/changes/${c.id}`}>#{c.number} {c.title}</Link></div>)}
        {r.commitSha && <code>{r.commitSha.slice(0, 12)} </code>}
        <Link to={`/projects/${slug}/releases/${r.number}`}>Release {r.number}</Link> · {r.state === 'waiting for approval' ? 'waiting' : r.statusLabel || r.state} · {fmtStamp(r.releasedAt || r.createdAt)}{r.state === 'released' && took != null && took >= 0 && <span className="muted"> · took {fmtDuration(took / 1000)}</span>}
        {r.state === 'waiting for approval' ? <p><ReleaseWaitingReason release={r} decision={r.decision} /></p> : <ReleaseAssent approval={r.approval} />}
        {r.state === 'failed' && r.bundleId && <button disabled={!current || !releases.current} onClick={() => setChoice({ release: r })}>Try again</button>}
        {r.state === 'released' && r.number !== history.find(h => h.state === 'released')?.number && r.bundleId && <button disabled={!releases.current} onClick={() => setChoice({ release: r })}>Deploy this again</button>}
        {r.backupId && <><Link to={`/projects/${slug}/deployments/${encodeURIComponent(name)}/backups#backup-${r.backupId}`}>Backup →</Link> <RollbackBefore slug={slug} name={name} release={r} current={current && releases.current} requested={search.get('rollbackBefore') === String(r.number)} onChoose={() => {
          setChoice({ before: r })
          if (search.has('rollbackBefore')) setSearch(params => { const next = new URLSearchParams(params); next.delete('rollbackBefore'); return next }, { replace: true })
        }} /></>}
      </div> })}
      {history.length > count && <button onClick={() => setCount(n => n + 10)}>Show 10 more</button>}
      {releases.data && history.length === 0 && <p className="muted">No deploys yet.</p>}
    </Section>}
  </>
}

// deployTook is how long the deploy itself took: from the moment it could
// start to live. A release that waited for a person starts when it was
// approved, not when it was cut; the wait is the approval's, not the deploy's.
export const deployTook = (r: ReleaseListed) =>
  r.releasedAt ? Date.parse(r.releasedAt) - Date.parse(r.approval?.at || r.createdAt) : undefined
