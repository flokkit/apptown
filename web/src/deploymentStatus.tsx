import { useGet } from './data'
import { RuntimeStatus, type RuntimeObservation } from './runtime'
import type { DeploymentDiagnostics, DeploymentView } from './wire.gen'

// A host-level running observation cannot establish that every part reports.
// Keep failures and stopped observations even when diagnostics are unavailable.
export function deploymentStatus(env: DeploymentView, diagnostics?: DeploymentDiagnostics | null, unavailable = false): RuntimeObservation {
  const uncertain = !!env.generation && (unavailable || !diagnostics || diagnostics.reading?.unknown || diagnostics.reading?.stale || diagnostics.parts.some(p => p.presence === 'unknown'))
  return env.observation === 'running' && uncertain ? { ...env, observation: 'unknown' } : env
}

export function useDeploymentStatus(slug: string, env: DeploymentView) {
  const q = useGet<DeploymentDiagnostics>(env.generation ? `/projects/${slug}/deployments/${env.name}/diagnostics` : null, { poll: 5000 })
  return deploymentStatus(env, q.data, q.refreshFailed)
}

export function DeploymentStatus({ slug, env }: { slug: string; env: DeploymentView }) {
  const observation = useDeploymentStatus(slug, env)
  return <RuntimeStatus observation={observation} />
}

export function FleetDeploymentStatus({ slug, id }: { slug: string; id?: string | null }) {
  const q = useGet<DeploymentView[]>(`/projects/${slug}/deployments`, { poll: 5000 })
  const env = q.data?.find(e => e.id === id)
  return env ? <DeploymentStatus slug={slug} env={q.refreshFailed ? { ...env, observation: 'unknown' } : env} /> : <RuntimeStatus observation={{ observation: 'unknown', targetName: '', observationStale: false }} />
}
