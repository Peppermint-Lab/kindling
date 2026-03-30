import { afterEach, describe, expect, it, vi } from "vitest"

import { api } from "@/lib/api"

describe("sandbox API parsing", () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it("accepts raw sandbox arrays", async () => {
    const sandboxes = [
      {
        id: "vm-1",
        name: "Alpha",
        host_group: "linux-remote-vm",
        base_image_ref: "ghcr.io/kindling/ubuntu-dev:latest",
        vcpu: 2,
        memory_mb: 2048,
        disk_gb: 20,
        desired_state: "running",
        observed_state: "running",
        auto_suspend_seconds: 0,
      },
    ]
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify(sandboxes), { status: 200 }),
    )

    await expect(api.listSandboxes()).resolves.toEqual(sandboxes)
  })

  it("unwraps paginated sandbox list envelopes", async () => {
    const sandboxes = [
      {
        id: "vm-1",
        name: "Alpha",
        host_group: "linux-remote-vm",
        base_image_ref: "ghcr.io/kindling/ubuntu-dev:latest",
        vcpu: 2,
        memory_mb: 2048,
        disk_gb: 20,
        desired_state: "running",
        observed_state: "running",
        auto_suspend_seconds: 0,
      },
    ]
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ items: sandboxes, total: 1, limit: 50, offset: 0 }), { status: 200 }),
    )

    await expect(api.listSandboxes()).resolves.toEqual(sandboxes)
  })
})
