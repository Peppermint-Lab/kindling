//go:build linux

package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
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
		r.mu.Lock()
		s := r.suspended[id]
		r.mu.Unlock()
		if err := persistCloudHypervisorSuspendedState(ai.workDir, s); err != nil {
			return fmt.Errorf("persist suspended state: %w", err)
		}
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
		loaded, err := loadCloudHypervisorSuspendedState(r.instanceStateDir(id))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", ErrInstanceNotRunning
			}
			return "", err
		}
		s = loaded
	}
	ip, err := r.startPreparedVM(ctx, s.inst, s.workDir, s.workDisk, s.hostPort)
	if err != nil {
		return "", err
	}
	_ = os.Remove(cloudHypervisorSuspendedStatePath(s.workDir))
	return ip, nil
}

func (r *CloudHypervisorRuntime) waitCH(id uuid.UUID, ai *cloudHypervisorInstance) {
	err := ai.cmd.Wait()
	if ai.bridgeCmd != nil && ai.bridgeCmd.Process != nil {
		_ = terminatePID(ai.bridgeCmd.Process.Pid)
	}
	cleanupCloudHypervisorRuntimeArtifacts(ai.runtimeDir, ai.socketBase)
	_ = os.RemoveAll(ai.runtimeDir)
	removeCHTap(ai.tapName)
	r.mu.Lock()
	delete(r.instances, id)
	retain := ai.retain
	if !retain {
		delete(r.templates, r.templateStateDir(id))
	}
	r.mu.Unlock()
	if !retain {
		_ = os.RemoveAll(ai.workDir)
		_ = os.RemoveAll(r.templateStateDir(id))
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
			cleanupCloudHypervisorRuntimeArtifacts(prepared.workDir, prepared.socketBase)
			_ = os.RemoveAll(prepared.workDir)
			return nil
		}
		if s, suspended := r.suspended[id]; suspended {
			delete(r.suspended, id)
			r.mu.Unlock()
			if s.workDir != "" {
				_ = os.RemoveAll(s.workDir)
			}
			_ = os.RemoveAll(r.instanceRuntimeDir(id))
			r.mu.Lock()
			delete(r.templates, r.templateStateDir(id))
			r.mu.Unlock()
			_ = os.RemoveAll(r.templateStateDir(id))
			return nil
		}
	}
	r.mu.Unlock()
	if !ok {
		workDir := r.instanceStateDir(id)
		runtimeDir := r.instanceRuntimeDir(id)
		cleanupCloudHypervisorRuntimeArtifacts(runtimeDir, cloudHypervisorSocketBase(id))
		_ = os.RemoveAll(runtimeDir)
		_ = os.RemoveAll(workDir)
		r.mu.Lock()
		delete(r.templates, r.templateStateDir(id))
		r.mu.Unlock()
		_ = os.RemoveAll(r.templateStateDir(id))
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

func (r *CloudHypervisorRuntime) guestAccessInstance(id uuid.UUID) (*cloudHypervisorInstance, bool) {
	r.mu.Lock()
	ai, ok := r.instances[id]
	r.mu.Unlock()
	if ok && ai.socketBase != "" {
		return ai, true
	}
	socketBase := cloudHypervisorSocketBase(id)
	if _, err := os.Stat(socketBase); err != nil {
		return nil, false
	}
	runtimeDir := r.instanceRuntimeDir(id)
	ready := make(chan struct{})
	close(ready)
	return &cloudHypervisorInstance{
		workDir:    r.instanceStateDir(id),
		runtimeDir: runtimeDir,
		socketBase: socketBase,
		apiSocket:  cloudHypervisorAPISocketPath(runtimeDir),
		ready:      ready,
		stopped:    make(chan struct{}),
	}, true
}

func (r *CloudHypervisorRuntime) Logs(ctx context.Context, id uuid.UUID) ([]string, error) {
	ai, ok := r.guestAccessInstance(id)
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
	ai, ok := r.guestAccessInstance(id)
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

func (r *CloudHypervisorRuntime) ExecGuest(ctx context.Context, id uuid.UUID, argv []string, cwd string, env []string) (GuestExecResult, error) {
	ai, ok := r.guestAccessInstance(id)
	if !ok || ai.socketBase == "" {
		return GuestExecResult{}, ErrInstanceNotRunning
	}
	conn, err := dialCloudHypervisorGuestOverUDS(ai.socketBase, GuestControlVsockPort)
	if err != nil {
		return GuestExecResult{}, err
	}
	defer conn.Close()
	return execGuestHTTP(ctx, conn, argv, cwd, env)
}

func (r *CloudHypervisorRuntime) StreamGuest(ctx context.Context, id uuid.UUID, argv []string, cwd string, env []string) (io.ReadWriteCloser, error) {
	ai, ok := r.guestAccessInstance(id)
	if !ok || ai.socketBase == "" {
		return nil, ErrInstanceNotRunning
	}
	conn, err := dialCloudHypervisorGuestOverUDS(ai.socketBase, GuestControlVsockPort)
	if err != nil {
		return nil, err
	}
	stream, err := streamGuestHTTP(ctx, conn, argv, cwd, env)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return stream, nil
}

func (r *CloudHypervisorRuntime) ConnectGuestTCP(ctx context.Context, id uuid.UUID, port int) (io.ReadWriteCloser, error) {
	ai, ok := r.guestAccessInstance(id)
	if !ok || ai.socketBase == "" {
		return nil, ErrInstanceNotRunning
	}
	conn, err := dialCloudHypervisorGuestOverUDS(ai.socketBase, GuestControlVsockPort)
	if err != nil {
		return nil, err
	}
	stream, err := tcpGuestHTTP(ctx, conn, port)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return stream, nil
}

func (r *CloudHypervisorRuntime) ReadGuestFile(ctx context.Context, id uuid.UUID, filePath string) ([]byte, error) {
	ai, ok := r.guestAccessInstance(id)
	if !ok || ai.socketBase == "" {
		return nil, ErrInstanceNotRunning
	}
	conn, err := dialCloudHypervisorGuestOverUDS(ai.socketBase, GuestControlVsockPort)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return readGuestFileHTTP(ctx, conn, filePath)
}

func (r *CloudHypervisorRuntime) WriteGuestFile(ctx context.Context, id uuid.UUID, filePath string, data []byte) error {
	ai, ok := r.guestAccessInstance(id)
	if !ok || ai.socketBase == "" {
		return ErrInstanceNotRunning
	}
	conn, err := dialCloudHypervisorGuestOverUDS(ai.socketBase, GuestControlVsockPort)
	if err != nil {
		return err
	}
	defer conn.Close()
	return writeGuestFileHTTP(ctx, conn, filePath, data)
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
