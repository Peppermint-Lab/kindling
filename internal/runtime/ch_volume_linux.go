//go:build linux

package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func (r *CloudHypervisorRuntime) ensurePersistentVolumeDisk(ctx context.Context, vol *PersistentVolumeMount) error {
	if vol == nil {
		return nil
	}
	if strings.TrimSpace(vol.HostPath) == "" {
		return fmt.Errorf("persistent volume host path is required")
	}
	if vol.SizeGB <= 0 {
		return fmt.Errorf("persistent volume size must be positive")
	}
	if err := ensureDir(filepath.Dir(vol.HostPath)); err != nil {
		return fmt.Errorf("create volume dir: %w", err)
	}
	if _, err := os.Stat(vol.HostPath); err == nil {
		return ensurePersistentVolumeSize(ctx, vol.HostPath, vol.SizeGB)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat volume disk: %w", err)
	} else if !vol.CreateIfMissing {
		return fmt.Errorf("persistent volume disk does not exist at %s", vol.HostPath)
	}

	emptyDir, err := os.MkdirTemp("", "kindling-volume-empty-")
	if err != nil {
		return fmt.Errorf("create temp volume source dir: %w", err)
	}
	defer os.RemoveAll(emptyDir)

	sizeArg := fmt.Sprintf("%dG", vol.SizeGB)
	if out, err := exec.CommandContext(ctx, "virt-make-fs", "--format=qcow2", "--type=ext4", "--size="+sizeArg, emptyDir, vol.HostPath).CombinedOutput(); err != nil {
		return fmt.Errorf("virt-make-fs volume: %s: %w", string(out), err)
	}
	return nil
}

func ensurePersistentVolumeSize(ctx context.Context, path string, sizeGB int) error {
	currentSize, err := qcow2VirtualSize(path)
	if err != nil {
		return fmt.Errorf("read qcow2 size: %w", err)
	}
	targetSize := int64(sizeGB) * 1024 * 1024 * 1024
	if currentSize >= targetSize {
		return nil
	}
	if out, err := exec.CommandContext(ctx, "qemu-img", "resize", path, fmt.Sprintf("%dG", sizeGB)).CombinedOutput(); err != nil {
		return fmt.Errorf("qemu-img resize: %s: %w", string(out), err)
	}
	return nil
}

func qcow2VirtualSize(path string) (int64, error) {
	out, err := exec.Command("qemu-img", "info", "--output=json", path).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("qemu-img info: %s: %w", string(out), err)
	}
	var meta struct {
		VirtualSize int64 `json:"virtual-size"`
	}
	if err := json.Unmarshal(out, &meta); err != nil {
		return 0, fmt.Errorf("decode qemu-img info: %w", err)
	}
	if meta.VirtualSize <= 0 {
		return 0, fmt.Errorf("qemu-img info returned invalid virtual size for %s", path)
	}
	return meta.VirtualSize, nil
}
