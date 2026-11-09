import { DeploymentRuntimeActions } from './DeploymentRuntimeActions'
import { deploymentStatus } from '../../deploymentStatus'
import type { DeploymentView } from '../../wire.gen'
import { ReleaseApprovalActions, ReleaseAssent } from './ReleaseApproval'
import { ChooseRelease, RollbackBefore, type ReleaseChoice } from './ChooseRelease'
import { useState, type ReactNode } from 'react'
import { DeploymentMenu } from './DeploymentMenu'
import { Link } from 'react-router-dom'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { ReleaseListed } from '../../wire.gen'
import { fmtDuration, fmtStamp } from '../../format'
import '../change/deploy-scene.css'
import { DeployScene, deployWords } from '../change/DeployScene'
import { Status } from '../../Status'
import { Section } from '../../Section'
import './deployment-deploys.css'

// NotDeployed is a deployment nothing has run on yet: it says so and what
// will put it live, in place of an empty deploy scene.
function NotDeployed({ env, current, onDeploy }: { env: DeploymentView; current: boolean; onDeploy: () => void }) {
  const how = env.feed === 'main'
    ? env.feedConfirmed ? 'It deploys when a change lands on main.' : 'It will deploy when a change lands on main, once someone saves its release feed in Settings.'
    : env.feed === 'promotion' ? `It deploys the release ${env.promotionSource || 'its source deployment'} last ran, when someone promotes it.`
    : 'Deploy a release to put it live.'
  return <section className="deployment-not-deployed" aria-label="Latest deploy">
    <p><strong>Not deployed yet.</strong> {how}</p>
    {env.feed !== 'main' && <button className="primary" disabled={!current} onClick={onDeploy}>Deploy a release…</button>}
  </section>
}

// DeploymentTitle is a deployment's name with its status, as the page heads it.
export function DeploymentTitle({ name, env }: { name: string; env: DeploymentView }) {
  return <h1>{name} <Status observation {...deploymentStatus(env)} /></h1>
}

// Every deploy is a release request, including requests from a landing.
// tab picks what shows under the heading: the deployment now (status, with
// body above the latest deploy and below under it), its deploy history, or
// its settings; tabs is the strip that switches them.
export type DeploymentTab = 'status' | 'history' | 'settings'
type DeployHistoryProps = { slug: string; name: string; url?: string; displayLabel?: string; env: DeploymentView; current: boolean; reload: () => Promise<void>; facts?: ReactNode; body?: ReactNode; below?: ReactNode; settings?: ReactNode; tabs?: ReactNode; tab?: DeploymentTab }
export function DeploymentDeploys(props: DeployHistoryProps) {
  return <DeploymentHistory key={`${props.slug}/${props.name}/${props.env.id}`} {...props} />
}
function DeploymentHistory({ slug, name, url, displayLabel, env, current, reload, facts, body, below, settings, tabs, tab = 'status' }: DeployHistoryProps) {
  const releases = useGet<ReleaseListed[]>(`/projects/${slug}/releases`, { poll: 3000 })
  const [pending, setPending] = useState<string>()
  const shown = pending ? { ...env, runtimeState: pending } : env
  const [choice, setChoice] = useState<ReleaseChoice | 'list' | null>(null)
  const [count, setCount] = useState(10)
  const history = releases.data?.filter(r => r.deployment === name) ?? []
  const latest = history[0]
  return <>
    <div className="deployment-page-heading">
      <div className="deployment-page-title"><DeploymentTitle name={displayLabel || name} env={env} />{facts}</div>
      <div className="deployment-page-actions">
        {/* Nothing has run yet, so there is nothing to start, stop or restart. */}
        {!!env.generation && <DeploymentRuntimeActions key={env.id} slug={slug} env={shown} current={current} reload={reload} onPending={setPending} />}
        <DeploymentMenu slug={slug} env={env} current={current} reload={reload} onDeployRelease={() => setChoice('list')} />
      </div>
    </div>
    {tabs}
    {tab === 'status' && <>
    {env.runtimeState === 'stopped' && env.servingRelease && <p>stopped · will run release {env.servingRelease.number} when started</p>}
    {body}
    {!env.generation && releases.data && history.length === 0
      ? <NotDeployed env={env} current={current} onDeploy={() => setChoice('list')} />
      : latest && releases.current && settledRelease(latest) && !(env.runtimeState === 'stopped' && latest.state === 'released')
      ? <LatestDeployLine slug={slug} release={latest} />
      : <section className="card deployment-deploy-card" aria-label="Latest deploy">
      <ReadStatus q={releases} what="latest deploy" /><StaleNote q={releases} />
      {latest ? <ReleaseDeploy key={latest.number} slug={slug} release={latest} url={url} current={current && releases.current} progressCurrent={!releases.refreshFailed} stopped={env.runtimeState === 'stopped'} /> : releases.data && <div className="ch-deploy-result"><div className="ch-deploy-heading"><strong>No deploys yet.</strong></div><DeployScene outcome="paused" compactStages /></div>}
    </section>}
    {below}
    </>}
    {tab === 'settings' && settings}
    <ChooseRelease key={`${slug}/${name}`} slug={slug} name={name} displayLabel={displayLabel} releases={releases.data ?? []} current={current && releases.current} choice={choice} onChoose={setChoice} />
    {tab === 'history' && <Section title="Deploy history">
      <ReadStatus q={releases} what="releases" />
      {history.slice(0, count).map(r => <div className="card" key={r.number}>
        <Link to={`/projects/${slug}/releases/${r.number}`}>Release {r.number}</Link> · {r.statusLabel || r.state} · {fmtStamp(r.releasedAt || r.createdAt)}
        {r.state === 'waiting for approval' ? <p>Waiting for approval from {r.approvalRoleTitle || r.approvalRole}.</p> : <ReleaseAssent approval={r.approval} />}
        {r.state === 'failed' && r.bundleId && <button disabled={!current || !releases.current} onClick={() => setChoice({ release: r })}>Try again</button>}
        {r.state === 'released' && r.number !== history.find(h => h.state === 'released')?.number && r.bundleId && <button disabled={!releases.current} onClick={() => setChoice({ release: r })}>Deploy this again</button>}
        {r.changes.map(c => <div key={c.id}><Link to={`/changes/${c.id}`}>Change {c.number}: {c.title}</Link></div>)}
        {r.backupId && <><Link to={`/backups#backup-${r.backupId}`}>Backup →</Link> <RollbackBefore slug={slug} name={name} release={r} current={current && releases.current} onChoose={() => setChoice({ before: r })} /></>}
      </div>)}
      {history.length > count && <button onClick={() => setCount(n => n + 10)}>Show 10 more</button>}
      {releases.data && history.length === 0 && <p className="muted">No deploys yet.</p>}
    </Section>}
  </>
}

