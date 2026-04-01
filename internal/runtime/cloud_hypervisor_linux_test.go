//go:build linux

package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCloudHypervisorIPsAllocatesPointToPointPairs(t *testing.T) {
	host0, guest0, err := cloudHypervisorIPs(0)
	if err != nil {
		t.Fatal(err)
	}
	if host0 != "10.0.0.0" || guest0 != "10.0.0.1/31" {
		t.Fatalf("slot 0 = (%q, %q), want (10.0.0.0, 10.0.0.1/31)", host0, guest0)
	}

	host1, guest1, err := cloudHypervisorIPs(1)
	if err != nil {
		t.Fatal(err)
	}
	if host1 != "10.0.0.2" || guest1 != "10.0.0.3/31" {
		t.Fatalf("slot 1 = (%q, %q), want (10.0.0.2, 10.0.0.3/31)", host1, guest1)
	}
}

func TestCloudHypervisorTapNameUsesDeploymentID(t *testing.T) {
	id1 := mustUUID("11111111-2222-3333-4444-555555555555")
	id2 := mustUUID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	got1 := cloudHypervisorTapName(id1, 0)
	got2 := cloudHypervisorTapName(id2, 0)
	if got1 == got2 {
		t.Fatalf("tap names should differ across deployments: %q", got1)
	}
	if len(got1) > 15 || len(got2) > 15 {
		t.Fatalf("tap names must fit Linux interface limit: %q %q", got1, got2)
	}
}

func TestCloudHypervisorSupportsWarmLifecycle(t *testing.T) {
	rt := &CloudHypervisorRuntime{}
	if !rt.Supports(CapabilitySuspendResume) {
		t.Fatal("expected cloud-hypervisor to support suspend/resume")
	}
	if !rt.Supports(CapabilityWarmClone) {
		t.Fatal("expected cloud-hypervisor to support warm clone")
	}
	if !rt.Supports(CapabilityLiveMigration) {
		t.Fatal("expected cloud-hypervisor to support live migration")
	}
}

func TestCloudHypervisorDiskArgsAddsPersistentVolumeAsSecondDisk(t *testing.T) {
	args := cloudHypervisorDiskArgs("/tmp/rootfs.qcow2", &PersistentVolumeMount{
		HostPath:  "/data/volumes/vol-1.qcow2",
		MountPath: "/data",
	})

	if len(args) != 4 {
		t.Fatalf("len(args) = %d, want 4", len(args))
	}
	if args[0] != "--disk" || args[2] != "--disk" {
		t.Fatalf("unexpected disk flags: %#v", args)
	}
	if args[1] != "path=/tmp/rootfs.qcow2,direct=off" {
		t.Fatalf("root disk arg = %q", args[1])
	}
	if args[3] != "path=/data/volumes/vol-1.qcow2,direct=off" {
		t.Fatalf("persistent volume arg = %q", args[3])
	}
}

func TestEnsurePersistentVolumeSizeResizesQcow2(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "qemu-img.log")
	writeRuntimeExecutable(t, filepath.Join(tmp, "qemu-img"), "#!/bin/sh\nif [ \"$1\" = \"info\" ]; then\n  printf '{\"virtual-size\":1073741824}'\n  exit 0\nfi\nprintf '%s %s %s\\n' \"$1\" \"$2\" \"$3\" > \""+logPath+"\"\n")
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	volumePath := filepath.Join(tmp, "volume.qcow2")
	if err := os.WriteFile(volumePath, []byte("qcow2"), 0o644); err != nil {
		t.Fatalf("write volume: %v", err)
	}

	if err := ensurePersistentVolumeSize(context.Background(), volumePath, 2); err != nil {
		t.Fatalf("ensurePersistentVolumeSize: %v", err)
	}

	if got := strings.TrimSpace(readRuntimeFile(t, logPath)); got != "resize "+volumePath+" 2G" {
		t.Fatalf("resize args = %q", got)
	}
}

