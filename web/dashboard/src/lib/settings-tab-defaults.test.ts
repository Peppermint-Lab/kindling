import { describe, expect, it } from "vitest"

import { resolveOrgSettingsDefaultTab, resolvePlatformSettingsDefaultTab } from "@/lib/settings-tab-defaults"

describe("resolveOrgSettingsDefaultTab", () => {
  it("prefers authentication when OAuth redirect params are present", () => {
    expect(
      resolveOrgSettingsDefaultTab({
        tab: "cluster",
        authError: "failed",
        authLinked: null,
        canManageOrg: true,
      }),
    ).toBe("authentication")
    expect(
      resolveOrgSettingsDefaultTab({
        tab: null,
        authError: null,
        authLinked: "github",
        canManageOrg: true,
      }),
    ).toBe("authentication")
  })

  it("defaults org admins to cluster workers tab", () => {
    expect(
      resolveOrgSettingsDefaultTab({
        tab: null,
        authError: null,
        authLinked: null,
        canManageOrg: true,
      }),
    ).toBe("cluster")
  })

  it("defaults members to authentication", () => {
    expect(
      resolveOrgSettingsDefaultTab({
        tab: null,
        authError: null,
        authLinked: null,
        canManageOrg: false,
      }),
    ).toBe("authentication")
  })

  it("ignores cluster tab when user cannot manage servers", () => {
    expect(
      resolveOrgSettingsDefaultTab({
        tab: "cluster",
        authError: null,
        authLinked: null,
        canManageOrg: false,
      }),
    ).toBe("authentication")
  })

  it("maps legacy public-url tab to authentication", () => {
    expect(
      resolveOrgSettingsDefaultTab({
        tab: "public-url",
        authError: null,
        authLinked: null,
        canManageOrg: true,
      }),
    ).toBe("authentication")
  })
})

describe("resolvePlatformSettingsDefaultTab", () => {
  it("returns sign-in-providers only when requested", () => {
    expect(resolvePlatformSettingsDefaultTab("sign-in-providers")).toBe("sign-in-providers")
    expect(resolvePlatformSettingsDefaultTab("public-url")).toBe("public-url")
    expect(resolvePlatformSettingsDefaultTab(null)).toBe("public-url")
    expect(resolvePlatformSettingsDefaultTab("bogus")).toBe("public-url")
  })

  it("returns health tab when requested", () => {
    expect(resolvePlatformSettingsDefaultTab("health")).toBe("health")
  })
})
