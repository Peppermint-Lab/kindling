import { useCallback, useEffect, useMemo, useState } from "react"
import { Link, Navigate, useNavigate } from "react-router-dom"
import { useAuth } from "@/contexts/AuthContext"
import {
  api,
  APIError,
  type AuthDeploymentKind,
  type OnboardingSnapshot,
  type Project,
  type ProjectDomain,
} from "@/lib/api"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { ThemeToggle } from "@/components/theme-toggle"
import { cn } from "@/lib/utils"

const HOSTED_STEPS = ["welcome", "capacity", "confirm", "project", "domain"] as const
const SELF_STEPS = ["welcome", "topology", "capacity", "confirm", "project", "domain"] as const

type HostedStep = (typeof HOSTED_STEPS)[number]
type SelfStep = (typeof SELF_STEPS)[number]
type StepId = HostedStep | SelfStep

function stepsForKind(kind: AuthDeploymentKind): readonly StepId[] {
  return kind === "hosted" ? HOSTED_STEPS : SELF_STEPS
}

function coerceTopology(v: unknown): "all_in_one" | "split" | "" {
  if (v === "all_in_one" || v === "split") return v
  return ""
}

function topologyChoiceLabel(topology: "all_in_one" | "split" | "") {
  if (topology === "split") return "Separate workers"
  if (topology === "all_in_one") return "All-in-one"
  return "—"
}

function coerceStep(kind: AuthDeploymentKind, raw: unknown): StepId {
  const order = stepsForKind(kind)
  const s = typeof raw === "string" ? raw : ""
  if (order.includes(s as StepId)) return s as StepId
  return order[0]
}