func TestEnsurePersistentVolumeDiskRejectsMissingManagedDisk(t *testing.T) {
	t.Parallel()

	rt := &CloudHypervisorRuntime{}
	vol := &PersistentVolumeMount{
		HostPath:        filepath.Join(t.TempDir(), "missing.qcow2"),
		MountPath:       "/data",
		SizeGB:          10,
		Filesystem:      "ext4",
		CreateIfMissing: false,
	}

	err := rt.ensurePersistentVolumeDisk(context.Background(), vol)
	if err == nil {
		t.Fatal("expected missing volume disk to fail")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing volume error = %v", err)
	}
}

func TestMaterializeCloudHypervisorCloneDiskUsesQemuImgOverlay(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "qemu-img.log")
	writeRuntimeExecutable(t, filepath.Join(tmp, "qemu-img"), "#!/bin/sh\nif [ \"$1\" = \"create\" ]; then\n  printf '%s\n' \"$*\" >> \""+logPath+"\"\n  exit 0\nfi\nexit 1\n")
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	tmpl := filepath.Join(tmp, "template.qcow2")
	work := filepath.Join(tmp, "work.qcow2")
	if err := os.WriteFile(tmpl, []byte("qcow2"), 0o644); err != nil {
		t.Fatalf("template: %v", err)
	}

	strategy, err := materializeCloudHypervisorCloneDisk(context.Background(), tmpl, work)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if strategy != "overlay" {
		t.Fatalf("strategy=%q want overlay", strategy)
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(got), "create") || !strings.Contains(string(got), filepath.Base(tmpl)) {
		t.Fatalf("unexpected qemu-img log: %q", string(got))
	}
}

func TestMaterializeCloudHypervisorCloneDiskFallsBackToFullCopy(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	writeRuntimeExecutable(t, filepath.Join(tmp, "qemu-img"), "#!/bin/sh\nexit 1\n")
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	tmpl := filepath.Join(tmp, "template.qcow2")
	work := filepath.Join(tmp, "work.qcow2")
	if err := os.WriteFile(tmpl, []byte("template-bytes"), 0o644); err != nil {
		t.Fatalf("template: %v", err)
	}

	strategy, err := materializeCloudHypervisorCloneDisk(context.Background(), tmpl, work)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if strategy != "full_copy" {
		t.Fatalf("strategy=%q want full_copy", strategy)
	}
	got, err := os.ReadFile(work)
	if err != nil {
		t.Fatalf("read work: %v", err)
	}
	if string(got) != "template-bytes" {
		t.Fatalf("work content=%q", string(got))
	}
}

func TestSharedRootfsPathHelpers(t *testing.T) {
	id := mustUUID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	got := sharedRootfsPathForID("/shared/rootfs", id)
	want := "/shared/rootfs/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee/rootfs.qcow2"
	if got != want {
		t.Fatalf("sharedRootfsPathForID = %q, want %q", got, want)
	}
	if ref := sharedRootfsRefFromWorkDisk("/shared/rootfs", got); ref != want {
		t.Fatalf("sharedRootfsRefFromWorkDisk = %q, want %q", ref, want)
	}
	if ref := sharedRootfsRefFromWorkDisk("/shared/rootfs", "/tmp/other/rootfs.qcow2"); ref != "" {
		t.Fatalf("sharedRootfsRefFromWorkDisk outside shared dir = %q, want empty", ref)
	}
}

func TestCleanupCloudHypervisorRuntimeArtifactsRemovesSocketAndPIDFiles(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	socketBase := filepath.Join(t.TempDir(), "kindling-vsock.sock")
	paths := []string{
		cloudHypervisorBridgePIDPath(workDir),
		cloudHypervisorVMPIDPath(workDir),
		cloudHypervisorAPISocketPath(workDir),
		socketBase,
		socketBase + "_1024",
	}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("999999"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	cleanupCloudHypervisorRuntimeArtifacts(workDir, socketBase)

	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat err=%v", path, err)
		}
	}
}

