import { useEffect, useMemo, useState } from "react"
import { Link, useNavigate } from "react-router-dom"
import { CopyPlusIcon, Layers3Icon, SparklesIcon } from "lucide-react"
import { api, type Sandbox, type SandboxTemplate } from "@/lib/api"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import {
  formatAutoSuspend,
  hostGroupLabel,
  sandboxImageContract,
  sandboxImagePresets,
  sandboxPresetByID,
  suggestedSandboxName,
} from "@/lib/sandbox-catalog"

type CreateMode = "preset" | "custom" | "template"

type SandboxDraft = {
  name: string
  host_group: "linux-sandbox" | "mac-sandbox"
  base_image_ref: string
  vcpu: string
  memory_mb: string
  disk_gb: string
  auto_suspend_enabled: boolean
  auto_suspend_seconds: string
  desired_state: "running" | "stopped"
}

function draftFromPreset(presetID: string): SandboxDraft {
  const preset = sandboxPresetByID(presetID)
  return {
    name: suggestedSandboxName(preset.id),
    host_group: preset.host_group,
    base_image_ref: preset.base_image_ref,
    vcpu: String(preset.vcpu),
    memory_mb: String(preset.memory_mb),
    disk_gb: String(preset.disk_gb),
    auto_suspend_enabled: false,
    auto_suspend_seconds: "900",
    desired_state: "running",
  }
}

function templateStatusVariant(template: SandboxTemplate) {
  if (template.status === "ready") return "default" as const
  if (template.status === "failed") return "destructive" as const
  return "secondary" as const
}

