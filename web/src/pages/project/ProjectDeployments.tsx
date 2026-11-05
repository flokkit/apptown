import { Link } from 'react-router-dom'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { DeploymentView, LandingDeployAttempt, ReleaseListed } from '../../wire.gen'
import { DeploymentRow } from './settings/Deployments'
import { Section } from '../../Section'

export function ProjectDeployments({ slug }: { slug: string }) {
  const q = useGet<DeploymentView[]>(`/projects/${slug}/deployments`, { poll: 5000 })
  return <Section title="Deployments" action={<Link to={`/projects/${slug}/settings/deployments`}>Configure →</Link>}>
    <ReadStatus q={q} what="deployments" /><StaleNote q={q} />
    <div role="list">{q.data?.map(e => <div key={e.id}><DeploymentRow slug={slug} env={q.refreshFailed ? { ...e, observation: 'unknown' } : e} /><LastDeploy slug={slug} name={e.name} /></div>)}</div>
    {q.data?.length === 0 && <p className="muted">Not deployed yet.</p>}
  </Section>
}
function LastDeploy({ slug, name }: { slug: string; name: string }) {
  const q = useGet<LandingDeployAttempt[]>(`/projects/${slug}/deployments/${name}/deploys`, { poll: 5000 })
  const releases = useGet<ReleaseListed[]>(`/projects/${slug}/releases`, { poll: 5000 })
  const attempt=q.data?.[0], step=attempt?.step
  const manual=releases.data?.find(r=>r.deployment===name && (!step || Date.parse(r.createdAt)>Date.parse(step.startedAt)))
  return <div className="small muted"><ReadStatus q={q} what="last deploy" /><StaleNote q={q} /><StaleNote q={releases} />{manual ? `Last deploy ${manual.state === 'released' ? 'passed' : manual.state === 'releasing' ? 'is running' : 'stopped'}` : step ? !attempt?.outcomeKnown ? 'Last deploy outcome unknown' : step.status === 'running' ? 'Deploying' : step.status === 'pass' ? 'Last deploy passed' : 'Last deploy stopped' : q.data && 'No landing deploys yet.'}</div>
}