func TestPersistAndLoadCloudHypervisorSuspendedState(t *testing.T) {
	t.Parallel()

	rt := &CloudHypervisorRuntime{stateDir: t.TempDir()}
	id := uuid.New()
	workDir := rt.instanceStateDir(id)
	s := &cloudHypervisorSuspended{
		inst: Instance{
			ID:       id,
			ImageRef: "kindling/example:latest",
			VCPUs:    2,
			MemoryMB: 768,
			Port:     3000,
			Env:      []string{"FOO=bar"},
		},
		workDir:  workDir,
		workDisk: cloudHypervisorWorkDiskPath(workDir),
		hostPort: 43210,
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(workDir)
	})

	if err := persistCloudHypervisorSuspendedState(workDir, s); err != nil {
		t.Fatalf("persist suspended state: %v", err)
	}

	got, err := loadCloudHypervisorSuspendedState(workDir)
	if err != nil {
		t.Fatalf("load suspended state: %v", err)
	}
	if got.inst.ID != s.inst.ID || got.inst.ImageRef != s.inst.ImageRef {
		t.Fatalf("loaded instance = %+v, want %+v", got.inst, s.inst)
	}
	if got.hostPort != s.hostPort {
		t.Fatalf("host port = %d, want %d", got.hostPort, s.hostPort)
	}
	if got.workDisk != s.workDisk {
		t.Fatalf("work disk = %q, want %q", got.workDisk, s.workDisk)
	}
}

func TestResolveCloudHypervisorTemplateFallsBackToDisk(t *testing.T) {
	t.Parallel()

	templateDir := t.TempDir()
	templateDisk := filepath.Join(templateDir, "rootfs.qcow2")
	if err := os.WriteFile(templateDisk, []byte("qcow2"), 0o644); err != nil {
		t.Fatalf("write template disk: %v", err)
	}

	templates := make(map[string]*cloudHypervisorTemplate)
	tmpl, ok := resolveCloudHypervisorTemplate(templateDir, templates)
	if !ok {
		t.Fatal("expected template fallback to load from disk")
	}
	if tmpl.workDisk != templateDisk {
		t.Fatalf("workDisk = %q, want %q", tmpl.workDisk, templateDisk)
	}
	if _, ok := templates[templateDir]; !ok {
		t.Fatal("expected template cache to be populated")
	}
}

func TestRecoverRetainedStatePrunesOrphans(t *testing.T) {
	t.Parallel()

	rt := &CloudHypervisorRuntime{
		stateDir:  t.TempDir(),
		templates: make(map[string]*cloudHypervisorTemplate),
	}
	keepInstance := uuid.New()
	pruneInstance := uuid.New()
	keepTemplate := uuid.New()
	pruneTemplate := uuid.New()

	for _, dir := range []string{
		rt.instanceStateDir(keepInstance),
		rt.instanceStateDir(pruneInstance),
		rt.templateStateDir(keepTemplate),
		rt.templateStateDir(pruneTemplate),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	result, err := rt.RecoverRetainedState(context.Background(), []uuid.UUID{keepInstance}, []string{rt.templateStateDir(keepTemplate)})
	if err != nil {
		t.Fatalf("RecoverRetainedState: %v", err)
	}
	if result.InstanceDirsKept != 1 || result.InstanceDirsPruned != 1 {
		t.Fatalf("instance recovery result = %+v", result)
	}
	if result.TemplateDirsKept != 1 || result.TemplateDirsPruned != 1 {
		t.Fatalf("template recovery result = %+v", result)
	}

	if _, err := os.Stat(rt.instanceStateDir(keepInstance)); err != nil {
		t.Fatalf("kept instance dir missing: %v", err)
	}
	if _, err := os.Stat(rt.templateStateDir(keepTemplate)); err != nil {
		t.Fatalf("kept template dir missing: %v", err)
	}
	if _, err := os.Stat(rt.instanceStateDir(pruneInstance)); !os.IsNotExist(err) {
		t.Fatalf("expected pruned instance dir to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(rt.templateStateDir(pruneTemplate)); !os.IsNotExist(err) {
		t.Fatalf("expected pruned template dir to be removed, stat err=%v", err)
	}
}

func TestCloudHypervisorExecGuestAttachesToExistingSocket(t *testing.T) {
	t.Parallel()

	rt := &CloudHypervisorRuntime{
		instances: make(map[uuid.UUID]*cloudHypervisorInstance),
	}
	id := uuid.New()
	socketPath := cloudHypervisorSocketBase(id)
	_ = os.Remove(socketPath)
	t.Cleanup(func() {
		_ = os.Remove(socketPath)
	})

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	defer ln.Close()

	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()

		br := bufio.NewReader(conn)
		line, err := br.ReadString('\n')
		if err != nil {
			done <- err
			return
		}
		if line != fmt.Sprintf("CONNECT %d\n", GuestControlVsockPort) {
			done <- fmt.Errorf("unexpected vsock connect line %q", line)
			return
		}
		if _, err := fmt.Fprint(conn, "OK 0\n"); err != nil {
			done <- err
			return
		}

		req, err := http.ReadRequest(br)
		if err != nil {
			done <- err
			return
		}
		defer req.Body.Close()
		if req.Method != http.MethodPost || req.URL.Path != "/exec" {
			done <- fmt.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
			return
		}

		var body guestExecRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			done <- err
			return
		}
		if len(body.Argv) != 2 || body.Argv[0] != "/bin/sh" || body.Argv[1] != "-lc" {
			done <- fmt.Errorf("unexpected argv %#v", body.Argv)
			return
		}

		resp := guestExecJSON{ExitCode: 0, Output: "attached ok"}
		payload, err := json.Marshal(resp)
		if err != nil {
			done <- err
			return
		}
		if _, err := fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(payload), payload); err != nil {
			done <- err
			return
		}
		done <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := rt.ExecGuest(ctx, id, []string{"/bin/sh", "-lc"}, "/", nil)
	if err != nil {
		t.Fatalf("ExecGuest: %v", err)
	}
	if got.ExitCode != 0 || got.Output != "attached ok" {
		t.Fatalf("exec result = %+v", got)
	}
	if err := <-done; err != nil {
		t.Fatalf("socket server: %v", err)
	}
}