export function SandboxesPage() {
  const navigate = useNavigate()
  const [sandboxes, setSandboxes] = useState<Sandbox[]>([])
  const [templates, setTemplates] = useState<SandboxTemplate[]>([])
  const [loading, setLoading] = useState(true)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [createMode, setCreateMode] = useState<CreateMode>("preset")
  const [selectedPresetID, setSelectedPresetID] = useState(sandboxImagePresets[0]?.id ?? "ubuntu-dev")
  const [draft, setDraft] = useState<SandboxDraft>(() => draftFromPreset(sandboxImagePresets[0]?.id ?? "ubuntu-dev"))
  const [cloneTemplateID, setCloneTemplateID] = useState("")
  const [cloneName, setCloneName] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const [quickLaunchID, setQuickLaunchID] = useState<string | null>(null)
  const [templateActionID, setTemplateActionID] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  const load = async () => {
    setLoading(true)
    setError(null)
    try {
      const [sandboxesValue, templatesValue] = await Promise.all([
        api.listSandboxes(),
        api.listSandboxTemplates(),
      ])
      setSandboxes(sandboxesValue)
      setTemplates(templatesValue)
      if (!cloneTemplateID) {
        const firstReadyTemplate = templatesValue.find((template) => template.status === "ready")
        if (firstReadyTemplate) {
          setCloneTemplateID(firstReadyTemplate.id)
          setCloneName(`${firstReadyTemplate.name}-clone`)
        }
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load sandboxes")
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [])

  const selectedPreset = useMemo(() => sandboxPresetByID(selectedPresetID), [selectedPresetID])
  const selectedTemplate = useMemo(
    () => templates.find((template) => template.id === cloneTemplateID) ?? null,
    [cloneTemplateID, templates],
  )
  const readyTemplates = useMemo(
    () => templates.filter((template) => template.status === "ready"),
    [templates],
  )

  const syncDraftPreset = (presetID: string) => {
    setSelectedPresetID(presetID)
    setDraft((current) => ({
      ...draftFromPreset(presetID),
      name: current.name.trim() ? current.name : suggestedSandboxName(presetID),
    }))
  }

  const openPresetDialog = (presetID: string) => {
    setCreateMode("preset")
    setSelectedPresetID(presetID)
    setDraft(draftFromPreset(presetID))
    setDialogOpen(true)
  }

  const openTemplateDialog = (template?: SandboxTemplate) => {
    setCreateMode("template")
    if (template) {
      setCloneTemplateID(template.id)
      setCloneName(`${template.name}-clone`)
    } else if (readyTemplates.length > 0) {
      setCloneTemplateID(readyTemplates[0].id)
      setCloneName(`${readyTemplates[0].name}-clone`)
    }
    setDialogOpen(true)
  }

  const submitCreate = async () => {
    setSubmitting(true)
    setError(null)
    try {
      if (createMode === "template") {
        if (!cloneTemplateID) {
          throw new Error("Choose a ready template to clone")
        }
        const created = await api.cloneSandboxTemplate(cloneTemplateID, {
          name: cloneName.trim() || undefined,
        })
        setDialogOpen(false)
        await load()
        navigate(`/sandboxes/${created.id}`)
        return
      }

      const nextVcpu = Number(draft.vcpu || "0")
      const nextMemory = Number(draft.memory_mb || "0")
      const nextDisk = Number(draft.disk_gb || "0")
      const nextAutoSuspend = draft.auto_suspend_enabled ? Number(draft.auto_suspend_seconds || "0") : 0
      if (!draft.name.trim()) throw new Error("Sandbox name is required")
      if (!draft.base_image_ref.trim()) throw new Error("Base image is required")
      if (!Number.isFinite(nextVcpu) || nextVcpu <= 0) throw new Error("vCPU must be a positive number")
      if (!Number.isFinite(nextMemory) || nextMemory <= 0) throw new Error("Memory must be a positive number")
      if (!Number.isFinite(nextDisk) || nextDisk <= 0) throw new Error("Disk must be a positive number")
      if (draft.auto_suspend_enabled && (!Number.isFinite(nextAutoSuspend) || nextAutoSuspend <= 0)) {
        throw new Error("Auto-suspend timeout must be a positive number of seconds")
      }
      const created = await api.createSandbox({
        name: draft.name.trim(),
        host_group: draft.host_group,
        base_image_ref: draft.base_image_ref.trim(),
        vcpu: nextVcpu,
        memory_mb: nextMemory,
        disk_gb: nextDisk,
        auto_suspend_seconds: nextAutoSuspend,
        desired_state: draft.desired_state,
      })
      setDialogOpen(false)
      await load()
      navigate(`/sandboxes/${created.id}`)
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create sandbox")
    } finally {
      setSubmitting(false)
    }
  }

  const quickLaunch = async (presetID: string) => {
    const preset = sandboxPresetByID(presetID)
    setQuickLaunchID(preset.id)
    setError(null)
    try {
      const created = await api.createSandbox({
        name: suggestedSandboxName(preset.id),
        host_group: preset.host_group,
        base_image_ref: preset.base_image_ref,
        vcpu: preset.vcpu,
        memory_mb: preset.memory_mb,
        disk_gb: preset.disk_gb,
        auto_suspend_seconds: 0,
        desired_state: "running",
      })
      await load()
      navigate(`/sandboxes/${created.id}`)
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to launch sandbox preset")
    } finally {
      setQuickLaunchID(null)
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Sandboxes</h1>
          <p className="text-sm text-muted-foreground">Persistent and disposable microVM workspaces across Linux and macOS workers.</p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => void load()} disabled={loading}>
            Refresh
          </Button>
          <Button onClick={() => openPresetDialog(selectedPresetID)}>
            <CopyPlusIcon className="mr-2 size-4" />
            New Sandbox
          </Button>
        </div>
      </div>

      {error ? (
        <Card>
          <CardContent className="pt-6 text-sm text-destructive">{error}</CardContent>
        </Card>
      ) : null}

      <Card>
        <CardHeader>
          <CardTitle>Recommended Images</CardTitle>
          <CardDescription>Use a curated Kindling-friendly base image when you want shell access, SSH, and package tooling to work with less setup.</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {sandboxImagePresets.map((preset) => (
            <div key={preset.id} className="rounded-xl border bg-muted/10 p-4">
              <div className="flex items-start justify-between gap-3">
                <div className="space-y-1">
                  <div className="flex items-center gap-2">
                    <p className="font-medium">{preset.name}</p>
                    <Badge variant={preset.ssh_readiness === "ready" ? "default" : "secondary"}>
                      {preset.ssh_readiness === "ready" ? "SSH-friendly" : "Manual setup"}
                    </Badge>
                  </div>
                  <p className="text-sm text-muted-foreground">{preset.description}</p>
                </div>
                <SparklesIcon className="mt-0.5 size-4 text-muted-foreground" />
              </div>
              <div className="mt-4 space-y-2 text-xs text-muted-foreground">
                <p>{hostGroupLabel(preset.host_group)} · {preset.vcpu} vCPU · {preset.memory_mb} MB · {preset.disk_gb} GB</p>
                <p className="font-mono text-[11px] break-all">{preset.base_image_ref}</p>
                <p>{preset.note}</p>
              </div>
              <div className="mt-4 flex gap-2">
                <Button
                  size="sm"
                  onClick={() => void quickLaunch(preset.id)}
                  disabled={quickLaunchID === preset.id}
                >
                  {quickLaunchID === preset.id ? "Launching..." : "Launch"}
                </Button>
                <Button size="sm" variant="outline" onClick={() => openPresetDialog(preset.id)}>
                  Customize
                </Button>
              </div>
            </div>
          ))}
        </CardContent>
      </Card>

      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        {sandboxes.map((sandbox) => (
          <Link key={sandbox.id} to={`/sandboxes/${sandbox.id}`} className="block">
            <Card className="h-full transition-colors hover:border-foreground/30">
              <CardHeader>
                <div className="flex items-start justify-between gap-3">
                  <div>
                    <CardTitle>{sandbox.name}</CardTitle>
                    <CardDescription>{sandbox.host_group}</CardDescription>
                  </div>
                  <Badge variant={sandbox.observed_state === "running" ? "default" : "secondary"}>
                    {sandbox.observed_state}
                  </Badge>
                </div>
              </CardHeader>
              <CardContent className="space-y-2 text-sm text-muted-foreground">
                <p>{sandbox.backend || "pending backend"} / {sandbox.arch || "pending arch"}</p>
                <p>{sandbox.vcpu} vCPU, {sandbox.memory_mb} MB RAM, {sandbox.disk_gb} GB disk</p>
                <p>{formatAutoSuspend(sandbox.auto_suspend_seconds)}</p>
                <p className="truncate">{sandbox.runtime_url || "No runtime URL yet"}</p>
              </CardContent>
            </Card>
          </Link>
        ))}
      </div>

      {!loading && sandboxes.length === 0 ? (
        <Card>
          <CardContent className="pt-6 text-sm text-muted-foreground">No sandboxes yet in this organization.</CardContent>
        </Card>
      ) : null}

      <Card>
        <CardHeader>
          <CardTitle>Templates</CardTitle>
          <CardDescription>Capture a sandbox snapshot from its detail page, then reuse it here for fast clones.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-sm text-muted-foreground">
              Ready templates keep the source image, resources, and placement so teams can stamp out repeatable environments.
            </p>
            <Button variant="outline" onClick={() => openTemplateDialog()} disabled={readyTemplates.length === 0}>
              <Layers3Icon className="mr-2 size-4" />
              Clone From Template
            </Button>
          </div>
          {templates.map((template) => (
            <div key={template.id} className="rounded-lg border p-4">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div className="space-y-2">
                  <div className="flex items-center gap-2">
                    <p className="font-medium">{template.name}</p>
                    <Badge variant={templateStatusVariant(template)}>{template.status}</Badge>
                  </div>
                  <p className="text-sm text-muted-foreground">
                    {hostGroupLabel(template.host_group)} · {template.vcpu} vCPU · {template.memory_mb} MB · {template.disk_gb} GB
                  </p>
                  <p className="font-mono text-xs text-muted-foreground break-all">{template.base_image_ref}</p>
                  {template.failure_message ? (
                    <p className="text-xs text-destructive">{template.failure_message}</p>
                  ) : template.status === "ready" ? (
                    <p className="text-xs text-muted-foreground">Snapshot ready for clone launches.</p>
                  ) : (
                    <p className="text-xs text-muted-foreground">Capture in progress. The source sandbox may still be stopping or snapshotting.</p>
                  )}
                </div>
                <div className="flex gap-2">
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={template.status !== "ready"}
                    onClick={() => openTemplateDialog(template)}
                  >
                    Clone
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={templateActionID === template.id}
                    onClick={() => {
                      if (!window.confirm(`Delete template ${template.name}?`)) return
                      setTemplateActionID(template.id)
                      setError(null)
                      void api.deleteSandboxTemplate(template.id).then(load).catch((err: unknown) => {
                        setError(err instanceof Error ? err.message : "Failed to delete template")
                      }).finally(() => {
                        setTemplateActionID(null)
                      })
                    }}
                  >
                    Delete
                  </Button>
                </div>
              </div>
            </div>
          ))}
          {!loading && templates.length === 0 ? (
            <p className="text-sm text-muted-foreground">No sandbox templates yet. Open a sandbox and capture one when you want a reusable clone source.</p>
          ) : null}
        </CardContent>
      </Card>

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="max-h-[90vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>New Sandbox</DialogTitle>
            <DialogDescription>
              Launch from a curated image, build a custom sandbox, or clone from a ready template.
            </DialogDescription>
          </DialogHeader>

          <Tabs value={createMode} onValueChange={(value) => setCreateMode(value as CreateMode)} className="gap-4">
            <TabsList>
              <TabsTrigger value="preset">Recommended</TabsTrigger>
              <TabsTrigger value="custom">Custom</TabsTrigger>
              <TabsTrigger value="template">Template</TabsTrigger>
            </TabsList>

            <TabsContent value="preset" className="space-y-4">
              <div className="grid gap-3 md:grid-cols-2">
                {sandboxImagePresets.map((preset) => (
                  <button
                    key={preset.id}
                    type="button"
                    className={`rounded-lg border p-3 text-left transition-colors ${selectedPresetID === preset.id ? "border-foreground/40 bg-accent/40" : "hover:border-foreground/20"}`}
                    onClick={() => syncDraftPreset(preset.id)}
                  >
                    <div className="flex items-center justify-between gap-3">
                      <p className="font-medium text-sm">{preset.name}</p>
                      <Badge variant={preset.ssh_readiness === "ready" ? "default" : "secondary"}>
                        {preset.ssh_readiness === "ready" ? "SSH-friendly" : "Manual setup"}
                      </Badge>
                    </div>
                    <p className="mt-2 text-xs text-muted-foreground">{preset.description}</p>
                    <p className="mt-2 text-xs text-muted-foreground">{preset.vcpu} vCPU · {preset.memory_mb} MB · {preset.disk_gb} GB</p>
                  </button>
                ))}
              </div>
              <div className="rounded-lg border bg-muted/20 p-3 text-sm">
                <p className="font-medium">{selectedPreset.name}</p>
                <p className="mt-1 text-muted-foreground">{selectedPreset.note}</p>
                <p className="mt-2 font-mono text-xs text-muted-foreground break-all">{selectedPreset.base_image_ref}</p>
              </div>
              <div className="space-y-2">
                <Label htmlFor="preset-name">Sandbox Name</Label>
                <Input
                  id="preset-name"
                  value={draft.name}
                  onChange={(e) => setDraft((current) => ({ ...current, name: e.target.value }))}
                />
              </div>
            </TabsContent>

            <TabsContent value="custom" className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="sandbox-name">Sandbox Name</Label>
                <Input
                  id="sandbox-name"
                  value={draft.name}
                  onChange={(e) => setDraft((current) => ({ ...current, name: e.target.value }))}
                />
              </div>
              <div className="grid gap-4 sm:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="sandbox-host-group">Host Group</Label>
                  <select
                    id="sandbox-host-group"
                    className="w-full rounded-md border bg-background px-3 py-2 text-sm"
                    value={draft.host_group}
                    onChange={(e) => setDraft((current) => ({ ...current, host_group: e.target.value as SandboxDraft["host_group"] }))}
                  >
                    <option value="linux-sandbox">Linux sandbox</option>
                    <option value="mac-sandbox">macOS sandbox</option>
                  </select>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="sandbox-state">Initial State</Label>
                  <select
                    id="sandbox-state"
                    className="w-full rounded-md border bg-background px-3 py-2 text-sm"
                    value={draft.desired_state}
                    onChange={(e) => setDraft((current) => ({ ...current, desired_state: e.target.value as SandboxDraft["desired_state"] }))}
                  >
                    <option value="running">Start immediately</option>
                    <option value="stopped">Create stopped</option>
                  </select>
                </div>
              </div>
              <div className="space-y-2">
                <Label htmlFor="sandbox-image">Base Image</Label>
                <Input
                  id="sandbox-image"
                  value={draft.base_image_ref}
                  className="font-mono text-sm"
                  onChange={(e) => setDraft((current) => ({ ...current, base_image_ref: e.target.value }))}
                />
                <p className="text-xs text-muted-foreground">{sandboxImageContract(draft.base_image_ref).description}</p>
              </div>
              <div className="grid gap-4 sm:grid-cols-3">
                <div className="space-y-2">
                  <Label htmlFor="sandbox-vcpu">vCPU</Label>
                  <Input
                    id="sandbox-vcpu"
                    type="number"
                    min="1"
                    value={draft.vcpu}
                    onChange={(e) => setDraft((current) => ({ ...current, vcpu: e.target.value }))}
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="sandbox-memory">Memory (MB)</Label>
                  <Input
                    id="sandbox-memory"
                    type="number"
                    min="128"
                    step="128"
                    value={draft.memory_mb}
                    onChange={(e) => setDraft((current) => ({ ...current, memory_mb: e.target.value }))}
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="sandbox-disk">Disk (GB)</Label>
                  <Input
                    id="sandbox-disk"
                    type="number"
                    min="1"
                    value={draft.disk_gb}
                    onChange={(e) => setDraft((current) => ({ ...current, disk_gb: e.target.value }))}
                  />
                </div>
              </div>
            </TabsContent>

            <TabsContent value="template" className="space-y-4">
              {readyTemplates.length === 0 ? (
                <div className="rounded-lg border border-dashed p-4 text-sm text-muted-foreground">
                  No ready templates yet. Open a sandbox detail page and use Capture Template first.
                </div>
              ) : (
                <>
                  <div className="space-y-2">
                    <Label htmlFor="template-select">Template</Label>
                    <select
                      id="template-select"
                      className="w-full rounded-md border bg-background px-3 py-2 text-sm"
                      value={cloneTemplateID}
                      onChange={(e) => {
                        const nextID = e.target.value
                        setCloneTemplateID(nextID)
                        const nextTemplate = templates.find((template) => template.id === nextID)
                        if (nextTemplate) {
                          setCloneName(`${nextTemplate.name}-clone`)
                        }
                      }}
                    >
                      {readyTemplates.map((template) => (
                        <option key={template.id} value={template.id}>
                          {template.name}
                        </option>
                      ))}
                    </select>
                  </div>
                  {selectedTemplate ? (
                    <div className="rounded-lg border bg-muted/20 p-3 text-sm">
                      <p className="font-medium">{selectedTemplate.name}</p>
                      <p className="mt-1 text-muted-foreground">
                        {hostGroupLabel(selectedTemplate.host_group)} · {selectedTemplate.vcpu} vCPU · {selectedTemplate.memory_mb} MB · {selectedTemplate.disk_gb} GB
                      </p>
                      <p className="mt-2 font-mono text-xs text-muted-foreground break-all">{selectedTemplate.base_image_ref}</p>
                    </div>
                  ) : null}
                  <div className="space-y-2">
                    <Label htmlFor="clone-name">Cloned Sandbox Name</Label>
                    <Input
                      id="clone-name"
                      value={cloneName}
                      onChange={(e) => setCloneName(e.target.value)}
                    />
                  </div>
                </>
              )}
            </TabsContent>
          </Tabs>

          <div className="rounded-lg border bg-muted/20 p-3 text-xs text-muted-foreground">
            {createMode === "template"
              ? "Template clones start with the captured image and resources. They default to always-on, then you can adjust lifecycle later."
              : "New sandboxes default to always-on. Turn on auto-suspend only when you explicitly want idle cost control."}
          </div>

          {createMode !== "template" ? (
            <div className="grid gap-4 sm:grid-cols-[auto_140px] sm:items-end">
              <label className="flex items-start gap-3 rounded-lg border p-3 text-sm">
                <input
                  type="checkbox"
                  className="mt-0.5 size-4"
                  checked={draft.auto_suspend_enabled}
                  onChange={(e) => setDraft((current) => ({ ...current, auto_suspend_enabled: e.target.checked }))}
                />
                <span>
                  <span className="block font-medium">Enable auto-suspend</span>
                  <span className="block text-muted-foreground">Leave this off for always-on sandboxes.</span>
                </span>
              </label>
              <div className="space-y-2">
                <Label htmlFor="sandbox-auto-suspend">Idle timeout (seconds)</Label>
                <Input
                  id="sandbox-auto-suspend"
                  type="number"
                  min="1"
                  step="1"
                  disabled={!draft.auto_suspend_enabled}
                  value={draft.auto_suspend_seconds}
                  onChange={(e) => setDraft((current) => ({ ...current, auto_suspend_seconds: e.target.value }))}
                />
              </div>
            </div>
          ) : null}

          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              Cancel
            </Button>
            <Button
              onClick={() => void submitCreate()}
              disabled={submitting || (createMode === "template" && readyTemplates.length === 0)}
            >
              {submitting ? "Working..." : createMode === "template" ? "Clone Sandbox" : "Create Sandbox"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
