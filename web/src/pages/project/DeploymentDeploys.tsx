import { DeploymentRuntimeActions } from './DeploymentRuntimeActions'
import { deploymentStatus } from '../../deploymentStatus'
import type { DeploymentView } from '../../wire.gen'
import { ReleaseApprovalActions, ReleaseAssent } from './ReleaseApproval'
import { ChooseRelease, RollbackBefore, type ReleaseChoice } from './ChooseRelease'
import { useState } from 'react'
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

// Every deploy is a release request, including requests from a landing.
type DeployHistoryProps = { slug: string; name: string; url?: string; displayLabel?: string; env: DeploymentView; current: boolean; reload: () => Promise<void> }
export function DeploymentDeploys(props: DeployHistoryProps) {
  return <DeploymentHistory key={`${props.slug}/${props.name}/${props.env.id}`} {...props} />
}
function DeploymentHistory({ slug, name, url, displayLabel, env, current, reload }: DeployHistoryProps) {
  const releases = useGet<ReleaseListed[]>(`/projects/${slug}/releases`, { poll: 3000 })
  const [pending, setPending] = useState<string>()
  const shown = pending ? { ...env, runtimeState: pending } : env
  const status = deploymentStatus(env)
  const [choice, setChoice] = useState<ReleaseChoice | 'list' | null>(null)
  const [count, setCount] = useState(10)
  const history = releases.data?.filter(r => r.deployment === name) ?? []
  const latest = history[0]
  return <>
    <div className="deployment-page-heading"><h1>{displayLabel || name} <Status observation {...status} /></h1>
      <DeploymentRuntimeActions key={env.id} slug={slug} env={shown} current={current} reload={reload} onPending={setPending} />
    </div>
    {env.runtimeState === 'stopped' && env.servingRelease && <p>stopped · will run release {env.servingRelease.number} when started</p>}
    <section className="card deployment-deploy-card" aria-label="Latest deploy">
      <ReadStatus q={releases} what="latest deploy" /><StaleNote q={releases} />
      {latest ? <ReleaseDeploy key={latest.number} slug={slug} release={latest} url={url} current={current && releases.current} progressCurrent={!releases.refreshFailed} stopped={env.runtimeState === 'stopped'} /> : releases.data && <div className="ch-deploy-result"><div className="ch-deploy-heading"><strong>No deploys yet.</strong></div><DeployScene outcome="paused" compactStages /></div>}
    </section>
    <ChooseRelease key={`${slug}/${name}`} slug={slug} name={name} displayLabel={displayLabel} releases={releases.data ?? []} current={current && releases.current} choice={choice} onChoose={setChoice} />
    <Section title="Deploy history">
      <ReadStatus q={releases} what="releases" />
      {history.slice(0, count).map(r => <div className="card" key={r.number}>
        <Link to={`/projects/${slug}/releases/${r.number}`}>Release {r.number}</Link> · {r.statusLabel || r.state} · {fmtStamp(r.releasedAt || r.createdAt)}
        {r.state === 'waiting for approval' ? <p>Waiting for approval from {r.approvalRole}.</p> : <ReleaseAssent approval={r.approval} />}
        {r.state === 'failed' && r.bundleId && <button disabled={!current || !releases.current} onClick={() => setChoice({ release: r })}>Try again</button>}
        {r.state === 'released' && r.number !== history.find(h => h.state === 'released')?.number && r.bundleId && <button disabled={!releases.current} onClick={() => setChoice({ release: r })}>Deploy this again</button>}
        {r.changes.map(c => <div key={c.id}><Link to={`/changes/${c.id}`}>Change {c.number}: {c.title}</Link></div>)}
        {r.backupId && <><Link to={`/backups#backup-${r.backupId}`}>Backup →</Link> <RollbackBefore slug={slug} name={name} release={r} current={current && releases.current} onChoose={() => setChoice({ before: r })} /></>}
      </div>)}
      {history.length > count && <button onClick={() => setCount(n => n + 10)}>Show 10 more</button>}
      {releases.data && history.length === 0 && <p className="muted">No deploys yet.</p>}
    </Section>
  </>
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
