import type { ProjectSecret, ServiceSecret } from "@/lib/api"

export function sortProjectSecrets(secrets: ProjectSecret[]): ProjectSecret[] {
  return [...secrets].sort((a, b) => a.name.localeCompare(b.name))
}

export function upsertProjectSecretInList(
  secrets: ProjectSecret[],
  next: ProjectSecret,
): ProjectSecret[] {
  const filtered = secrets.filter((secret) => secret.id !== next.id && secret.name !== next.name)
  filtered.push(next)
  return sortProjectSecrets(filtered)
}

export function sortServiceSecrets(secrets: ServiceSecret[]): ServiceSecret[] {
  return [...secrets].sort((a, b) => a.name.localeCompare(b.name))
}

export function upsertServiceSecretInList(
  secrets: ServiceSecret[],
  next: ServiceSecret,
): ServiceSecret[] {
  const filtered = secrets.filter((secret) => secret.id !== next.id && secret.name !== next.name)
  filtered.push(next)
  return sortServiceSecrets(filtered)
}
