import { useEffect, useState, type FormEvent } from "react"
import { api, type UserSSHKey } from "@/lib/api"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

export function SSHKeysPage() {
  const [keys, setKeys] = useState<UserSSHKey[]>([])
  const [name, setName] = useState("")
  const [publicKey, setPublicKey] = useState("")
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const load = async () => {
    setLoading(true)
    setError(null)
    try {
      setKeys(await api.listUserSSHKeys())
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load SSH keys")
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [])

  const onSubmit = async (event: FormEvent) => {
    event.preventDefault()
    setSaving(true)
    setError(null)
    try {
      await api.createUserSSHKey({ name, public_key: publicKey })
      setName("")
      setPublicKey("")
      await load()
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to add SSH key")
    } finally {
      setSaving(false)
    }
  }

  const removeKey = async (id: string) => {
    setError(null)
    try {
      await api.deleteUserSSHKey(id)
      await load()
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to delete SSH key")
    }
  }

  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">SSH Keys</h1>
        <p className="text-sm text-muted-foreground">Keys here are reconciled into running sandboxes for the dedicated <span className="font-mono">kindling</span> user.</p>
      </div>

      {error ? (
        <Card>
          <CardContent className="pt-6 text-sm text-destructive">{error}</CardContent>
        </Card>
      ) : null}

      <Card>
        <CardHeader>
          <CardTitle>Add Key</CardTitle>
          <CardDescription>Paste a standard OpenSSH public key from your local machine.</CardDescription>
        </CardHeader>
        <CardContent>
          <form className="space-y-4" onSubmit={onSubmit}>
            <div className="space-y-2">
              <Label htmlFor="ssh-key-name">Name</Label>
              <Input id="ssh-key-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Work laptop" />
            </div>
            <div className="space-y-2">
              <Label htmlFor="ssh-public-key">Public Key</Label>
              <textarea
                id="ssh-public-key"
                value={publicKey}
                onChange={(e) => setPublicKey(e.target.value)}
                placeholder="ssh-ed25519 AAAA..."
                className="min-h-32 w-full rounded-md border bg-background px-3 py-2 font-mono text-sm"
              />
            </div>
            <Button type="submit" disabled={saving || publicKey.trim() === ""}>
              {saving ? "Saving…" : "Add SSH Key"}
            </Button>
          </form>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Your Keys</CardTitle>
          <CardDescription>{loading ? "Loading…" : `${keys.length} active key${keys.length === 1 ? "" : "s"}`}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          {keys.map((key) => (
            <div key={key.id} className="rounded-lg border p-4">
              <div className="flex items-center justify-between gap-3">
                <div>
                  <p className="font-medium">{key.name || "SSH key"}</p>
                  <p className="text-xs text-muted-foreground">{new Date(key.created_at).toLocaleString()}</p>
                </div>
                <Button variant="outline" size="sm" onClick={() => void removeKey(key.id)}>
                  Delete
                </Button>
              </div>
              <pre className="mt-3 overflow-x-auto whitespace-pre-wrap rounded-md bg-muted/50 p-3 font-mono text-xs">{key.public_key}</pre>
            </div>
          ))}
          {!loading && keys.length === 0 ? (
            <p className="text-sm text-muted-foreground">No SSH keys added yet.</p>
          ) : null}
        </CardContent>
      </Card>
    </div>
  )
}