func TestSetupHostBridgeRetriesWhenAllocatedPortDoesNotOpen(t *testing.T) {
	runtimeDir := t.TempDir()
	socketBase := filepath.Join(t.TempDir(), "kindling-vsock.sock")
	ai := &cloudHypervisorInstance{
		runtimeDir: runtimeDir,
		socketBase: socketBase,
		inst:       Instance{ID: uuid.New()},
	}

	origPick := pickFreeTCPPortFunc
	origStart := startCloudHypervisorBridgeHelperFunc
	origWait := waitForTCPPortFunc
	defer func() {
		pickFreeTCPPortFunc = origPick
		startCloudHypervisorBridgeHelperFunc = origStart
		waitForTCPPortFunc = origWait
	}()

	ports := []int{41001, 41002}
	var (
		pickCalls  int
		waited     []string
		startedPIDs []int
	)
	pickFreeTCPPortFunc = func() (int, error) {
		if pickCalls >= len(ports) {
			return 0, fmt.Errorf("unexpected extra port allocation")
		}
		port := ports[pickCalls]
		pickCalls++
		return port, nil
	}
	startCloudHypervisorBridgeHelperFunc = func(hostPort int, vsockUDS string) (*exec.Cmd, error) {
		cmd := exec.Command("sleep", "60")
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		startedPIDs = append(startedPIDs, cmd.Process.Pid)
		return cmd, nil
	}
	waitForTCPPortFunc = func(ctx context.Context, addr string, timeout time.Duration) error {
		waited = append(waited, addr)
		if len(waited) == 1 {
			return fmt.Errorf("timed out waiting for %s", addr)
		}
		return nil
	}
	t.Cleanup(func() {
		for _, pid := range startedPIDs {
			_ = terminatePID(pid)
		}
	})

	rt := &CloudHypervisorRuntime{}
	if err := rt.setupHostBridge(context.Background(), ai, 0); err != nil {
		t.Fatalf("setupHostBridge: %v", err)
	}
	if pickCalls != 2 {
		t.Fatalf("pickFreeTCPPort calls = %d, want 2", pickCalls)
	}
	if len(waited) != 2 {
		t.Fatalf("waitForTCPPort calls = %d, want 2", len(waited))
	}
	if ai.hostPort != ports[1] {
		t.Fatalf("hostPort = %d, want %d", ai.hostPort, ports[1])
	}
	if ai.bridgeCmd == nil || ai.bridgeCmd.Process == nil {
		t.Fatal("expected bridge command to remain attached after retry")
	}
}

func mustUUID(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		panic(err)
	}
	return id
}

func writeRuntimeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readRuntimeFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
