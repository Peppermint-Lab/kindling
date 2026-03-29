import type { Deployment } from "@/lib/api"

export function deploymentPromotionConfirmMessage(deployment: Deployment): string {
  const commit = deployment.github_commit?.trim() ? deployment.github_commit.slice(0, 8) : deployment.id.slice(0, 8)
  let message = `Promote deployment ${commit} back to production?`
  if (deployment.persistent_volume) {
    message += "\n\nThis service uses a persistent volume, so rollback may briefly interrupt traffic while volume ownership moves to the new deployment."
  }
  return message
}
