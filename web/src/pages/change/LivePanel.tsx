import { Link } from 'react-router-dom'
import type { ChangeView } from '../../api'
import { Tech } from '../../Tech'
import { fmtDuration } from '../../format'
import { Status } from '../../Status'
import { hostOnlyWords } from '../../runtime'
import { DeployStages, releaseProgress } from './DeployScene'
import './deploy-scene.css'

// Each release keeps its stage list and endpoint together, so these cards carry nested progress.
export function LivePanel({ view, progressCurrent = true }: { view: ChangeView; progressCurrent?: boolean }) {
  const releases = view.evidence.landingReleases
  return <section className="stack stack-3" aria-label="Landing releases">
    {view.change.state === 'shipped' && view.stateDetail && <p>{view.stateDetail}</p>}
   {!releases.length ? <p>No release requested{view.change.state === 'landing' ? ' yet' : ''}. <Link to={`/projects/${view.projectSlug}/deployments`}>Deployments</Link></p> : releases.map(r => {
    const progress = releaseProgress(r, progressCurrent)
    return <div className="card stack stack-2" key={r.number}>
     <div className="row row-2"><Link to={`/projects/${view.projectSlug}/deployments/${r.deployment}`}>{r.deployment}</Link><Status word={r.statusLabel} old={!progressCurrent} />
      {r.url ? r.hostOnly ? <span><Tech>{r.url}</Tech> · {hostOnlyWords(r.url)}</span> : <><span><Tech>{r.url}</Tech></span><a className="btn primary" href={r.url}>Open app</a></> : <span>Address not known yet</span>}
     </div>
     {progress.uncertain && <p>Deployment progress uncertain{r.lastStage ? ' · last recorded stage shown below' : ''}.</p>}
     {!progressCurrent && <p className="muted">Old reading. Current progress could not be read.</p>}
     <DeployStages stage={progress.outcome === 'failed' ? r.failedStage : r.stage ?? r.lastStage} outcome={progress.outcome} compact />
      {progress.running && r.stageElapsedMs != null && <p className="small muted">{fmtDuration(Math.floor(r.stageElapsedMs / 1000))} in this stage</p>}
     {r.failure && <p>{r.failure}</p>}
     <Link to={`/projects/${view.projectSlug}/releases/${r.number}`}>Release {r.number} →</Link>
    </div>
   })}
  </section>
}
