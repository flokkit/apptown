import { statusProps } from './runtime'
import type { DeploymentView } from './wire.gen'

export function deploymentStatus(env: DeploymentView) {
  return statusProps(env.status)
}
