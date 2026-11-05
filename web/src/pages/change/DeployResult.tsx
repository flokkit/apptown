import type { Step } from '../../api'
import { useEffect, useState } from 'react'
import type { ReleaseListed, StepProgress, RunTrace } from '../../wire.gen'
import { useGet } from '../../data'
import { Link } from 'react-router-dom'
import { fmtBytes } from '../../format'
import { AppAddress } from '../../runtime'
import { StaleNote } from '../../ReadGate'
import { DeployScene, deployWords } from './DeployScene'

export function deployDuration(ms: number) {
  const seconds = Math.max(0, Math.floor(ms / 1000))
  return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`
}

// Workflow releases are cut after landing. Their run trace records the wait.
export function previousLanding(releases: ReleaseListed[], deployment: string | undefined, startedAt: string | undefined) {
  if (!deployment || !startedAt) return undefined
  return releases.filter(r => r.deployment === deployment && r.state === 'released' && r.releasedAt && Date.parse(r.releasedAt) < Date.parse(startedAt))
    .sort((a, b) => b.number - a.number)[0]
}

export function landingDurationMs(trace: RunTrace | null | undefined) {
  const finished = trace?.steps.filter(s => s?.type === 'deploy' && s.status === 'pass').at(-1)?.finishedAt
  const start = trace?.run?.createdAt
  if (!start || !finished) return undefined
  const ms = Date.parse(finished) - Date.parse(start)
  return Number.isFinite(ms) && ms >= 0 ? ms : undefined
}

export function landingDuration(trace: RunTrace | null | undefined) {
  const ms = landingDurationMs(trace)
  return ms === undefined ? undefined : deployDuration(ms)
}

export function DeployBackupLine({ backup }: { backup: Extract<Step, { type: 'deploy' }>['evidence']['backup'] }) {
  if (!backup) return <p className="ch-deploy-backup small muted">Backup outcome not recorded</p>
  const set = backup.set
  const complete = backup.outcome === 'ok' && set?.status === 'ok'
  return <p className="ch-deploy-backup small">{complete
    ? <Link to={`/deployments/backups#backup-${set.id}`}>Backed up before this deploy · {fmtBytes(set.bytes)} →</Link>
    : backup.outcome === 'no-data' ? 'No data to back up'
    : <>{backup.set ? 'Backup incomplete' : 'No backup'}: {backup.reason}{backup.accepted && <> · went ahead, accepted by {backup.accepted.by}</>}</>}
  </p>
}

// Keep the scene mounted through completion so only a page that observed
// running progress celebrates the deploy.
export function DeployResult({ slug, changeId, release, runStartedAt, step }: { slug: string; changeId: string; release?: number; runStartedAt?: string; step: Extract<Step, { type: 'deploy' }> }) {
  const running = step.status === 'running'
  const live = useGet<StepProgress>(running ? `/steps/${step.id}/live?from=0` : null, { poll: 3000 })
  const passed = step.status === 'pass'
  const releases = useGet<ReleaseListed[]>(slug ? `/projects/${slug}/releases` : null, { poll: running ? 3000 : 0 })
  // The deploy config's default is main, as in the deploy executor.
  const deployment = typeof step.config?.deployment === 'string' && step.config.deployment ? step.config.deployment : 'main'
  const previous = previousLanding(releases.data ?? [], deployment, step.startedAt)
  const previousTrace = useGet<RunTrace>(previous?.runId ? `/runs/${previous.runId}/trace` : null, { kind: 'immutable' })
  const previousDuration = landingDuration(previousTrace.data)
  // Stage durations are not retained. Reserve a fifth of the previous landing
  // for preparation, backup, startup and checking rather than budgeting it all to build.
  const previousMs = landingDurationMs(previousTrace.data)
  const expectedBuildMs = previousMs ? previousMs * .8 : undefined
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    if (!running) return
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [running])
  const elapsed = live.data?.elapsedMs != null ? live.data.elapsedMs + Math.max(0, now - live.answeredAt) : undefined
  const start = runStartedAt
  const finish = step.finishedAt
  const elapsedLanding = start && finish ? Date.parse(finish) - Date.parse(start) : NaN
  const duration = Number.isFinite(elapsedLanding) && elapsedLanding >= 0 ? deployDuration(elapsedLanding) : undefined
  const timing = <span className="ch-deploy-times small muted">{running && elapsed != null && <span>{deployDuration(elapsed)}</span>}
    {!running && duration && <span>Landing took {duration}</span>}
    {previousDuration && <span>Last deploy took {previousDuration}</span>}</span>
  return <div className="ch-deploy-result">
    <div className="ch-deploy-heading"><strong className={passed ? 'ok' : undefined}>{running ? 'Deploying' : passed ? "It's live!" : 'Deploy stopped'}</strong>
      {passed && release && <span> Release {release}</span>}
    </div>
    <DeployScene compactStages expectedBuildMs={expectedBuildMs}
      buildElapsedMs={live.data?.stage === 'building' && live.data.stageElapsedMs != null ? live.data.stageElapsedMs + Math.max(0, now - live.answeredAt) : undefined} stage={live.data?.stage} failedStage={step.evidence.failedStage}
      outcome={running ? live.current && live.data?.running && live.data.stage ? 'running' : 'paused' : passed ? 'passed' : 'failed'} />
    {running && <>
      {!!live.data?.lines?.length && <p className="ch-deploy-caption small muted">{live.data.lines.at(-1)}</p>}
      <StaleNote q={live} />
      {live.err && <p className="error">Progress could not be read: {live.err}</p>}
      <Link className="small muted" to={`/changes/${changeId}?tab=details`} onClick={e => e.currentTarget.closest('dialog')?.close()}>Details →</Link>
    </>}
    <div className="ch-deploy-result-facts">{timing}{!running && (passed ? <>
      <DeployBackupLine backup={step.evidence.backup} />
      {step.evidence.url && <p><AppAddress url={step.evidence.url} /></p>}
      {step.evidence.url && <a className="btn primary ch-open-app" href={step.evidence.url} target="_blank" rel="noopener noreferrer">Open the app <span aria-hidden="true">↗</span></a>}
    </> : <>
      <p>Stopped while {step.evidence.failedStage ? deployWords[step.evidence.failedStage] : 'deploying'}</p>
      {step.evidence.reason && <p>{step.evidence.reason}</p>}
      <DeployBackupLine backup={step.evidence.backup} />
      {/* A landing removes the running app when it starts the new one; before that the old one still runs, so only a stop at the answer check says the app is down. */}
      {step.evidence.failedStage === 'checking' && <p>The app is not being served until the next deploy.</p>}
    </>)}</div>
  </div>
}
