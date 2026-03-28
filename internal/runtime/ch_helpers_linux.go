//go:build linux

package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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

func cloudHypervisorSocketBase(id uuid.UUID) string {
	return filepath.Join(os.TempDir(), "kindling-vsock-"+id.String()+".sock")
}

func cloudHypervisorVMPIDPath(workDir string) string {
	return filepath.Join(workDir, "cloud-hypervisor.pid")
}

func cloudHypervisorBridgePIDPath(workDir string) string {
	return filepath.Join(workDir, "cloud-hypervisor-bridge.pid")
}

func cloudHypervisorAPISocketPath(workDir string) string {
	return filepath.Join(workDir, "api.sock")
}

func cloudHypervisorWorkDiskPath(workDir string) string {
	return filepath.Join(workDir, "rootfs.qcow2")
}

func cloudHypervisorSuspendedStatePath(workDir string) string {
	return filepath.Join(workDir, "suspended.json")
}

func cleanupCloudHypervisorRuntimeArtifacts(workDir, socketBase string) {
	_ = terminatePIDFromFile(cloudHypervisorBridgePIDPath(workDir))
	_ = terminatePIDFromFile(cloudHypervisorVMPIDPath(workDir))
	_ = os.Remove(cloudHypervisorBridgePIDPath(workDir))
	_ = os.Remove(cloudHypervisorVMPIDPath(workDir))
	_ = os.Remove(cloudHypervisorAPISocketPath(workDir))
	if strings.TrimSpace(socketBase) != "" {
		_ = os.Remove(socketBase)
		_ = os.Remove(socketBase + "_" + strconv.Itoa(cloudHypervisorVsockPort))
	}
}

type persistedCloudHypervisorSuspendedState struct {
	Instance Instance `json:"instance"`
	HostPort int      `json:"host_port"`
	WorkDisk string   `json:"work_disk"`
}

func persistCloudHypervisorSuspendedState(workDir string, s *cloudHypervisorSuspended) error {
	if s == nil {
		return nil
	}
	if err := ensureDir(workDir); err != nil {
		return err
	}
	payload, err := json.Marshal(persistedCloudHypervisorSuspendedState{
		Instance: s.inst,
		HostPort: s.hostPort,
		WorkDisk: s.workDisk,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(cloudHypervisorSuspendedStatePath(workDir), payload, 0o600)
}

func loadCloudHypervisorSuspendedState(id uuid.UUID) (*cloudHypervisorSuspended, error) {
	workDir := cloudHypervisorWorkDir(id)
	payload, err := os.ReadFile(cloudHypervisorSuspendedStatePath(workDir))
	if err != nil {
		return nil, err
	}
	var persisted persistedCloudHypervisorSuspendedState
	if err := json.Unmarshal(payload, &persisted); err != nil {
		return nil, err
	}
	workDisk := strings.TrimSpace(persisted.WorkDisk)
	if workDisk == "" {
		workDisk = cloudHypervisorWorkDiskPath(workDir)
	}
	return &cloudHypervisorSuspended{
		inst:     persisted.Instance,
		workDir:  workDir,
		workDisk: workDisk,
		hostPort: persisted.HostPort,
	}, nil
}

func resolveCloudHypervisorTemplate(snapshotRef string, templates map[string]*cloudHypervisorTemplate) (*cloudHypervisorTemplate, bool) {
	if tmpl, ok := templates[snapshotRef]; ok {
		return tmpl, true
	}
	templateDisk := filepath.Join(strings.TrimSpace(snapshotRef), "rootfs.qcow2")
	if strings.TrimSpace(snapshotRef) == "" {
		return nil, false
	}
	if _, err := os.Stat(templateDisk); err != nil {
		return nil, false
	}
	tmpl := &cloudHypervisorTemplate{workDisk: templateDisk}
	templates[snapshotRef] = tmpl
	return tmpl, true
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
