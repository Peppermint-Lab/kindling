//go:build linux

package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
)

func materializeCloudHypervisorCloneDisk(ctx context.Context, templateDisk, workDisk string) (strategy string, err error) {
	templateAbs, err := filepath.Abs(templateDisk)
	if err != nil {
		return "", fmt.Errorf("abs template disk: %w", err)
	}
	workAbs, err := filepath.Abs(workDisk)
	if err != nil {
		return "", fmt.Errorf("abs work disk: %w", err)
	}
	out, err := exec.CommandContext(ctx, "qemu-img", "create", "-f", "qcow2", "-F", "qcow2", "-b", templateAbs, workAbs).CombinedOutput()
	if err != nil {
		slog.Debug("cloud_hypervisor qcow2 overlay clone failed, falling back to full copy",
			"error", err, "output", string(out), "template", templateAbs, "work", workAbs)
		if err2 := copyFile(templateDisk, workDisk); err2 != nil {
			return "", fmt.Errorf("overlay: %w; full copy: %w", err, err2)
		}
		return "full_copy", nil
	}
	return "overlay", nil
}
