import { useGet } from './data'
import { RuntimeStatus, statusProps, failedRuntimeRefresh } from './runtime'
import { Status } from './Status'
import type { DeploymentView } from './wire.gen'

export function deploymentStatus(env: DeploymentView) {
  return statusProps(env.status)
}

export function DeploymentStatus({ env }: { slug: string; env: DeploymentView }) {
  return <RuntimeStatus observation={env} />
}

export function FleetDeploymentStatus({ slug, id }: { slug: string; id?: string | null }) {
  const q = useGet<DeploymentView[]>(`/projects/${slug}/deployments`, { poll: 5000 })
  const env = q.data?.find(e => e.id === id)
  return env ? <RuntimeStatus observation={q.refreshFailed ? failedRuntimeRefresh(env, q.reason) : env} /> : <Status observation word="unknown" help={q.reason || 'Waiting for the deployment status'} />
}
