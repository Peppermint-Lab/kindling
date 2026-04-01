import { describe, expect, it } from "vitest"

import type { Server } from "@/lib/api"
import { ratioPercent, sortControlPlaneHosts } from "@/lib/server-observability"

type HostMetrics = NonNullable<Server["host_metrics"]>

function makeHostMetrics(overrides: Partial<HostMetrics>): HostMetrics {
  return {
    sample_health: "fresh",
    cpu_percent: 0,
    load_avg_1m: 0,
    load_avg_5m: 0,
    load_avg_15m: 0,
    memory_total_bytes: 0,
    memory_available_bytes: 0,
    memory_used_bytes: 0,
    disk_total_bytes: 0,
    disk_free_bytes: 0,
    disk_used_bytes: 0,
    disk_read_bytes_per_sec: 0,
    disk_write_bytes_per_sec: 0,
    state_disk_total_bytes: 0,
    state_disk_free_bytes: 0,
    state_disk_used_bytes: 0,
    ...overrides,
  }
}

function makeServer(overrides: Partial<Server> & Pick<Server, "id" | "hostname">): Server {
  return {
    internal_ip: "",
    status: "active",
    last_heartbeat_at: "2026-04-01T12:00:00Z",
    created_at: "2026-04-01T12:00:00Z",
    ...overrides,
  }
}

describe("ratioPercent", () => {
  it("guards invalid totals", () => {
    expect(ratioPercent(10, 0)).toBe(0)
    expect(ratioPercent(undefined, 100)).toBe(0)
  })

  it("returns a bounded percentage", () => {
    expect(ratioPercent(50, 200)).toBe(25)
    expect(ratioPercent(500, 200)).toBe(100)
  })
})

describe("sortControlPlaneHosts", () => {
  it("orders unhealthy hosts first, then cpu, then memory", () => {
    const hosts = [
      makeServer({
        id: "healthy-low",
        hostname: "healthy-low",
        health: "healthy",
        host_metrics: makeHostMetrics({
          sample_health: "fresh",
          cpu_percent: 20,
          memory_used_bytes: 100,
        }),
      }),
      makeServer({
        id: "healthy-high",
        hostname: "healthy-high",
        health: "healthy",
        host_metrics: makeHostMetrics({
          sample_health: "fresh",
          cpu_percent: 80,
          memory_used_bytes: 50,
        }),
      }),
      makeServer({
        id: "stale-host",
        hostname: "stale-host",
        health: "healthy",
        host_metrics: makeHostMetrics({
          sample_health: "stale",
          cpu_percent: 5,
          memory_used_bytes: 10,
        }),
      }),
      makeServer({
        id: "healthy-high-memory",
        hostname: "healthy-high-memory",
        health: "healthy",
        host_metrics: makeHostMetrics({
          sample_health: "fresh",
          cpu_percent: 80,
          memory_used_bytes: 500,
        }),
      }),
    ]

    expect(sortControlPlaneHosts(hosts).map((host) => host.id)).toEqual([
      "stale-host",
      "healthy-high-memory",
      "healthy-high",
      "healthy-low",
    ])
  })
})
