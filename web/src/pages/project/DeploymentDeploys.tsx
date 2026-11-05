import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { ChangeView, Step } from '../../api'
import type { LandingDeployAttempt, ReleaseListed } from '../../wire.gen'
import { deployDuration } from '../change/DeployResult'
import { fmtStamp } from '../../format'
import { DeployBackupLine, DeployResult } from '../change/DeployResult'
import '../change/deploy-scene.css'
import { DeployScene } from '../change/DeployScene'
import { HeldDecision } from '../change/HeldDecision'
import { Status } from '../../Status'
import { Section } from '../../Section'
import './deployment-deploys.css'

export function DeploymentDeploys({ slug, name, url, displayLabel }: { slug: string; name: string; url?: string; displayLabel?: string }) {
  const attempts = useGet<LandingDeployAttempt[]>(`/projects/${slug}/deployments/${name}/deploys`, { poll: 3000 })
  const releases = useGet<ReleaseListed[]>(`/projects/${slug}/releases`, { poll: 3000 })
  const [count, setCount] = useState(10)
  const ready = !!attempts.data && !!releases.data
  const latest = attempts.data?.[0]
  const step = latest?.step as Extract<Step, { type: 'deploy' }> | undefined
  const change = useGet<ChangeView>(latest && step && ['fail', 'error', 'needs_human'].includes(step.status) ? `/changes/${latest.changeId}` : null, { poll: 3000 })
  const releaseFor = (a: LandingDeployAttempt) => releases.data?.find(r => r.runId === a.step.runId && r.deployment === name && a.step.status === 'pass')
  const release = latest && releaseFor(latest)
  const manualReleases = releases.data?.filter(r => r.deployment === name && !attempts.data?.some(a => a.step.runId === r.runId)) ?? []
  const manual = manualReleases[0]
  const showManual = manual && (!latest || Date.parse(manual.createdAt) > Date.parse(latest.step.startedAt))
  const history = [
    ...(attempts.data ?? []).map(a => ({ kind: 'landing' as const, attempt: a, at: a.step.startedAt })),
    ...manualReleases.map(r => ({ kind: 'manual' as const, release: r, at: r.createdAt })),
  ].sort((a,b) => b.at.localeCompare(a.at))
  const state = !ready || latest && !latest.outcomeKnown ? 'unknown'
    : showManual ? manual.state === 'released' ? 'passed' : manual.state === 'releasing' ? 'deploying' : 'stopped'
    : !step ? 'none' : step.status === 'pass' ? 'passed' : step.status === 'running' ? 'deploying' : 'stopped'
  return <>
    <h1 className="deployment-page-heading">{displayLabel || name} <Status word={attempts.refreshFailed || releases.refreshFailed ? 'unknown' : state} reason={attempts.refreshFailed || releases.refreshFailed ? undefined : state === 'passed' ? 'live' : state === 'none' ? 'not deployed' : undefined} /></h1>
    <section className="card deployment-deploy-card" aria-label="Latest deploy">
      <ReadStatus q={[attempts, releases]} what="latest deploy" /><StaleNote q={attempts} /><StaleNote q={releases} />
      {ready && (latest && !latest.outcomeKnown ? <div className="ch-deploy-result"><div className="ch-deploy-heading"><strong>Deploy status unknown</strong></div><DeployScene outcome="paused" compactStages /></div> : showManual ? <ManualDeploy slug={slug} release={manual} url={url} /> : latest && step ? <>
        <DeployResult key={`${step.id}/${latest!.attempt}`} slug={slug} changeId={latest.changeId!} release={release?.number} runStartedAt={latest.runStartedAt} step={step} />

        {step.status !== 'running' && step.status !== 'pass' && <><ReadStatus q={change} what="recovery actions" /><StaleNote q={change} />{change.current && !attempts.refreshFailed && change.data && <HeldDecision view={change.data} onChange={change.reload} resultShown />}</>}
      </> : attempts.data && <div className="ch-deploy-result"><div className="ch-deploy-heading"><strong>No deploys yet.</strong></div><DeployScene outcome="paused" compactStages /></div>)}
    </section>
    <Section title="Deploy history">
      <ReadStatus q={releases} what="releases" />
      {history.slice(0, count).map(entry => {
        if (entry.kind === 'manual') {
          const r = entry.release
          return <div className="card" key={`release-${r.number}`}><Link to={`/projects/${slug}/releases/${r.number}`}>Release {r.number}</Link> · {r.state} · {fmtStamp(r.releasedAt || r.createdAt)}
            {r.changes.map(c => <div key={c.id}><Link to={`/changes/${c.id}`}>Change {c.number}: {c.title}</Link></div>)}
            {r.backupId ? <Link to={`/deployments/backups#backup-${r.backupId}`}>Backup →</Link> : <p className="small muted">Backup outcome not recorded</p>}
          </div>
        }
        const a = entry.attempt, s = a.step as Extract<Step, { type: 'deploy' }>, r = releaseFor(a)
        return <div className="card" key={`${s.id}/${a.attempt}`}>
          <strong>{!a.outcomeKnown ? 'Outcome unknown' : s.status === 'running' ? 'Deploying' : s.status === 'pass' ? 'Live' : 'Deploy stopped'}</strong> · {fmtStamp(s.startedAt)}
          <div><Link to={`/changes/${a.changeId}`}>Change {a.changeNumber}: {a.changeTitle}</Link>{r && <> · <Link to={`/projects/${slug}/releases/${r.number}`}>Release {r.number}</Link></>}</div>
          <DeployBackupLine backup={s.evidence.backup} />
        </div>
      })}
      {history.length > count && <button onClick={() => setCount(n => n + 10)}>Show 10 more</button>}
      {attempts.data?.length === 0 && !releases.data?.some(r => r.deployment === name) && <p className="muted">No deploys yet.</p>}
    </Section>
  </>
}

function ManualDeploy({ slug, release: r, url }: { slug: string; release: ReleaseListed; url?: string }) {
  const running = r.state === 'releasing', passed = r.state === 'released'
  const duration = r.releasedAt ? Date.parse(r.releasedAt) - Date.parse(r.createdAt) : undefined
  return <div className="ch-deploy-result">
    <div className="ch-deploy-heading"><strong className={passed ? 'ok' : undefined}>{passed ? "It's live!" : running ? 'Deploying' : 'Deploy stopped'}</strong>{passed && <span> Release {r.number}</span>}</div>
    <DeployScene outcome={running ? 'paused' : passed ? 'passed' : 'failed'} compactStages />
    <div className="ch-deploy-result-facts">
      {r.failure && <p>{r.failure}</p>}
      {passed && url && <><p>{new URL(url).host}</p><a className="btn primary" href={url}>Open the app</a></>}
      {r.backupId && <p><Link to={`/deployments/backups#backup-${r.backupId}`}>Backed up before this deploy →</Link></p>}
      {duration != null && duration >= 0 && <p className="small muted">Deploy took {deployDuration(duration)}</p>}
      <Link to={`/projects/${slug}/releases/${r.number}`}>Release details →</Link>
    </div>
  </div>
}
