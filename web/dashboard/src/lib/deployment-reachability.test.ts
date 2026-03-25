import { describe, expect, it } from "vitest"

import {
  countAdditionalPublicEndpoints,
  isPrimaryPublicURLRedirect,
  selectLatestRunningDeployment,
} from "./deployment-reachability"
import type { Deployment } from "@/lib/api"

describe("selectLatestRunningDeployment", () => {
  it("picks the running deployment with the newest created_at", () => {
    const deployments = [
      {
        id: "older-running",
        project_id: "p1",
        github_commit: "main",
        phase: "running",
        running_at: "2026-03-25T11:00:00Z",
        created_at: "2026-03-25T10:00:00Z",
      },
      {
        id: "newer-running",
        project_id: "p1",
        github_commit: "main",
        phase: "running",
        running_at: "2026-03-25T09:00:00Z",
        created_at: "2026-03-25T12:00:00Z",
      },
      {
        id: "failed-newest",
        project_id: "p1",
        github_commit: "main",
        phase: "failed",
        running_at: "2026-03-25T13:00:00Z",
        failed_at: "2026-03-25T13:05:00Z",
        created_at: "2026-03-25T13:00:00Z",
      },
    ] as Deployment[]

    expect(selectLatestRunningDeployment(deployments)?.id).toBe("newer-running")
  })
})

describe("reachability helpers", () => {
  const reachable = {
    public_url: "https://app.example.com",
    runtime_url: "http://127.0.0.1:32768",
    domain: "app.example.com",
    vm_ip: "127.0.0.1",
    port: 32768,
    proxies_to_deployment: true,
    public_endpoints: [
      {
        domain: "app.example.com",
        public_url: "https://app.example.com",
        proxies_to_deployment: true,
      },
      {
        domain: "www.example.com",
        public_url: "https://www.example.com",
        redirect_to: "https://kindling.example.com",
        redirect_status_code: 302,
        proxies_to_deployment: false,
      },
    ],
  }

  it("counts additional public endpoints beyond the primary", () => {
    expect(countAdditionalPublicEndpoints(reachable)).toBe(1)
  })

  it("detects when the primary public URL is a redirecting entry point", () => {
    expect(isPrimaryPublicURLRedirect(reachable)).toBe(false)
    expect(
      isPrimaryPublicURLRedirect({
        ...reachable,
        proxies_to_deployment: false,
      }),
    ).toBe(true)
  })
})
