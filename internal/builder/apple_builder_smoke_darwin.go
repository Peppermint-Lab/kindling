//go:build darwin

package builder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const builderSmokeTestTimeout = 4 * time.Minute // timeout for builder VM smoke test

// SmokeTestAppleBuilderVM boots the builder microVM (same paths as serve on macOS) and runs
// `buildah version` in the guest. Used by `kindling debug builder-vm-smoke`; must run from a
// binary signed with the Virtualization entitlement (see Makefile / contrib/kindling.entitlements).
func SmokeTestAppleBuilderVM(ctx context.Context) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	kernel := filepath.Join(home, ".kindling", "vmlinuz.bin")
	initrd := filepath.Join(home, ".kindling", "initramfs.cpio.gz")
	rootfs := filepath.Join(home, ".kindling", "builder-rootfs")

	if _, err := NewAppleVZBuildRunner(AppleVZBuildRunnerConfig{
		KernelPath:       kernel,
		InitramfsPath:    initrd,
		BuilderRootfsDir: rootfs,
	}); err != nil {
		return fmt.Errorf("builder VM prereqs: %w", err)
	}

	ws, err := os.MkdirTemp("", "kindling-builder-smoke-*")
	if err != nil {
		return fmt.Errorf("create temp workspace: %w", err)
	}
	defer os.RemoveAll(ws)

	vm, err := newAppleBuilderVM(kernel, initrd, rootfs, ws, "builder")
	if err != nil {
		return fmt.Errorf("create builder VM: %w", err)
	}
	defer vm.Close()

	runCtx, cancel := context.WithTimeout(ctx, builderSmokeTestTimeout)
	defer cancel()

	if err := vm.start(runCtx); err != nil {
		return fmt.Errorf("start builder vm: %w", err)
	}

	env := []string{"PATH=/usr/local/bin:/usr/bin:/bin:/sbin:/usr/sbin"}
	code, err := vm.Exec(runCtx, []string{"buildah", "version"}, "/workspace", env, func(line string) { fmt.Println(line) })
	if err != nil {
		return fmt.Errorf("exec buildah version: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("buildah version exited %d", code)
	}
	return nil
}
