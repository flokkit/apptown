import { useGet } from './data'
import { RuntimeStatus, runtimeDescription } from './runtime'
import { Status } from './Status'
import type { ComponentProps } from 'react'
import type { DeploymentDiagnostics, DeploymentView, ReleaseListed } from './wire.gen'

// Release progress takes precedence while containers are being replaced.
// Otherwise diagnostics qualify the host-level runtime observation.
export function deploymentStatus(env: DeploymentView, diagnostics?: DeploymentDiagnostics | null, unavailable = false, releases: ReleaseListed[] = []): ComponentProps<typeof Status> {
  const release = releases.find(r => r.deployment === env.name && r.state === 'releasing')
  if (release) return { word: 'deploying', help: `Release ${release.number}` }
  // A target that runs no containers (a bucket website) never reports part
  // presence; its runtime reading is the witnessed answer, not an unknown part.
  const partsUnknown = diagnostics?.containers !== false && diagnostics?.parts.some(p => p.presence === 'unknown')
  const uncertain = !!env.generation && (unavailable || !diagnostics || diagnostics.reading?.unknown || diagnostics.reading?.stale || partsUnknown)
  const word = env.observation === 'running' && uncertain ? 'unknown' : env.observation
  return { word, help: runtimeDescription(env), staleAt: env.observationStale && word !== 'unknown' ? env.observedAt : undefined }
}

export function useDeploymentStatus(slug: string, env: DeploymentView) {
  const q = useGet<DeploymentDiagnostics>(env.generation ? `/projects/${slug}/deployments/${env.name}/diagnostics` : null, { poll: 5000 })
  const releases = useGet<ReleaseListed[]>(env.kind !== 'preview' ? `/projects/${slug}/releases` : null, { poll: 5000 })
  return deploymentStatus(env, q.data, q.refreshFailed, releases.refreshFailed ? [] : releases.data ?? [])
}

export function DeploymentStatus({ slug, env }: { slug: string; env: DeploymentView }) {
  const status = useDeploymentStatus(slug, env)
  return <Status observation {...status} />
}

export function FleetDeploymentStatus({ slug, id }: { slug: string; id?: string | null }) {
  const q = useGet<DeploymentView[]>(`/projects/${slug}/deployments`, { poll: 5000 })
  const env = q.data?.find(e => e.id === id)
  return env ? <DeploymentStatus slug={slug} env={q.refreshFailed ? { ...env, observation: 'unknown' } : env} /> : <RuntimeStatus observation={{ observation: 'unknown', targetName: '', observationStale: false }} />
}
