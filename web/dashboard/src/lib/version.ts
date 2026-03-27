export * from "@/lib/version-core"

import { resolveVersionSnapshot } from "@/lib/version-core"

export const kindlingVersion = resolveVersionSnapshot(__APP_VERSION__)
