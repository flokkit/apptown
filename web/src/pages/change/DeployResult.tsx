import type { ChangeView, Step } from '../../api'
import type { StepProgress } from '../../wire.gen'
import { useGet } from '../../data'
import { fmtDuration } from '../../format'
import { DeployScene, deployWords } from './DeployScene'

// Kept mounted while the dialog shows progress, so only a page that saw the
// deploy running can celebrate its completion.
export function DeployResult({ view, step, hidden = false }: { view: ChangeView; step: Extract<Step, { type: 'deploy' }>; hidden?: boolean }) {
  const running = step.status === 'running'
  const live = useGet<StepProgress>(running ? `/steps/${step.id}/live?from=0` : null, { poll: 3000 })
  const passed = step.status === 'pass'
  const release = view.evidence?.releases?.[0]?.number
  const start = view.detail?.run.createdAt
  const finish = step.finishedAt
  const duration = start && finish ? fmtDuration(Math.max(0, Math.round((Date.parse(finish) - Date.parse(start)) / 1000))) : undefined
  return <div hidden={running || hidden} className="ch-deploy-result">
    <DeployScene stage={live.data?.stage} failedStage={step.evidence.failedStage}
      outcome={running ? live.current && live.data?.running && live.data.stage ? 'running' : 'paused' : passed ? 'passed' : 'failed'} />
    <div className="ch-deploy-result-facts">{!running && (passed ? <>
      <h3>{release ? `Release ${release} is live` : 'Live'}</h3>
      {step.evidence.url && <p><a href={step.evidence.url}>{step.evidence.url}</a></p>}
      {duration && <p className="small muted">Landing took {duration}</p>}
      {step.evidence.url && <a href={step.evidence.url}>Open the app</a>}
    </> : <>
      <h3>Stopped while {step.evidence.failedStage ? deployWords[step.evidence.failedStage] : 'deploying'}</h3>
      {step.evidence.reason && <p>{step.evidence.reason}</p>}
      {/* A landing removes the running app when it starts the new one; before that the old one still runs, so only a stop at the answer check says the app is down. */}
      {step.evidence.failedStage === 'checking' && <p>The app is not being served until the next deploy.</p>}
    </>)}</div>
  </div>
}