export function OnboardingPage() {
  const { session, refresh, logout, switchOrg } = useAuth()
  const navigate = useNavigate()
  const [snap, setSnap] = useState<OnboardingSnapshot | null>(null)
  const [wizard, setWizard] = useState<Record<string, unknown>>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [accessBusy, setAccessBusy] = useState<string | null>(null)

  const [installHint, setInstallHint] = useState<string | null>(null)
  const [projectForm, setProjectForm] = useState({
    name: "",
    github_repository: "",
    dockerfile_path: "Dockerfile",
    root_directory: "/",
  })
  const [createdProject, setCreatedProject] = useState<Project | null>(null)
  const [domainName, setDomainName] = useState("")
  const [domains, setDomains] = useState<ProjectDomain[]>([])
  const [domainsLoading, setDomainsLoading] = useState(false)

  const load = useCallback(async () => {
    const o = await api.getOnboarding()
    setSnap(o)
    return o
  }, [])

  const orgId = session?.authenticated ? session.organization.id : undefined

  useEffect(() => {
    if (!session?.authenticated) return
    let cancelled = false
    ;(async () => {
      setLoading(true)
      setError(null)
      try {
        const o = await load()
        if (cancelled) return
        setWizard((w) => (Object.keys(w).length > 0 ? w : { ...(o.wizard_state ?? {}) }))
      } catch (e) {
        if (!cancelled) setError(e instanceof APIError ? e.message : "Could not load onboarding")
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [session?.authenticated, orgId, load])

  const kind = session?.authenticated ? session.deployment_kind : "self_hosted"
  const stepOrder = useMemo(() => stepsForKind(kind), [kind])
  const step = useMemo(() => coerceStep(kind, wizard.step), [kind, wizard.step])
  const topology = useMemo(() => coerceTopology(wizard.topology), [wizard.topology])
  const projectId =
    typeof wizard.project_id === "string" && wizard.project_id.length > 0 ? wizard.project_id : null

  const persistWizard = async (next: Record<string, unknown>) => {
    setWizard(next)
    await api.patchOnboarding(next)
  }

  const goStep = async (nextStep: StepId) => {
    await persistWizard({ ...wizard, step: nextStep })
  }

  const stepIndex = stepOrder.indexOf(step)
  const canGoBack = stepIndex > 0

  useEffect(() => {
    if (!snap || !session?.authenticated) return
    if (step !== "capacity" && step !== "confirm") return
    const t = window.setInterval(() => {
      void load().catch(() => {})
    }, 2500)
    return () => clearInterval(t)
  }, [snap, session?.authenticated, step, load])

  useEffect(() => {
    if (!projectId || step !== "project" || createdProject) return
    let cancelled = false
    void api
      .getProject(projectId)
      .then((p) => {
        if (!cancelled) {
          setCreatedProject(p)
          setProjectForm((f) => ({
            ...f,
            name: p.name,
            github_repository: p.github_repository ?? "",
            dockerfile_path: p.dockerfile_path,
            root_directory: p.root_directory,
          }))
        }
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [projectId, step, createdProject])

  useEffect(() => {
    if (!projectId || step !== "domain") return
    let cancelled = false
    setDomainsLoading(true)
    void api
      .listProjectDomains(projectId)
      .then((d) => {
        if (!cancelled) setDomains(d)
      })
      .catch(() => {
        if (!cancelled) setDomains([])
      })
      .finally(() => {
        if (!cancelled) setDomainsLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [projectId, step])

  const hostedCapacityReady = (snap?.healthy_worker_agents ?? 0) >= 1
  const selfCapacityReady = (snap?.healthy_servers ?? 0) >= 1 && (snap?.servers_count ?? 0) >= 1

  const handleNext = async () => {
    const idx = stepOrder.indexOf(step)
    if (idx < 0 || idx >= stepOrder.length - 1) return
    const next = stepOrder[idx + 1]
    if (kind === "self_hosted" && step === "topology" && !topology) {
      setError("Choose where workloads run before continuing.")
      return
    }
    if (step === "capacity") {
      if (kind === "hosted" && !hostedCapacityReady) {
        setError("Wait until at least one worker is healthy, or check the install command on your server.")
        return
      }
      if (kind === "self_hosted" && !selfCapacityReady) {
        setError("Register a server and wait for a recent heartbeat (Settings → Servers).")
        return
      }
    }
    if (step === "project") {
      if (!createdProject && (!projectForm.name.trim() || busy)) {
        setError("Create a project before continuing.")
        return
      }
    }
    setError(null)
    await goStep(next)
  }

  const handleBack = async () => {
    const idx = stepOrder.indexOf(step)
    if (idx <= 0) return
    setError(null)
    await goStep(stepOrder[idx - 1])
  }

  const handleCreateProject = async () => {
    if (!projectForm.name.trim()) return
    setBusy(true)
    setError(null)
    try {
      const p = await api.createProject({
        name: projectForm.name.trim(),
        github_repository: projectForm.github_repository.trim() || undefined,
        dockerfile_path: projectForm.dockerfile_path.trim() || "Dockerfile",
        root_directory: projectForm.root_directory.trim() || "/",
        min_instance_count: 0,
        max_instance_count: 3,
        scale_to_zero_enabled: true,
      })
      setCreatedProject(p)
      const next = { ...wizard, step: "domain" as StepId, project_id: p.id }
      await persistWizard(next)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not create project")
    } finally {
      setBusy(false)
    }
  }

  const handleAddDomain = async () => {
    const pid = createdProject?.id ?? projectId
    if (!pid || !domainName.trim()) return
    setBusy(true)
    setError(null)
    try {
      await api.createProjectDomain(pid, domainName.trim())
      setDomainName("")
      const d = await api.listProjectDomains(pid)
      setDomains(d)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not add domain")
    } finally {
      setBusy(false)
    }
  }

  const handleVerifyDomain = async (domainId: string) => {
    const pid = createdProject?.id ?? projectId
    if (!pid) return
    setBusy(true)
    setError(null)
    try {
      await api.verifyProjectDomain(pid, domainId)
      setDomains(await api.listProjectDomains(pid))
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Verification failed")
    } finally {
      setBusy(false)
    }
  }

  const handleFinish = async () => {
    setBusy(true)
    setError(null)
    try {
      await api.completeOnboarding()
      await refresh()
      const pid = createdProject?.id ?? projectId
      if (pid) navigate(`/projects/${pid}`, { replace: true })
      else navigate("/projects", { replace: true })
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not complete onboarding")
    } finally {
      setBusy(false)
    }
  }

  const generateEnrollment = async () => {
    if (!session?.authenticated || kind !== "hosted") return
    setBusy(true)
    setError(null)
    try {
      const t = await api.createWorkerEnrollmentToken()
      setInstallHint(t.install_hint)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not create enrollment token")
    } finally {
      setBusy(false)
    }
  }

  const handleSwitchOrganization = async (organizationId: string) => {
    setAccessBusy(organizationId)
    try {
      const next = await switchOrg(organizationId)
      navigate(next.authenticated && next.needs_onboarding ? "/onboarding" : "/", {
        replace: true,
      })
    } finally {
      setAccessBusy(null)
    }
  }

  const handleLogout = async () => {
    setAccessBusy("logout")
    try {
      await logout()
      navigate("/login", { replace: true })
    } finally {
      setAccessBusy(null)
    }
  }

  if (!session) {
    return (
      <div className="flex min-h-svh items-center justify-center text-muted-foreground text-sm">Loading…</div>
    )
  }
  if (!session.authenticated) {
    return <Navigate to="/login" replace />
  }
  if (!session.needs_onboarding) {
    return <Navigate to="/" replace />
  }

  if (loading || !snap) {
    return (
      <div className="flex min-h-svh items-center justify-center text-muted-foreground text-sm">
        Loading onboarding…
      </div>
    )
  }

  if (!snap.can_edit) {
    const otherOrganizations = session.organizations.filter((org) => org.id !== session.organization.id)
    return (
      <div className="flex min-h-svh flex-col items-center justify-center gap-4 p-6">
        <p className="text-muted-foreground text-sm text-center max-w-md">
          {kind === "self_hosted" ? (
            <>
              Self-hosted cluster setup is shared across the control plane and must be completed by a platform admin.
              Ask a platform admin to finish onboarding before continuing in <strong>{session.organization.name}</strong>.
            </>
          ) : (
            <>
              Only organization owners and admins can run setup for <strong>{session.organization.name}</strong>. Ask an
              admin to sign in and complete onboarding.
            </>
          )}
        </p>
        <div className="flex flex-wrap items-center justify-center gap-2">
          <Button variant="outline" onClick={() => void refresh()} disabled={accessBusy !== null}>
            Refresh
          </Button>
          <Button variant="ghost" onClick={() => void handleLogout()} disabled={accessBusy !== null}>
            Sign out
          </Button>
        </div>
        {otherOrganizations.length > 0 ? (
          <div className="w-full max-w-md space-y-2 rounded-lg border p-4">
            <p className="text-sm font-medium">Switch organization</p>
            <p className="text-xs text-muted-foreground">
              Use an organization that already finished setup, or one where you can complete onboarding.
            </p>
            <div className="flex flex-wrap gap-2">
              {otherOrganizations.map((org) => (
                <Button
                  key={org.id}
                  type="button"
                  variant="outline"
                  onClick={() => void handleSwitchOrganization(org.id)}
                  disabled={accessBusy !== null}
                >
                  {accessBusy === org.id ? "Switching…" : org.name}
                </Button>
              ))}
            </div>
          </div>
        ) : null}
      </div>
    )
  }

  const progressLabel = `Step ${stepIndex + 1} of ${stepOrder.length}`

  return (
    <div className="min-h-svh flex flex-col">
      <header className="flex h-12 shrink-0 items-center justify-end border-b border-border/60 px-4 gap-2">
        <span className="text-xs text-muted-foreground mr-auto">{progressLabel}</span>
        <ThemeToggle />
      </header>

      <main className="flex flex-1 items-start justify-center p-6 pb-12">
        <Card className="w-full max-w-lg">
          <CardHeader>
            <CardTitle className="text-xl">Welcome to Kindling</CardTitle>
            <CardDescription>First-run setup — connect capacity, then your app.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-6">
            {error ? <p className="text-destructive text-sm">{error}</p> : null}

            {step === "welcome" ? (
              <div className="space-y-3 text-sm">
                <p>
                  You&apos;re setting up{" "}
                  <Badge variant="secondary">{kind === "hosted" ? "Kindling Cloud" : "Self-hosted Kindling"}</Badge>.
                </p>
                {kind === "hosted" ? (
                  <p className="text-muted-foreground">
                    The control plane runs on Kindling&apos;s infrastructure. You&apos;ll add your own Linux workers with a
                    one-time install command — no inbound firewall rules required.
                  </p>
                ) : (
                  <p className="text-muted-foreground">
                    You control the cluster. We&apos;ll confirm server registration and heartbeats, then guide you to a
                    single project and optional custom domain.
                  </p>
                )}
              </div>
            ) : null}

            {step === "topology" ? (
              <div className="space-y-4">
                <p className="text-sm text-muted-foreground">Where should workloads run?</p>
                <div className="grid gap-3">
                  <button
                    type="button"
                    onClick={() => void persistWizard({ ...wizard, step, topology: "all_in_one" })}
                    className={cn(
                      "rounded-lg border p-4 text-left text-sm transition-colors",
                      topology === "all_in_one"
                        ? "border-primary bg-primary/5"
                        : "border-border hover:bg-muted/40",
                    )}
                  >
                    <p className="font-medium">All-in-one</p>
                    <p className="text-muted-foreground mt-1">
                      API, database, reconcilers, and worker on the same Linux host. Simplest topology.
                    </p>
                  </button>
                  <button
                    type="button"
                    onClick={() => void persistWizard({ ...wizard, step, topology: "split" })}
                    className={cn(
                      "rounded-lg border p-4 text-left text-sm transition-colors",
                      topology === "split" ? "border-primary bg-primary/5" : "border-border hover:bg-muted/40",
                    )}
                  >
                    <p className="font-medium">Separate workers</p>
                    <p className="text-muted-foreground mt-1">
                      Control plane on one host; additional hosts run worker-only <code className="text-xs">kindling serve</code>{" "}
                      against the same database (see production docs for networking).
                    </p>
                  </button>
                </div>
              </div>
            ) : null}

            {step === "capacity" ? (
              <div className="space-y-4 text-sm">
                {kind === "self_hosted" && !topology ? (
                  <div className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm">
                    <p className="font-medium text-destructive">Choose a topology first</p>
                    <Button type="button" variant="outline" size="sm" className="mt-2" onClick={() => void goStep("topology")}>
                      Go back to topology
                    </Button>
                  </div>
                ) : null}
                {kind === "hosted" ? (
                  <>
                    <p className="text-muted-foreground">
                      Generate an enrollment token and run the install command on your Linux worker. This page refreshes
                      when the worker is healthy.
                    </p>
                    <div className="flex flex-wrap gap-2">
                      <Button type="button" size="sm" onClick={() => void generateEnrollment()} disabled={busy}>
                        {installHint ? "Regenerate install command" : "Generate install command"}
                      </Button>
                    </div>
                    {installHint ? (
                      <div className="space-y-2">
                        <Label>Install command</Label>
                        <pre className="text-xs bg-muted p-3 rounded-md overflow-x-auto whitespace-pre-wrap break-all">
                          {installHint}
                        </pre>
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          onClick={() => void navigator.clipboard.writeText(installHint)}
                        >
                          Copy command
                        </Button>
                      </div>
                    ) : null}
                    <div className="rounded-md border border-dashed p-3 space-y-2">
                      <p className="text-xs font-medium">Worker agents</p>
                      {snap.worker_agents && snap.worker_agents.length > 0 ? (
                        <ul className="space-y-1 text-xs">
                          {snap.worker_agents.map((w) => (
                            <li key={w.id} className="flex justify-between gap-2">
                              <span className="font-mono truncate">{w.hostname}</span>
                              <span className="text-muted-foreground shrink-0">
                                v{w.reported_version_applied}/{w.desired_version}
                              </span>
                            </li>
                          ))}
                        </ul>
                      ) : (
                        <p className="text-xs text-muted-foreground">No workers enrolled yet.</p>
                      )}
                      <p className="text-xs">
                        Healthy workers:{" "}
                        <strong>{snap.healthy_worker_agents ?? 0}</strong>
                      </p>
                    </div>
                  </>
                ) : (
                  <>
                    <p>
                      Chosen topology:{" "}
                      <Badge variant="outline">{topologyChoiceLabel(topology)}</Badge>
                    </p>
                    {topology === "all_in_one" ? (
                      <p className="text-muted-foreground">
                        Ensure this host is running Kindling with the worker component and check{" "}
                        <Link to="/settings" className="text-primary underline">
                          Settings → Servers
                        </Link>{" "}
                        for a recent heartbeat.
                      </p>
                    ) : (
                      <p className="text-muted-foreground">
                        Register every worker host against your cluster, then confirm heartbeats under{" "}
                        <Link to="/settings" className="text-primary underline">
                          Settings → Servers
                        </Link>
                        . Split topology requires correct PostgreSQL and private networking per your ops docs.
                      </p>
                    )}
                    <div className="rounded-md border border-dashed p-3 text-xs space-y-1">
                      <p>
                        Servers registered: <strong>{snap.servers_count ?? 0}</strong>
                      </p>
                      <p>
                        Healthy (recent heartbeat): <strong>{snap.healthy_servers ?? 0}</strong>
                      </p>
                    </div>
                  </>
                )}
              </div>
            ) : null}

            {step === "confirm" ? (
              <ul className="text-sm space-y-2 list-disc pl-5">
                <li>
                  Environment: {kind === "hosted" ? "Hosted control plane" : "Self-hosted"}
                  {kind === "self_hosted" && topology ? (
                    <>
                      {" "}
                      — {topology === "all_in_one" ? "all-in-one" : "separate workers"}
                    </>
                  ) : null}
                </li>
                <li>
                  Capacity:{" "}
                  {kind === "hosted"
                    ? `${snap.healthy_worker_agents ?? 0} healthy worker agent(s)`
                    : `${snap.healthy_servers ?? 0} healthy server(s) of ${snap.servers_count ?? 0}`}
                </li>
              </ul>
            ) : null}

            {step === "project" ? (
              <div className="space-y-4">
                <p className="text-sm text-muted-foreground">
                  Create your first project. One GitHub repository per project for v1.
                </p>
                <div className="space-y-2">
                  <Label htmlFor="onb-name">Project name</Label>
                  <Input
                    id="onb-name"
                    value={projectForm.name}
                    onChange={(e) => setProjectForm((f) => ({ ...f, name: e.target.value }))}
                    disabled={!!createdProject}
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="onb-gh">GitHub repository (optional)</Label>
                  <Input
                    id="onb-gh"
                    placeholder="owner/repo"
                    className="font-mono text-sm"
                    value={projectForm.github_repository}
                    onChange={(e) => setProjectForm((f) => ({ ...f, github_repository: e.target.value }))}
                    disabled={!!createdProject}
                  />
                </div>
                <div className="grid grid-cols-2 gap-3">
                  <div className="space-y-2">
                    <Label htmlFor="onb-df">Dockerfile path</Label>
                    <Input
                      id="onb-df"
                      value={projectForm.dockerfile_path}
                      onChange={(e) => setProjectForm((f) => ({ ...f, dockerfile_path: e.target.value }))}
                      disabled={!!createdProject}
                    />
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor="onb-root">Root directory</Label>
                    <Input
                      id="onb-root"
                      value={projectForm.root_directory}
                      onChange={(e) => setProjectForm((f) => ({ ...f, root_directory: e.target.value }))}
                      disabled={!!createdProject}
                    />
                  </div>
                </div>
                {createdProject ? (
                  <p className="text-sm text-muted-foreground">
                    Created <span className="font-medium text-foreground">{createdProject.name}</span>. Continue to add an
                    optional domain, or finish setup.
                  </p>
                ) : (
                  <Button type="button" onClick={() => void handleCreateProject()} disabled={busy}>
                    Create project
                  </Button>
                )}
              </div>
            ) : null}

            {step === "domain" ? (
              <div className="space-y-4">
                <p className="text-sm text-muted-foreground">
                  Optionally attach a custom domain to your project. You can skip this and add it later in the project
                  page.
                </p>
                <div className="flex gap-2 items-end">
                  <div className="flex-1 space-y-2">
                    <Label htmlFor="onb-dom">Hostname</Label>
                    <Input
                      id="onb-dom"
                      placeholder="www.example.com"
                      value={domainName}
                      onChange={(e) => setDomainName(e.target.value)}
                    />
                  </div>
                  <Button type="button" variant="secondary" onClick={() => void handleAddDomain()} disabled={busy}>
                    Add
                  </Button>
                </div>
                {domainsLoading ? (
                  <p className="text-xs text-muted-foreground">Loading domains…</p>
                ) : domains.length > 0 ? (
                  <ul className="space-y-3 text-sm border rounded-md p-3">
                    {domains.map((d) => (
                      <li key={d.id} className="space-y-1">
                        <div className="flex justify-between gap-2">
                          <span className="font-mono break-all">{d.domain_name}</span>
                          {d.verified_at ? (
                            <Badge variant="secondary">Verified</Badge>
                          ) : (
                            <VerifyDomainButton
                              onClick={() => void handleVerifyDomain(d.id)}
                              disabled={busy}
                            />
                          )}
                        </div>
                        {d.dns_challenge ? (
                          <p className="text-xs text-muted-foreground">
                            DNS: <span className="font-mono">{d.dns_challenge.name}</span> →{" "}
                            <span className="font-mono">{d.dns_challenge.value}</span>
                          </p>
                        ) : null}
                        {d.instructions ? (
                          <p className="text-xs text-muted-foreground whitespace-pre-wrap">{d.instructions}</p>
                        ) : null}
                      </li>
                    ))}
                  </ul>
                ) : null}
              </div>
            ) : null}
          </CardContent>
          <CardFooter className="flex flex-wrap gap-2 justify-between">
            <div className="flex gap-2">
              {canGoBack ? (
                <Button type="button" variant="outline" onClick={() => void handleBack()} disabled={busy}>
                  Back
                </Button>
              ) : null}
            </div>
            <div className="flex gap-2">
              {step === "domain" ? (
                <Button type="button" onClick={() => void handleFinish()} disabled={busy}>
                  {busy ? "Finishing…" : "Finish setup"}
                </Button>
              ) : null}
              {(step !== "project" || createdProject) && step !== "domain" ? (
                <Button type="button" onClick={() => void handleNext()} disabled={busy}>
                  Continue
                </Button>
              ) : null}
            </div>
          </CardFooter>
        </Card>
      </main>
    </div>
  )
}

function VerifyDomainButton({ onClick, disabled }: { onClick: () => void; disabled: boolean }) {
  return (
    <Button type="button" size="sm" variant="outline" onClick={onClick} disabled={disabled}>
      Verify
    </Button>
  )
}