// A release that finished a while ago needs no scene: one line says how it
// ended. The scene is for a deploy under way, waiting or unsure, and stays
// for a few minutes after it ends so the outcome is seen.
const sceneAfterMs = 10 * 60 * 1000
const settledRelease = (r: ReleaseListed, now = Date.now()) =>
  ['released', 'failed', 'cancelled', 'superseded'].includes(r.state) && !r.progressUncertain &&
  now - Date.parse(r.releasedAt || r.stageEnteredAt || r.createdAt) > sceneAfterMs

function LatestDeployLine({ slug, release: r }: { slug: string; release: ReleaseListed }) {
  const duration = r.releasedAt ? Date.parse(r.releasedAt) - Date.parse(r.createdAt) : undefined
  return <section className="deployment-latest-line" aria-label="Latest deploy">
    <span>Latest deploy: <Link to={`/projects/${slug}/releases/${r.number}`}>release {r.number}</Link> <Status word={r.statusLabel || (r.state === 'released' ? 'deployed' : r.state === 'failed' ? 'failed' : 'stopped')} />{r.failedStage && <> while {deployWords[r.failedStage]}</>} <span className="muted">{fmtStamp(r.releasedAt || r.createdAt)}{duration != null && duration >= 0 && r.state === 'released' ? ` · took ${fmtDuration(duration / 1000)}` : ''}</span></span>
    {r.failure && <span className="deployment-latest-failure">{r.failure}</span>}
    <ReleaseAssent approval={r.approval} />
    {r.backupId && <Link to={`/backups#backup-${r.backupId}`}>Backed up before this deploy →</Link>}
  </section>
}

function ReleaseDeploy({ slug, release: r, url, current, progressCurrent, stopped }: { slug: string; release: ReleaseListed; url?: string; current: boolean; progressCurrent: boolean; stopped?: boolean }) {
  const ready = stopped && r.state === 'released'
  const releasing = r.state === 'releasing'
  const uncertain = releasing && (!progressCurrent || r.progressUncertain)
  const running = releasing && !uncertain, passed = !stopped && r.state === 'released'
  const duration = r.releasedAt ? Date.parse(r.releasedAt) - Date.parse(r.createdAt) : undefined
  return <div className="ch-deploy-result">
    <div className="ch-deploy-heading"><strong className={passed ? 'ok' : undefined}>{ready ? `Release ${r.number} is ready to start` : passed ? "It's live!" : uncertain ? 'Deployment progress uncertain' : running ? 'Deploying' : r.state === 'waiting for approval' ? 'Waiting for approval' : r.state === 'cancelled' ? 'Release cancelled' : r.state === 'superseded' ? 'Release superseded' : 'Deploy stopped'}</strong>{passed && <span> Release {r.number}</span>}</div>
    <DeployScene stage={r.stage ?? r.lastStage} failedStage={r.failedStage} buildElapsedMs={r.stage === 'building' ? r.stageElapsedMs ?? undefined : undefined} outcome={running ? 'running' : uncertain || ready || r.state === 'waiting for approval' || r.state === 'cancelled' || r.state === 'superseded' ? 'paused' : passed ? 'passed' : 'failed'} compactStages />
    <ReleaseApprovalActions slug={slug} release={r} decision={r.decision} current={current} />
    <div className="ch-deploy-result-facts">
      {uncertain && <p className="ch-deploy-progress">{(r.stage ?? r.lastStage) ? `Last seen: ${deployWords[(r.stage ?? r.lastStage)!]} · ` : ''}{r.progressUncertain ? r.interruptedByRestart ? 'Interrupted by a restart — checking.' : 'Deployment interrupted; being checked.' : 'Current progress is unknown until releases can be read again.'}</p>}
      {running && <p className="ch-deploy-progress">{r.stage ? deployWords[r.stage] : 'Waiting for deployment progress'}{r.stageElapsedMs != null && ` · ${fmtDuration(Math.floor(r.stageElapsedMs / 1000))}`}</p>}
      {r.failure && <p>{r.failure}</p>}
      {passed && url && <><p>{new URL(url).host}</p><a className="btn primary" href={url}>Open the app</a></>}
      {r.backupId && <p><Link to={`/backups#backup-${r.backupId}`}>Backed up before this deploy →</Link></p>}
      {duration != null && duration >= 0 && <p className="small muted">Deploy took {fmtDuration(duration / 1000)}</p>}
      <Link to={`/projects/${slug}/releases/${r.number}`}>Release details →</Link>
    </div>
  </div>
}
