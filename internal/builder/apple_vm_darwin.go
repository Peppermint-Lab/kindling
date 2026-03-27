//go:build darwin

package builder

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Code-Hex/vz/v3"
)

const (
	builderGuestNATCIDR = "10.0.0.1/31"
	builderGuestGW      = "10.0.0.0"
	builderHostname     = "kindling-builder"
	builderVsockExec    = uint32(1027)
	builderVMVCPU       = 4
	builderVMMemoryMB   = 8192
	guestReadyTimeout   = 120 * time.Second
)

type appleBuilderVM struct {
	kernelPath    string
	initramfsPath string
	builderRoot   string
	workspaceDir  string
	dummyAppDir   string

	mu     sync.Mutex
	started bool
	cancel context.CancelFunc
	vm     *vz.VirtualMachine
	vsock  *vz.VirtioSocketDevice

	readyOnce sync.Once
	readyCh   chan struct{}
}

func newAppleBuilderVM(kernelPath, initramfsPath, builderRoot, workspaceDir string) (*appleBuilderVM, error) {
	dummy, err := os.MkdirTemp("", "kindling-builder-app-")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dummy, ".keep"), []byte{}, 0o644); err != nil {
		os.RemoveAll(dummy)
		return nil, err
	}
	return &appleBuilderVM{
		kernelPath:    kernelPath,
		initramfsPath: initramfsPath,
		builderRoot:   builderRoot,
		workspaceDir:  workspaceDir,
		dummyAppDir:   dummy,
		readyCh:       make(chan struct{}),
	}, nil
}

func (v *appleBuilderVM) Close() {
	v.mu.Lock()
	cancel := v.cancel
	vm := v.vm
	v.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if vm != nil && vm.CanStop() {
		_ = vm.Stop()
	}
	if v.dummyAppDir != "" {
		_ = os.RemoveAll(v.dummyAppDir)
	}
}

func (v *appleBuilderVM) start(parentCtx context.Context) error {
	v.mu.Lock()
	if v.started {
		v.mu.Unlock()
		return nil
	}
	v.mu.Unlock()

	if _, err := os.Stat(v.kernelPath); err != nil {
		return fmt.Errorf("kernel not found at %s: %w", v.kernelPath, err)
	}
	if _, err := os.Stat(v.initramfsPath); err != nil {
		return fmt.Errorf("initramfs not found at %s: %w", v.initramfsPath, err)
	}

	devNullR, err := os.Open(os.DevNull)
	if err != nil {
		return fmt.Errorf("open /dev/null for reading: %w", err)
	}
	devNullW, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		devNullR.Close()
		return fmt.Errorf("open /dev/null for writing: %w", err)
	}

	bootLoader, err := vz.NewLinuxBootLoader(
		v.kernelPath,
		vz.WithInitrd(v.initramfsPath),
		vz.WithCommandLine("console=hvc0"),
	)
	if err != nil {
		return fmt.Errorf("create boot loader: %w", err)
	}

	vmCfg, err := vz.NewVirtualMachineConfiguration(
		bootLoader,
		builderVMVCPU,
		builderVMMemoryMB*1024*1024,
	)
	if err != nil {
		return fmt.Errorf("create vm config: %w", err)
	}

	natAttachment, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return fmt.Errorf("nat: %w", err)
	}
	netCfg, err := vz.NewVirtioNetworkDeviceConfiguration(natAttachment)
	if err != nil {
		return fmt.Errorf("net: %w", err)
	}
	vmCfg.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{netCfg})

	serialAttachment, err := vz.NewFileHandleSerialPortAttachment(devNullR, devNullW)
	if err != nil {
		return fmt.Errorf("serial: %w", err)
	}
	consoleCfg, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serialAttachment)
	if err != nil {
		return fmt.Errorf("console: %w", err)
	}
	vmCfg.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{consoleCfg})

	vsockCfg, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		return fmt.Errorf("vsock cfg: %w", err)
	}
	vmCfg.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{vsockCfg})

	appShare, err := vz.NewSharedDirectory(v.dummyAppDir, false)
	if err != nil {
		return fmt.Errorf("app share: %w", err)
	}
	appTagged, err := vz.NewSingleDirectoryShare(appShare)
	if err != nil {
		return fmt.Errorf("app single: %w", err)
	}
	fsApp, err := vz.NewVirtioFileSystemDeviceConfiguration("app")
	if err != nil {
		return fmt.Errorf("fs app: %w", err)
	}
	fsApp.SetDirectoryShare(appTagged)

	wsShare, err := vz.NewSharedDirectory(v.workspaceDir, false)
	if err != nil {
		return fmt.Errorf("workspace share: %w", err)
	}
	wsTagged, err := vz.NewSingleDirectoryShare(wsShare)
	if err != nil {
		return fmt.Errorf("workspace single: %w", err)
	}
	fsWS, err := vz.NewVirtioFileSystemDeviceConfiguration("workspace")
	if err != nil {
		return fmt.Errorf("fs workspace: %w", err)
	}
	fsWS.SetDirectoryShare(wsTagged)

	bShare, err := vz.NewSharedDirectory(v.builderRoot, false)
	if err != nil {
		return fmt.Errorf("builder share: %w", err)
	}
	bTagged, err := vz.NewSingleDirectoryShare(bShare)
	if err != nil {
		return fmt.Errorf("builder single: %w", err)
	}
	fsB, err := vz.NewVirtioFileSystemDeviceConfiguration("builder")
	if err != nil {
		return fmt.Errorf("fs builder: %w", err)
	}
	fsB.SetDirectoryShare(bTagged)

	vmCfg.SetDirectorySharingDevicesVirtualMachineConfiguration([]vz.DirectorySharingDeviceConfiguration{
		fsApp, fsWS, fsB,
	})

	entropyCfg, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return fmt.Errorf("entropy: %w", err)
	}
	vmCfg.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropyCfg})

	if _, err := vmCfg.Validate(); err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	vm, err := vz.NewVirtualMachine(vmCfg)
	if err != nil {
		return fmt.Errorf("vm: %w", err)
	}

	runCtx, cancel := context.WithCancel(parentCtx)
	var vsockDev *vz.VirtioSocketDevice
	socketDevices := vm.SocketDevices()
	if len(socketDevices) > 0 {
		vsockDev = socketDevices[0]
		ln, err := vsockDev.Listen(1024)
		if err != nil {
			cancel()
			return fmt.Errorf("vsock listen: %w", err)
		}
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				go v.handleHostVsockConn(conn)
			}
		}()
	}

	if err := vm.Start(); err != nil {
		cancel()
		return fmt.Errorf("start vm: %w", err)
	}

	if vsockDev == nil {
		cancel()
		return fmt.Errorf("vsock device missing")
	}

	if err := waitBuilderGuestReady(runCtx, v.readyCh, guestReadyTimeout); err != nil {
		cancel()
		_ = vm.Stop()
		return fmt.Errorf("guest ready: %w", err)
	}

	v.mu.Lock()
	v.vm = vm
	v.vsock = vsockDev
	v.cancel = cancel
	v.started = true
	v.mu.Unlock()

	go func() {
		<-runCtx.Done()
		if vm.CanStop() {
			_ = vm.Stop()
		}
		slog.Info("builder vm stopped")
	}()

	slog.Info("kindling builder vm started", "workspace", v.workspaceDir)
	return nil
}

