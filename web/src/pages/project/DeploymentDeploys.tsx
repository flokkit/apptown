import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { ReleaseListed } from '../../wire.gen'
import { fmtDuration, fmtStamp } from '../../format'
import '../change/deploy-scene.css'
import { DeployScene } from '../change/DeployScene'
import { Status } from '../../Status'
import { Section } from '../../Section'
import './deployment-deploys.css'

// Every deploy is a release request, including requests from a landing.
export function DeploymentDeploys({ slug, name, url, displayLabel }: { slug: string; name: string; url?: string; displayLabel?: string }) {
  const releases = useGet<ReleaseListed[]>(`/projects/${slug}/releases`, { poll: 3000 })
  const [count, setCount] = useState(10)
  const history = releases.data?.filter(r => r.deployment === name) ?? []
  const latest = history[0]
  const state = !releases.data || releases.refreshFailed ? 'unknown'
    : !latest ? 'none' : latest.state === 'released' ? 'passed' : latest.state === 'releasing' ? 'deploying' : 'stopped'
  return <>
    <h1 className="deployment-page-heading">{displayLabel || name} <Status word={state} reason={state === 'passed' ? 'live' : state === 'none' ? 'not deployed' : undefined} /></h1>
    <section className="card deployment-deploy-card" aria-label="Latest deploy">
      <ReadStatus q={releases} what="latest deploy" /><StaleNote q={releases} />
      {latest ? <ReleaseDeploy key={latest.number} slug={slug} release={latest} url={url} /> : releases.data && <div className="ch-deploy-result"><div className="ch-deploy-heading"><strong>No deploys yet.</strong></div><DeployScene outcome="paused" compactStages /></div>}
    </section>
    <Section title="Deploy history">
      <ReadStatus q={releases} what="releases" />
      {history.slice(0, count).map(r => <div className="card" key={r.number}>
        <Link to={`/projects/${slug}/releases/${r.number}`}>Release {r.number}</Link> · {r.state} · {fmtStamp(r.releasedAt || r.createdAt)}
        {r.changes.map(c => <div key={c.id}><Link to={`/changes/${c.id}`}>Change {c.number}: {c.title}</Link></div>)}
        {r.backupId ? <Link to={`/deployments/backups#backup-${r.backupId}`}>Backup →</Link> : <p className="small muted">See the release for its backup outcome.</p>}
      </div>)}
      {history.length > count && <button onClick={() => setCount(n => n + 10)}>Show 10 more</button>}
      {releases.data && history.length === 0 && <p className="muted">No deploys yet.</p>}
    </Section>
  </>
}

function ReleaseDeploy({ slug, release: r, url }: { slug: string; release: ReleaseListed; url?: string }) {
  const running = r.state === 'releasing', passed = r.state === 'released'
  const duration = r.releasedAt ? Date.parse(r.releasedAt) - Date.parse(r.createdAt) : undefined
  return <div className="ch-deploy-result">
    <div className="ch-deploy-heading"><strong className={passed ? 'ok' : undefined}>{passed ? "It's live!" : running ? 'Deploying' : 'Deploy stopped'}</strong>{passed && <span> Release {r.number}</span>}</div>
    <DeployScene outcome={running ? 'paused' : passed ? 'passed' : 'failed'} compactStages />
    <div className="ch-deploy-result-facts">
      {r.failure && <p>{r.failure}</p>}
      {passed && url && <><p>{new URL(url).host}</p><a className="btn primary" href={url}>Open the app</a></>}
      {r.backupId && <p><Link to={`/deployments/backups#backup-${r.backupId}`}>Backed up before this deploy →</Link></p>}
      {duration != null && duration >= 0 && <p className="small muted">Deploy took {fmtDuration(duration / 1000)}</p>}
      <Link to={`/projects/${slug}/releases/${r.number}`}>Release details →</Link>
    </div>
  </div>
}
