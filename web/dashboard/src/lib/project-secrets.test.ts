import { describe, expect, it } from "vitest"

import type { ProjectSecret } from "@/lib/api"
import { sortProjectSecrets, upsertProjectSecretInList } from "@/lib/project-secrets"

describe("project secrets helpers", () => {
  it("sorts secrets by name", () => {
    const secrets: ProjectSecret[] = [
      { id: "2", name: "ZETA" },
      { id: "1", name: "ALPHA" },
    ]

    expect(sortProjectSecrets(secrets).map((secret) => secret.name)).toEqual(["ALPHA", "ZETA"])
  })

  it("replaces matching secrets when upserting", () => {
    const secrets: ProjectSecret[] = [
      { id: "1", name: "API_KEY", updated_at: "2026-03-27T12:00:00Z" },
      { id: "2", name: "DATABASE_URL" },
    ]

    expect(
      upsertProjectSecretInList(secrets, {
        id: "3",
        name: "API_KEY",
        updated_at: "2026-03-27T12:05:00Z",
      }),
    ).toEqual([
      { id: "3", name: "API_KEY", updated_at: "2026-03-27T12:05:00Z" },
      { id: "2", name: "DATABASE_URL" },
    ])
  })
})
