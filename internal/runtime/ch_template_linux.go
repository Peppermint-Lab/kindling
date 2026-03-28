//go:build linux

package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

func (r *CloudHypervisorRuntime) CreateTemplate(ctx context.Context, id uuid.UUID) (string, error) {
	_ = ctx
	r.mu.Lock()
	s, ok := r.suspended[id]
	if !ok {
		r.mu.Unlock()
		return "", fmt.Errorf("cloud-hypervisor template requires suspended source")
	}
	templateDir := lifecyclePath("ch-template", id.String())
	templateDisk := filepath.Join(templateDir, "rootfs.qcow2")
	r.mu.Unlock()
	_ = os.RemoveAll(templateDir)
	if err := ensureDir(templateDir); err != nil {
		return "", err
	}
	if err := copyFile(s.workDisk, templateDisk); err != nil {
		return "", err
	}
	r.mu.Lock()
	r.templates[templateDir] = &cloudHypervisorTemplate{workDisk: templateDisk}
	r.mu.Unlock()
	return templateDir, nil
}

func (r *CloudHypervisorRuntime) StartClone(ctx context.Context, inst Instance, snapshotRef string, cloneSourceVMID uuid.UUID) (string, StartMetadata, error) {
	r.mu.Lock()
	tmpl, ok := resolveCloudHypervisorTemplate(snapshotRef, r.templates)
	r.mu.Unlock()
	if !ok {
		return "", StartMetadata{}, ErrInstanceNotRunning
	}
	workDir := filepath.Join(os.TempDir(), "kindling-ch-"+inst.ID.String())
	_ = os.RemoveAll(workDir)
	if err := ensureDir(workDir); err != nil {
		return "", StartMetadata{}, err
	}
	workDisk := filepath.Join(workDir, "rootfs.qcow2")
	if sharedDisk, ok := r.sharedRootfsPath(inst.ID); ok {
		if err := ensureDir(filepath.Dir(sharedDisk)); err != nil {
			return "", StartMetadata{}, err
		}
		workDisk = sharedDisk
	}
	_ = os.Remove(workDisk)
	if err := copyFile(tmpl.workDisk, workDisk); err != nil {
		return "", StartMetadata{}, err
	}
	ip, err := r.startPreparedVM(ctx, inst, workDir, workDisk, 0)
	if err != nil {
		return "", StartMetadata{}, err
	}
	return ip, StartMetadata{
		SnapshotRef:     snapshotRef,
		SharedRootfsRef: sharedRootfsRefFromWorkDisk(r.sharedRootfsDir, workDisk),
		CloneSourceVMID: cloneSourceVMID,
	}, nil
}
