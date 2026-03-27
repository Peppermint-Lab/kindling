import type { ProjectSecret } from "@/lib/api"

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
