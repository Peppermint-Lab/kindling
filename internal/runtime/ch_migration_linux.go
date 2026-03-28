//go:build linux

package runtime

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/google/uuid"
)

func (r *CloudHypervisorRuntime) MigrationMetadata(ctx context.Context, id uuid.UUID) (MigrationMetadata, error) {
	r.mu.Lock()
	ai, ok := r.instances[id]
	r.mu.Unlock()
	if !ok {
		return MigrationMetadata{}, ErrInstanceNotRunning
	}
	info, err := r.pingVMM(ctx, ai.apiSocket)
	if err != nil {
		return MigrationMetadata{}, err
	}
	return MigrationMetadata{
		SharedRootfsRef: ai.sharedRootfsRef,
		Version:         strings.TrimSpace(info.Version),
	}, nil
}

func (r *CloudHypervisorRuntime) PrepareMigrationTarget(ctx context.Context, id uuid.UUID) (PreparedMigrationTarget, error) {
	if strings.TrimSpace(r.sharedRootfsDir) == "" {
		return PreparedMigrationTarget{}, ErrLiveMigrationUnsupported
	}
	workDir := r.instanceRuntimeDir(id)
	_ = os.RemoveAll(workDir)
	if err := ensureDir(workDir); err != nil {
		return PreparedMigrationTarget{}, err
	}
	apiSocket := cloudHypervisorAPISocketPath(workDir)
	socketBase := filepath.Join(os.TempDir(), "kindling-vsock-"+id.String()+".sock")
	cmd := exec.CommandContext(ctx, r.binaryPath, "--api-socket", apiSocket)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(workDir)
		return PreparedMigrationTarget{}, fmt.Errorf("start destination cloud-hypervisor: %w", err)
	}
	prepared := &cloudHypervisorPreparedMigration{
		id:              id,
		cmd:             cmd,
		workDir:         workDir,
		apiSocket:       apiSocket,
		socketBase:      socketBase,
		sharedRootfsRef: sharedRootfsRefFromWorkDisk(r.sharedRootfsDir, sharedRootfsPathForID(r.sharedRootfsDir, id)),
		stopped:         make(chan struct{}),
	}
	ai := &cloudHypervisorInstance{logMu: sync.Mutex{}}
	go func() {
		r.captureOutputCH(ai, stdout)
	}()
	go func() {
		r.captureOutputCH(ai, stderr)
	}()
	if err := waitForCloudHypervisorAPI(ctx, apiSocket); err != nil {
		_ = terminatePID(cmd.Process.Pid)
		_ = os.RemoveAll(workDir)
		return PreparedMigrationTarget{}, err
	}
	port, err := pickFreeTCPPort()
	if err != nil {
		_ = terminatePID(cmd.Process.Pid)
		_ = os.RemoveAll(workDir)
		return PreparedMigrationTarget{}, err
	}
	receiveAddr := net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
	if err := r.putVMM(ctx, apiSocket, "/vm.receive-migration", map[string]string{"receiver_url": "tcp:" + receiveAddr}); err != nil {
		_ = terminatePID(cmd.Process.Pid)
		_ = os.RemoveAll(workDir)
		return PreparedMigrationTarget{}, err
	}
	prepared.hostPort = 0
	r.mu.Lock()
	r.prepared[id] = prepared
	r.mu.Unlock()
	go r.waitPreparedMigration(prepared)
	return PreparedMigrationTarget{ReceiveAddr: receiveAddr}, nil
}

func (r *CloudHypervisorRuntime) SendMigration(ctx context.Context, id uuid.UUID, req SendMigrationRequest) error {
	r.mu.Lock()
	ai, ok := r.instances[id]
	r.mu.Unlock()
	if !ok {
		return ErrInstanceNotRunning
	}
	payload := map[string]any{
		"destination_url":  req.DestinationURL,
		"downtime_ms":      req.DowntimeMS,
		"timeout_s":        req.TimeoutSeconds,
		"timeout_strategy": "Cancel",
	}
	return r.putVMM(ctx, ai.apiSocket, "/vm.send-migration", payload)
}