func waitBuilderGuestReady(ctx context.Context, ready <-chan struct{}, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("timed out after %s waiting for builder guest", timeout)
	}
}

func (v *appleBuilderVM) handleHostVsockConn(conn net.Conn) {
	defer conn.Close()
	buf := make([]byte, 8192)
	n, err := conn.Read(buf)
	if err != nil || n == 0 {
		return
	}
	req := string(buf[:n])
	if strings.Contains(req, "GET /config") {
		cfg := map[string]any{
			"mode":     "builder",
			"ip_addr":  builderGuestNATCIDR,
			"ip_gw":    builderGuestGW,
			"hostname": builderHostname,
			"port":     3000,
		}
		cfgBytes, _ := json.Marshal(cfg)
		resp := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
			len(cfgBytes), cfgBytes)
		_, _ = conn.Write([]byte(resp))
		slog.Debug("served builder config via vsock")
		return
	}
	if strings.Contains(req, "POST /logs") {
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"))
		return
	}
	if strings.Contains(req, "POST /ready") {
		v.readyOnce.Do(func() { close(v.readyCh) })
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"))
		slog.Info("builder guest reported ready")
		return
	}
	_, _ = conn.Write([]byte("HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n"))
}

func (v *appleBuilderVM) Exec(ctx context.Context, argv []string, extraEnv []string, logLine func(string)) (int, error) {
	v.mu.Lock()
	dev := v.vsock
	v.mu.Unlock()
	if dev == nil {
		return 0, fmt.Errorf("builder vm not running")
	}

	conn, err := dev.Connect(builderVsockExec)
	if err != nil {
		return 0, fmt.Errorf("vsock connect exec port: %w", err)
	}
	defer conn.Close()

	payload, err := json.Marshal(map[string]any{
		"argv": argv,
		"cwd":  "/workspace",
		"env":  extraEnv,
	})
	if err != nil {
		return 0, err
	}
	reqStr := fmt.Sprintf("POST /exec HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		len(payload), string(payload))
	if _, err := io.WriteString(conn, reqStr); err != nil {
		return 0, err
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return 0, fmt.Errorf("read HTTP response: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("exec HTTP %d: %s", resp.StatusCode, string(body))
	}

	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	code := -1
	for _, line := range lines {
		if strings.HasPrefix(line, "KINDLING_EXIT_CODE ") {
			c, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "KINDLING_EXIT_CODE ")))
			if err == nil {
				code = c
			}
			continue
		}
		if logLine != nil && line != "" {
			logLine(line)
		}
	}
	if code < 0 {
		return 0, fmt.Errorf("missing exit code in guest response")
	}
	return code, nil
}
