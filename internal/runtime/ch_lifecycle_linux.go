//go:build linux

package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/google/uuid"
)

func (r *CloudHypervisorRuntime) Suspend(ctx context.Context, id uuid.UUID) error {
	r.mu.Lock()
	ai, ok := r.instances[id]
	if !ok {
		r.mu.Unlock()
		return nil
	}
	ai.retain = true
	r.suspended[id] = &cloudHypervisorSuspended{
		inst:     ai.inst,
		workDir:  ai.workDir,
		workDisk: ai.workDisk,
		hostPort: ai.hostPort,
	}
	r.mu.Unlock()
	if err := r.Stop(ctx, id); err != nil {
		return fmt.Errorf("stop instance for suspend: %w", err)
	}
	select {
	case <-ai.stopped:
		return nil
	case <-ctx.Done():
		r.mu.Lock()
		delete(r.suspended, id)
		ai.retain = false
		r.mu.Unlock()
		return ctx.Err()
	}
}

func (r *CloudHypervisorRuntime) Resume(ctx context.Context, id uuid.UUID) (string, error) {
	r.mu.Lock()
	s, ok := r.suspended[id]
	if ok {
		delete(r.suspended, id)
	}
	r.mu.Unlock()
	if !ok {
		return "", ErrInstanceNotRunning
	}
	return r.startPreparedVM(ctx, s.inst, s.workDir, s.workDisk, s.hostPort)
}

func (r *CloudHypervisorRuntime) waitCH(id uuid.UUID, ai *cloudHypervisorInstance) {
	err := ai.cmd.Wait()
	if ai.bridgeCmd != nil && ai.bridgeCmd.Process != nil {
		_ = terminatePID(ai.bridgeCmd.Process.Pid)
	}
	_ = os.Remove(cloudHypervisorVMPIDPath(ai.workDir))
	_ = os.Remove(cloudHypervisorBridgePIDPath(ai.workDir))
	removeCHTap(ai.tapName)
	r.cleanupGuestVsock(ai.socketBase)
	r.mu.Lock()
	delete(r.instances, id)
	retain := ai.retain
	r.mu.Unlock()
	if !retain {
		_ = os.RemoveAll(ai.workDir)
	}
	if err != nil {
		slog.Error("cloud-hypervisor VM exited", "id", id, "error", err)
	} else {
		slog.Info("cloud-hypervisor VM exited", "id", id)
	}
	close(ai.stopped)
}

func (r *CloudHypervisorRuntime) Stop(ctx context.Context, id uuid.UUID) error {
	r.mu.Lock()
	ai, ok := r.instances[id]
	if !ok {
		if prepared, prepOK := r.prepared[id]; prepOK {
			delete(r.prepared, id)
			r.mu.Unlock()
			if prepared.cmd != nil && prepared.cmd.Process != nil {
				_ = terminatePID(prepared.cmd.Process.Pid)
			}
			_ = os.RemoveAll(prepared.workDir)
			return nil
		}
		if s, suspended := r.suspended[id]; suspended {
			delete(r.suspended, id)
			r.mu.Unlock()
			if s.workDir != "" {
				_ = os.RemoveAll(s.workDir)
			}
			return nil
		}
	}
	r.mu.Unlock()
	if !ok {
		workDir := cloudHypervisorWorkDir(id)
		_ = terminatePIDFromFile(cloudHypervisorBridgePIDPath(workDir))
		_ = terminatePIDFromFile(cloudHypervisorVMPIDPath(workDir))
		_ = os.Remove(cloudHypervisorBridgePIDPath(workDir))
		_ = os.Remove(cloudHypervisorVMPIDPath(workDir))
		_ = os.RemoveAll(workDir)
		return nil
	}
	if ai.bridgeCmd != nil && ai.bridgeCmd.Process != nil {
		_ = terminatePID(ai.bridgeCmd.Process.Pid)
	}
	ai.cancel()
	return nil
}

func (r *CloudHypervisorRuntime) Healthy(ctx context.Context, id uuid.UUID) bool {
	r.mu.Lock()
	ai, ok := r.instances[id]
	r.mu.Unlock()
	return ok && ai.cmd != nil && ai.cmd.ProcessState == nil
}

func (r *CloudHypervisorRuntime) Logs(ctx context.Context, id uuid.UUID) ([]string, error) {
	r.mu.Lock()
	ai, ok := r.instances[id]
	r.mu.Unlock()
	if !ok {
		return nil, nil
	}
	ai.logMu.Lock()
	defer ai.logMu.Unlock()
	out := make([]string, len(ai.logs))
	copy(out, ai.logs)
	return out, nil
}

// ResourceStats pulls guest-agent /stats over the vsock bridge.
func (r *CloudHypervisorRuntime) ResourceStats(ctx context.Context, id uuid.UUID) (ResourceStats, error) {
	r.mu.Lock()
	ai, ok := r.instances[id]
	r.mu.Unlock()
	if !ok || ai.socketBase == "" {
		return ResourceStats{}, ErrInstanceNotRunning
	}
	conn, err := dialCloudHypervisorGuestOverUDS(ai.socketBase, GuestStatsVsockPort)
	if err != nil {
		return ResourceStats{}, err
	}
	defer conn.Close()
	return resourceStatsFromGuestHTTP(ctx, conn)
}

func (r *CloudHypervisorRuntime) StopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ai := range r.instances {
		ai.cancel()
	}
	for _, prepared := range r.prepared {
		if prepared.cmd != nil && prepared.cmd.Process != nil {
			_ = terminatePID(prepared.cmd.Process.Pid)
		}
	}
}