func (r *CloudHypervisorRuntime) FinalizeMigrationTarget(ctx context.Context, id uuid.UUID) (string, StartMetadata, error) {
	r.mu.Lock()
	prepared, ok := r.prepared[id]
	if ok {
		delete(r.prepared, id)
	}
	r.mu.Unlock()
	if !ok {
		return "", StartMetadata{}, ErrInstanceNotRunning
	}
	var err error
	prepared.hostPort, err = pickFreeTCPPort()
	if err != nil {
		_ = terminatePID(prepared.cmd.Process.Pid)
		_ = os.RemoveAll(prepared.workDir)
		return "", StartMetadata{}, fmt.Errorf("pick free TCP port: %w", err)
	}
	prepared.ip, err = applyAdvertisedHost(net.JoinHostPort("0.0.0.0", strconv.Itoa(prepared.hostPort)), r.advertiseHost)
	if err != nil {
		_ = terminatePID(prepared.cmd.Process.Pid)
		_ = os.RemoveAll(prepared.workDir)
		return "", StartMetadata{}, fmt.Errorf("apply advertised host: %w", err)
	}
	bridgeCmd, err := startCloudHypervisorBridgeHelper(prepared.hostPort, prepared.socketBase)
	if err != nil {
		_ = terminatePID(prepared.cmd.Process.Pid)
		_ = os.RemoveAll(prepared.workDir)
		return "", StartMetadata{}, fmt.Errorf("start migration bridge: %w", err)
	}
	if err := waitForTCPPort(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(prepared.hostPort)), chTCPBridgeTimeout); err != nil {
		_ = terminatePID(bridgeCmd.Process.Pid)
		_ = terminatePID(prepared.cmd.Process.Pid)
		_ = os.RemoveAll(prepared.workDir)
		return "", StartMetadata{}, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	ai := &cloudHypervisorInstance{
		cmd:             prepared.cmd,
		bridgeCmd:       bridgeCmd,
		ip:              prepared.ip,
		workDir:         prepared.workDir,
		socketBase:      prepared.socketBase,
		apiSocket:       prepared.apiSocket,
		workDisk:        sharedRootfsPathForID(r.sharedRootfsDir, id),
		sharedRootfsRef: prepared.sharedRootfsRef,
		hostPort:        prepared.hostPort,
		cancel:          cancel,
		ready:           make(chan struct{}),
		inst:            Instance{ID: id},
		stopped:         make(chan struct{}),
	}
	r.mu.Lock()
	r.instances[id] = ai
	r.mu.Unlock()
	go r.waitCH(id, ai)
	_ = runCtx
	return prepared.ip, StartMetadata{SharedRootfsRef: prepared.sharedRootfsRef}, nil
}

func (r *CloudHypervisorRuntime) AbortMigrationTarget(ctx context.Context, id uuid.UUID) error {
	r.mu.Lock()
	prepared, ok := r.prepared[id]
	if ok {
		delete(r.prepared, id)
	}
	r.mu.Unlock()
	if !ok {
		return nil
	}
	if prepared.cmd != nil && prepared.cmd.Process != nil {
		_ = terminatePID(prepared.cmd.Process.Pid)
	}
	_ = os.RemoveAll(prepared.workDir)
	return nil
}

func (r *CloudHypervisorRuntime) waitPreparedMigration(prepared *cloudHypervisorPreparedMigration) {
	if prepared == nil || prepared.cmd == nil {
		return
	}
	_ = prepared.cmd.Wait()
	r.mu.Lock()
	current, ok := r.prepared[prepared.id]
	if ok && current == prepared {
		delete(r.prepared, prepared.id)
	}
	r.mu.Unlock()
	_ = os.RemoveAll(prepared.workDir)
	close(prepared.stopped)
}
