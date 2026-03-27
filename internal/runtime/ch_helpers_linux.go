//go:build linux

package runtime

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/kindlingvm/kindling/internal/oci"
)

func (r *CloudHypervisorRuntime) captureOutputCH(ai *cloudHypervisorInstance, rd interface{ Read([]byte) (int, error) }) {
	scanner := bufio.NewScanner(rd)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		ai.logMu.Lock()
		ai.logs = append(ai.logs, line)
		if len(ai.logs) > 1000 {
			ai.logs = ai.logs[len(ai.logs)-1000:]
		}
		ai.logMu.Unlock()
	}
}

func tailCloudHypervisorLogs(ai *cloudHypervisorInstance, max int) string {
	ai.logMu.Lock()
	defer ai.logMu.Unlock()
	if len(ai.logs) == 0 {
		return ""
	}
	if max > len(ai.logs) {
		max = len(ai.logs)
	}
	return strings.Join(ai.logs[len(ai.logs)-max:], "\n")
}

func cloudHypervisorWorkDir(id uuid.UUID) string {
	return filepath.Join(os.TempDir(), "kindling-ch-"+id.String())
}

func cloudHypervisorVMPIDPath(workDir string) string {
	return filepath.Join(workDir, "cloud-hypervisor.pid")
}

func cloudHypervisorBridgePIDPath(workDir string) string {
	return filepath.Join(workDir, "cloud-hypervisor-bridge.pid")
}

func (r *CloudHypervisorRuntime) sharedRootfsPath(id uuid.UUID) (string, bool) {
	if strings.TrimSpace(r.sharedRootfsDir) == "" {
		return "", false
	}
	return sharedRootfsPathForID(r.sharedRootfsDir, id), true
}

func sharedRootfsPathForID(base string, id uuid.UUID) string {
	return filepath.Join(base, id.String(), "rootfs.qcow2")
}

func sharedRootfsRefFromWorkDisk(sharedDir, workDisk string) string {
	if strings.TrimSpace(sharedDir) == "" {
		return ""
	}
	cleanDisk := filepath.Clean(workDisk)
	cleanBase := filepath.Clean(sharedDir) + string(os.PathSeparator)
	if strings.HasPrefix(cleanDisk, cleanBase) {
		return cleanDisk
	}
	return ""
}

func exportImageToDir(ctx context.Context, imageRef, rootfsDir string, auth *oci.Auth) (string, error) {
	if err := oci.ExportImageRootfs(ctx, imageRef, rootfsDir, auth); err != nil {
		return "", err
	}
	return rootfsDir, nil
}

func firstExistingPath(paths ...string) string {
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	for _, p := range paths {
		if strings.TrimSpace(p) != "" {
			return p
		}
	}
	return ""
}
