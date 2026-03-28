//go:build linux

package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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

func (r *CloudHypervisorRuntime) StateDir() string {
	return strings.TrimSpace(r.stateDir)
}

func (r *CloudHypervisorRuntime) DurableFastWakeEnabled() bool {
	return strings.TrimSpace(r.stateDir) != ""
}

func (r *CloudHypervisorRuntime) instanceStateRoot() string {
	return filepath.Join(r.stateDir, "instances")
}

func (r *CloudHypervisorRuntime) templateStateRoot() string {
	return filepath.Join(r.stateDir, "templates")
}

func (r *CloudHypervisorRuntime) instanceStateDir(id uuid.UUID) string {
	return filepath.Join(r.instanceStateRoot(), id.String())
}

func (r *CloudHypervisorRuntime) templateStateDir(id uuid.UUID) string {
	return filepath.Join(r.templateStateRoot(), id.String())
}

func (r *CloudHypervisorRuntime) deleteRetainedState(id uuid.UUID) {
	if id == uuid.Nil {
		return
	}
	_ = os.RemoveAll(r.instanceStateDir(id))
	_ = os.RemoveAll(r.templateStateDir(id))
	r.mu.Lock()
	delete(r.templates, r.templateStateDir(id))
	r.mu.Unlock()
}

func (r *CloudHypervisorRuntime) instanceRuntimeDir(id uuid.UUID) string {
	return filepath.Join(os.TempDir(), "kindling-ch-runtime-"+id.String())
}

func cloudHypervisorSocketBase(id uuid.UUID) string {
	return filepath.Join(os.TempDir(), "kindling-vsock-"+id.String()+".sock")
}

func cloudHypervisorVMPIDPath(runtimeDir string) string {
	return filepath.Join(runtimeDir, "cloud-hypervisor.pid")
}

func cloudHypervisorBridgePIDPath(runtimeDir string) string {
	return filepath.Join(runtimeDir, "cloud-hypervisor-bridge.pid")
}

func cloudHypervisorAPISocketPath(runtimeDir string) string {
	return filepath.Join(runtimeDir, "api.sock")
}

func cloudHypervisorWorkDiskPath(workDir string) string {
	return filepath.Join(workDir, "rootfs.qcow2")
}

func cloudHypervisorTemplateDiskPath(templateDir string) string {
	return filepath.Join(templateDir, "rootfs.qcow2")
}

func cloudHypervisorSuspendedStatePath(workDir string) string {
	return filepath.Join(workDir, "suspended.json")
}

func cleanupCloudHypervisorRuntimeArtifacts(runtimeDir, socketBase string) {
	_ = terminatePIDFromFile(cloudHypervisorBridgePIDPath(runtimeDir))
	_ = terminatePIDFromFile(cloudHypervisorVMPIDPath(runtimeDir))
	_ = os.Remove(cloudHypervisorBridgePIDPath(runtimeDir))
	_ = os.Remove(cloudHypervisorVMPIDPath(runtimeDir))
	_ = os.Remove(cloudHypervisorAPISocketPath(runtimeDir))
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

func loadCloudHypervisorSuspendedState(workDir string) (*cloudHypervisorSuspended, error) {
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
	if strings.TrimSpace(snapshotRef) == "" {
		return nil, false
	}
	templateDisk := cloudHypervisorTemplateDiskPath(strings.TrimSpace(snapshotRef))
	if _, err := os.Stat(templateDisk); err != nil {
		return nil, false
	}
	tmpl := &cloudHypervisorTemplate{workDisk: templateDisk}
	templates[snapshotRef] = tmpl
	return tmpl, true
}

func (r *CloudHypervisorRuntime) RecoverRetainedState(ctx context.Context, keepInstanceIDs []uuid.UUID, keepTemplateRefs []string) (RetainedStateRecovery, error) {
	result := RetainedStateRecovery{StateDir: r.StateDir()}
	if strings.TrimSpace(r.stateDir) == "" {
		return result, nil
	}
	if err := ensureDir(r.instanceStateRoot()); err != nil {
		return result, err
	}
	if err := ensureDir(r.templateStateRoot()); err != nil {
		return result, err
	}

	keepInstances := make(map[string]struct{}, len(keepInstanceIDs))
	for _, id := range keepInstanceIDs {
		if id == uuid.Nil {
			continue
		}
		keepInstances[id.String()] = struct{}{}
	}
	keepTemplates := make(map[string]struct{}, len(keepTemplateRefs))
	for _, ref := range keepTemplateRefs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		keepTemplates[filepath.Clean(ref)] = struct{}{}
	}

	kept, pruned, err := pruneRetainedStateDirs(ctx, r.instanceStateRoot(), keepInstances)
	if err != nil {
		return result, err
	}
	result.InstanceDirsKept = kept
	result.InstanceDirsPruned = pruned

	templateKept, templatePruned, err := pruneTemplateStateDirs(ctx, r.templateStateRoot(), keepTemplates)
	if err != nil {
		return result, err
	}
	result.TemplateDirsKept = templateKept
	result.TemplateDirsPruned = templatePruned

	return result, nil
}

func pruneRetainedStateDirs(ctx context.Context, root string, keep map[string]struct{}) (kept, pruned int, err error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return kept, pruned, ctx.Err()
		}
		if !entry.IsDir() {
			continue
		}
		if _, ok := keep[entry.Name()]; ok {
			kept++
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return kept, pruned, err
		}
		pruned++
	}
	return kept, pruned, nil
}

func pruneTemplateStateDirs(ctx context.Context, root string, keep map[string]struct{}) (kept, pruned int, err error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return kept, pruned, ctx.Err()
		}
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if _, ok := keep[filepath.Clean(dir)]; ok {
			kept++
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return kept, pruned, err
		}
		pruned++
	}
	return kept, pruned, nil
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
