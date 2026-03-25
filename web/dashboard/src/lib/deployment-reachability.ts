import type { Deployment, DeploymentReachability } from "@/lib/api"

export function isRunningDeployment(deployment: Deployment): boolean {
  return Boolean(deployment.running_at) && !deployment.stopped_at && !deployment.failed_at
}

export function selectLatestRunningDeployment(deployments: Deployment[]): Deployment | null {
  const running = deployments.filter(isRunningDeployment)
  if (running.length === 0) return null

  return [...running].sort((a, b) => {
    const aTime = a.created_at ? new Date(a.created_at).getTime() : 0
    const bTime = b.created_at ? new Date(b.created_at).getTime() : 0
    return bTime - aTime
  })[0]
}

export function countAdditionalPublicEndpoints(reachable?: DeploymentReachability | null): number {
  const total = reachable?.public_endpoints?.length ?? 0
  return total > 1 ? total - 1 : 0
}

export function isPrimaryPublicURLRedirect(reachable?: DeploymentReachability | null): boolean {
  return Boolean(reachable?.public_url) && reachable?.proxies_to_deployment === false
}
