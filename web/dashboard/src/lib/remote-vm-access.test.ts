import { describe, expect, it } from "vitest"

import { publicBrowserAppUrl } from "@/lib/remote-vm-access"

const baseSandbox = {
  id: "59139d47-c480-42f7-b9c7-7fd0e00e4e43",
  name: "ubuntu-dev-1910",
  host_group: "linux-remote-vm",
  base_image_ref: "ghcr.io/kindling/ubuntu-dev:latest",
  vcpu: 2,
  memory_mb: 2048,
  disk_gb: 20,
  desired_state: "running",
  observed_state: "running",
  auto_suspend_seconds: 0,
}

describe("publicBrowserAppUrl", () => {
  it("adds http when runtime_url is a bare host and port", () => {
    const url = publicBrowserAppUrl({
      ...baseSandbox,
      runtime_url: "145.239.71.199:41047",
    })

    expect(url).toBe("http://145.239.71.199:41047")
  })

  it("preserves runtime_url schemes when already present", () => {
    const url = publicBrowserAppUrl({
      ...baseSandbox,
      runtime_url: "https://vm.example.test",
    })

    expect(url).toBe("https://vm.example.test")
  })
})
