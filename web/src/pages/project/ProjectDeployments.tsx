import { DeploymentsTab } from './settings/Deployments'

export function ProjectDeployments({ slug }: { slug: string }) {
  return <DeploymentsTab slug={slug} />
}
