//go:build darwin

package builder

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAppleBuilderVM_BuildahVersion boots the Apple VZ builder microVM and runs `buildah version`
// inside the guest chroot. Requires:
//   - ~/.kindling/vmlinuz.bin, initramfs.cpio.gz (same as kindling serve on macOS)
//   - ~/.kindling/builder-rootfs with Linux buildah tree (see CLAUDE.md)
//
// Opt-in: KINDLING_BUILDER_VM_E2E=1
//
// Note: the test binary from "go test" is not codesigned with com.apple.security.virtualization.
// For a real run, use: make build && ./bin/kindling debug builder-vm-smoke
func TestAppleBuilderVM_BuildahVersion(t *testing.T) {
	if os.Getenv("KINDLING_BUILDER_VM_E2E") != "1" {
		t.Skip("set KINDLING_BUILDER_VM_E2E=1 to run this Apple Virtualization smoke test (usually fails without VZ entitlement — use bin/kindling debug builder-vm-smoke)")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	kernel := filepath.Join(home, ".kindling", "vmlinuz.bin")
	initrd := filepath.Join(home, ".kindling", "initramfs.cpio.gz")
	rootfs := filepath.Join(home, ".kindling", "builder-rootfs")

	if _, err := NewAppleVZBuildRunner(AppleVZBuildRunnerConfig{
		KernelPath:       kernel,
		InitramfsPath:    initrd,
		BuilderRootfsDir: rootfs,
	}); err != nil {
		t.Fatalf("builder VM prereqs: %v", err)
	}

	ws := t.TempDir()
	vm, err := newAppleBuilderVM(kernel, initrd, rootfs, ws, "builder")
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	if err := vm.start(ctx); err != nil {
		t.Fatalf("start builder vm: %v", err)
	}

	env := []string{"PATH=/usr/local/bin:/usr/bin:/bin:/sbin:/usr/sbin"}
	code, err := vm.Exec(ctx, []string{"buildah", "version"}, "/workspace", env, func(line string) { t.Log(line) })
	if err != nil {
		t.Fatalf("exec buildah version: %v", err)
	}
	if code != 0 {
		t.Fatalf("buildah version exited %d", code)
	}
}
