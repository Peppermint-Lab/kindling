# kindling-mac: Local Linux VMs on macOS

Run Linux microVMs directly on your Mac using Apple's Virtualization Framework — no cloud, no server, just a local daemon.

## Quick Start

```bash
# Install kindling-mac
brew install kindlingvm/tap/kindling-mac

# Or download binaries from releases
curl -fsSL https://github.com/kindlingvm/kindling/releases/latest/download/kindling-mac -o /usr/local/bin/kindling-mac
curl -fsSL https://github.com/kindlingvm/kindling/releases/latest/download/kindling -o /usr/local/bin/kindling
chmod +x /usr/local/bin/kindling /usr/local/bin/kindling-mac

# Start the daemon
kindling-mac

# In another terminal: start your box VM (like WSL)
kindling local box start

# Open a shell in the VM
kindling local box shell
```

## Requirements

- macOS 13+ (Ventura or later)
- Apple Silicon (M-series chip)
- The `com.apple.security.virtualization` entitlement (granted automatically by the installer)

## How It Works

```
                    kindling CLI
                         │
                         ▼
            ┌──────────────────────────┐
            │   kindling-mac daemon    │
            │  ~/.kindling-mac.sock   │
            └───────────┬──────────────┘
                        │
        ┌───────────────┴───────────────┐
        │                               │
        ▼                               ▼
  ┌───────────┐                 ┌───────────┐
  │    box    │                 │   temp-1  │
  │  (main VM)│                 │  (ephemeral)│
  │  persistent│               │  disposable │
  └───────────┘                └─────────────┘
```

- **box**: A persistent Linux VM that boots once and stays running. Your day-to-day Linux environment.
- **temp**: Ephemeral disposable VMs for agents, CI, and short-lived tasks. Fast to start, deleted when stopped.

## Setup

### 1. Download the kernel and initramfs

```bash
mkdir -p ~/.kindling-mac
curl -fsSL https://github.com/kindlingvm/kindling/releases/download/v0.1.0/vmlinuz-arm64 -o ~/.kindling-mac/vmlinuz
curl -fsSL https://github.com/kindlingvm/kindling/releases/download/v0.1.0/initramfs-arm64.cpio.gz -o ~/.kindling-mac/initramfs.cpio.gz
```

### 2. Configure

```bash
cp /usr/local/etc/kindling-mac.yaml ~/.kindling-mac.yaml
$EDITOR ~/.kindling-mac.yaml
```

### 3. Start the daemon

```bash
kindling-mac
```

### 4. Start your box VM

```bash
kindling local box start
```

## CLI Reference

```bash
# Daemon
kindling local status          # Show daemon and all VM status

# Box VM (persistent)
kindling local box start    # Start (or resume) the box VM
kindling local box stop     # Stop the box VM
kindling local box status   # Show box status
kindling local box shell    # Open an interactive shell

# Temp VMs (ephemeral)
kindling local temp create          # Create a new temp VM
kindling local temp create --template mytemplate  # Clone from template (fast)
kindling local temp list            # List all temp VMs
kindling local temp delete <id>     # Delete an temp VM
kindling local temp exec <id> -- ls /app  # Run a command in a temp VM

# Templates (for fast temp cloning)
kindling local template list        # List available templates
kindling local template capture <vm_id> <name>  # Capture a VM as a template
kindling local template delete <id>  # Delete a template

# All VMs
kindling local list            # List all local VMs
```

## Auto-start on Login

```bash
# Install the launchd plist
cp /usr/local/opt/kindling-mac/homebrew.mxcl.kindling-mac.plist ~/Library/LaunchAgents/
launchctl load ~/Library/LaunchAgents/homebrew.mxcl.kindling-mac.plist
```

## Shared Folders

Mount macOS directories into your Linux VM:

```yaml
# ~/.kindling-mac.yaml
box:
  shared_folders:
    - host_path: "~/projects"   # macOS path
      guest_path: "/workspace"   # Linux mount point
```

## Rosetta (x86_64 on Apple Silicon)

Run Intel/AMD binaries inside the Linux VM without emulation:

```yaml
box:
  rosetta: true
```

## Architecture

- **Daemon** (`kindling-mac`): Manages all VMs via Apple Virtualization Framework. Communicates over a Unix socket at `~/.kindling-mac.sock`.
- **CLI** (`kindling local`): Connects to the daemon socket and issues commands. Same binary as the server `kindling` CLI.
- **State**: SQLite at `~/.kindling-mac/state.db`. No PostgreSQL required.
- **Guest agent**: The same `cmd/guest-agent` binary used by the Linux Kindling server runs inside each microVM as PID 1.

## Comparison

| | kindling-mac | Box for Mac |
|---|---|---|
| Binary | `kindling-mac` daemon | `box-mac` daemon |
| VMs | Linux microVMs | Linux microVMs |
| State | Local SQLite | Local SQLite |
| Use case | Dev environments, agents | Dev environments, agents |
| Entry point | `kindling local` | `box vm` |
