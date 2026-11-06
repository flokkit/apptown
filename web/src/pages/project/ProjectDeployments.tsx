import { Link } from 'react-router-dom'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { DeploymentView, ReleaseListed } from '../../wire.gen'
import { DeploymentRow } from './settings/Deployments'
import { Section } from '../../Section'

export function ProjectDeployments({ slug }: { slug: string }) {
  const q = useGet<DeploymentView[]>(`/projects/${slug}/deployments`, { poll: 5000 })
  return <Section title="Deployments" action={<Link to={`/projects/${slug}/settings/deployments`}>Configure →</Link>}>
    <ReadStatus q={q} what="deployments" /><StaleNote q={q} />
    <div role="list">{q.data?.map(e => <div key={e.id}><DeploymentRow slug={slug} current={q.current} env={q.refreshFailed ? { ...e, observation: 'unknown' } : e} /><LastDeploy slug={slug} name={e.name} /></div>)}</div>
    {q.data?.length === 0 && <p className="muted">Not deployed yet.</p>}
  </Section>
}
function LastDeploy({ slug, name }: { slug: string; name: string }) {
  const releases = useGet<ReleaseListed[]>(`/projects/${slug}/releases`, { poll: 5000 })
  const release = releases.data?.find(r => r.deployment === name)
  return <div className="small muted"><ReadStatus q={releases} what="last deploy" /><StaleNote q={releases} />{release ? `Last deploy ${release.state === 'released' ? 'passed' : release.state === 'releasing' ? 'is running' : 'stopped'}` : releases.data && 'No releases yet.'}</div>
}
