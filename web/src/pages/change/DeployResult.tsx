import type { ChangeView, Step } from '../../api'
import { useEffect, useState } from 'react'
import type { ReleaseListed, StepProgress, RunTrace } from '../../wire.gen'
import { useGet } from '../../data'
import { Link } from 'react-router-dom'
import { fmtBytes } from '../../format'
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

export function landingDuration(trace: RunTrace | null | undefined) {
  const finished = trace?.steps.filter(s => s?.type === 'deploy' && s.status === 'pass').at(-1)?.finishedAt
  const start = trace?.run?.createdAt
  if (!start || !finished) return undefined
  const ms = Date.parse(finished) - Date.parse(start)
  return Number.isFinite(ms) && ms >= 0 ? deployDuration(ms) : undefined
}

function DeployBackupLine({ backup }: { backup: Extract<Step, { type: 'deploy' }>['evidence']['backup'] }) {
  if (!backup) return null
  const set = backup.set
  const complete = backup.outcome === 'ok' && set?.status === 'ok'
  return <p className="ch-deploy-backup small">{complete
    ? <Link to={`/deployments/backups#backup-${set.id}`}>Backed up before this deploy · {fmtBytes(set.bytes)} →</Link>
    : backup.outcome === 'no-data' ? 'No data to back up'
    : <>{backup.set ? 'Backup incomplete' : 'No backup'}: {backup.reason}{backup.accepted && <> · went ahead, accepted by {backup.accepted.by}</>}</>}
  </p>
}

// Kept mounted while the dialog shows progress, so only a page that saw the
// deploy running can celebrate its completion.
export function DeployResult({ view, step, hidden = false, showRunning = false }: { view: ChangeView; step: Extract<Step, { type: 'deploy' }>; hidden?: boolean; showRunning?: boolean }) {
  const running = step.status === 'running'
  const live = useGet<StepProgress>(running ? `/steps/${step.id}/live?from=0` : null, { poll: 3000 })
  const passed = step.status === 'pass'
  const releases = useGet<ReleaseListed[]>(view.projectSlug ? `/projects/${view.projectSlug}/releases` : null, { poll: running ? 3000 : 0 })
  const release = view.evidence?.releases?.[0]?.number
  // The deploy config's default is main, as in the deploy executor.
  const deployment = typeof step.config?.deployment === 'string' && step.config.deployment ? step.config.deployment : 'main'
  const previous = previousLanding(releases.data ?? [], deployment, step.startedAt)
  const previousTrace = useGet<RunTrace>(previous?.runId ? `/runs/${previous.runId}/trace` : null, { kind: 'immutable' })
  const previousDuration = landingDuration(previousTrace.data)
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    if (!running) return
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [running])
  const elapsed = live.data?.elapsedMs != null ? live.data.elapsedMs + Math.max(0, now - live.answeredAt) : undefined
  const start = view.detail?.run.createdAt
  const finish = step.finishedAt
  const elapsedLanding = start && finish ? Date.parse(finish) - Date.parse(start) : NaN
  const duration = Number.isFinite(elapsedLanding) && elapsedLanding >= 0 ? deployDuration(elapsedLanding) : undefined
  return <div hidden={(!showRunning && running) || hidden} className="ch-deploy-result">
    {running && showRunning && <div className="ch-deploy-heading"><strong>{release ? `Release ${release}` : deployment}</strong>
      <span className="ch-deploy-times small muted">{elapsed != null && <span>{deployDuration(elapsed)}</span>}
        {previousDuration && <span>Last deploy took {previousDuration}</span>}</span>
    </div>}
    <DeployScene showStages={!passed} stage={live.data?.stage} failedStage={step.evidence.failedStage}
      outcome={running ? live.current && live.data?.running && live.data.stage ? 'running' : 'paused' : passed ? 'passed' : 'failed'} />
    {running && showRunning && <>
      {!!live.data?.lines?.length && <p className="ch-deploy-caption small muted">{live.data.lines.at(-1)}</p>}
      <StaleNote q={live} />
      {live.err && <p className="error">Progress could not be read: {live.err}</p>}
      <Link className="small muted" to={`/changes/${view.change.id}?tab=details`} onClick={e => e.currentTarget.closest('dialog')?.close()}>Details →</Link>
    </>}
    <div className="ch-deploy-result-facts">{!running && (passed ? <>
      <h3 className="ok">It's live!</h3>
      {release && <p>Release {release} is live</p>}
      <DeployBackupLine backup={step.evidence.backup} />
      {step.evidence.url && <p><a href={step.evidence.url}>{new URL(step.evidence.url).host.split('.').map((part, i) => <span key={i}>{i > 0 && <>.<wbr /></>}{part}</span>)}</a></p>}
      {duration && <p className="small muted">Landing took {duration}</p>}
      {step.evidence.url && <a className="btn primary ch-open-app" href={step.evidence.url}>Open the app</a>}
    </> : <>
      <h3>Stopped while {step.evidence.failedStage ? deployWords[step.evidence.failedStage] : 'deploying'}</h3>
      {step.evidence.reason && <p>{step.evidence.reason}</p>}
      <DeployBackupLine backup={step.evidence.backup} />
      {/* A landing removes the running app when it starts the new one; before that the old one still runs, so only a stop at the answer check says the app is down. */}
      {step.evidence.failedStage === 'checking' && <p>The app is not being served until the next deploy.</p>}
    </>)}</div>
  </div>
}
