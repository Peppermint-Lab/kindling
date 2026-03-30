package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	kruntime "github.com/kindlingvm/kindling/internal/runtime"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
	"github.com/kindlingvm/kindling/internal/sshtrust"
)

const (
	HostGroupLinux = "linux-remote-vm"
	HostGroupMac   = "mac-remote-vm"

	DefaultBaseImageRef      = "docker.io/library/alpine:latest"
	DefaultPublishedHTTPPort = 3000
	sandboxSSHHostKeyPrefix  = "KINDLING_SSH_HOST_PUBLIC_KEY="
)

type Service struct {
	Q        *queries.Queries
	Runtime  kruntime.Runtime
	ServerID uuid.UUID
}

func (s *Service) Reconcile(ctx context.Context, sandboxID uuid.UUID) error {
	sb, err := s.Q.RemoteVMFirstByID(ctx, pguuid.ToPgtype(sandboxID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("fetch sandbox: %w", err)
	}
	if sb.DeletedAt.Valid {
		return nil
	}
	if s.Runtime == nil {
		return fmt.Errorf("runtime not configured")
	}

	if sb.DesiredState == "deleted" {
		return s.reconcileDelete(ctx, sb)
	}

	if !sb.ServerID.Valid {
		if err := s.assignSandbox(ctx, sb); err != nil {
			return err
		}
		sb, err = s.Q.RemoteVMFirstByID(ctx, sb.ID)
		if err != nil {
			return fmt.Errorf("re-fetch sandbox after assign: %w", err)
		}
	}

	if sb.ServerID.Valid && uuid.UUID(sb.ServerID.Bytes) != s.ServerID {
		return nil
	}

	switch sb.DesiredState {
	case "stopped":
		return s.reconcileStopped(ctx, sb)
	case "running":
		return s.reconcileRunning(ctx, sb)
	default:
		return nil
	}
}

func (s *Service) ReconcileTemplate(ctx context.Context, templateID uuid.UUID) error {
	tpl, err := s.Q.RemoteVMTemplateFirstByID(ctx, pguuid.ToPgtype(templateID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("fetch sandbox template: %w", err)
	}
	if tpl.DeletedAt.Valid || tpl.Status == "ready" || tpl.Status == "deleted" {
		return nil
	}
	if !tpl.SourceRemoteVmID.Valid {
		_, _ = s.Q.RemoteVMTemplateMarkFailed(ctx, queries.RemoteVMTemplateMarkFailedParams{
			ID:             tpl.ID,
			FailureMessage: "template is missing source sandbox",
		})
		return nil
	}

	sb, err := s.Q.RemoteVMFirstByID(ctx, tpl.SourceRemoteVmID)
	if err != nil {
		return fmt.Errorf("fetch source sandbox: %w", err)
	}
	if !sb.ServerID.Valid || uuid.UUID(sb.ServerID.Bytes) != s.ServerID {
		return nil
	}
	if sb.DesiredState != "stopped" {
		if _, err := s.Q.RemoteVMUpdateDesiredState(ctx, queries.RemoteVMUpdateDesiredStateParams{
			ID:           sb.ID,
			DesiredState: "stopped",
		}); err != nil {
			return fmt.Errorf("request source sandbox stop: %w", err)
		}
		return fmt.Errorf("waiting for source sandbox to stop")
	}
	if sb.ObservedState != "stopped" {
		return fmt.Errorf("source sandbox not yet stopped")
	}

	snapshotRef, err := s.Runtime.CreateTemplate(ctx, uuid.UUID(sb.ID.Bytes))
	if err != nil {
		_, _ = s.Q.RemoteVMTemplateMarkFailed(ctx, queries.RemoteVMTemplateMarkFailedParams{
			ID:             tpl.ID,
			FailureMessage: err.Error(),
		})
		return fmt.Errorf("create runtime template: %w", err)
	}
	if _, err := s.Q.RemoteVMTemplateMarkReady(ctx, queries.RemoteVMTemplateMarkReadyParams{
		ID:          tpl.ID,
		ServerID:    pguuid.ToPgtype(s.ServerID),
		SnapshotRef: snapshotRef,
	}); err != nil {
		return fmt.Errorf("mark template ready: %w", err)
	}
	return nil
}

func (s *Service) assignSandbox(ctx context.Context, sb queries.RemoteVm) error {
	hostGroup := strings.TrimSpace(sb.HostGroup)
	if hostGroup == "" {
		hostGroup = defaultHostGroup()
	}
	backend := strings.TrimSpace(sb.Backend)
	arch := strings.TrimSpace(sb.Arch)
	policy := kruntime.NormalizeRemoteVMIsolationPolicy(sb.IsolationPolicy)

	var tpl *queries.RemoteVmTemplate
	if sb.TemplateID.Valid {
		v, err := s.Q.RemoteVMTemplateFirstByID(ctx, sb.TemplateID)
		if err == nil {
			tpl = &v
			hostGroup = firstNonEmpty(tpl.HostGroup, hostGroup)
			backend = firstNonEmpty(tpl.Backend, backend)
			arch = firstNonEmpty(tpl.Arch, arch)
			if strings.TrimSpace(sb.IsolationPolicy) == "" {
				policy = kruntime.NormalizeRemoteVMIsolationPolicy(tpl.IsolationPolicy)
			}
		}
	}

	backendFilter := backend
	if hostGroup == HostGroupLinux {
		if policy == kruntime.RemoteVMIsolationRequireMicrovm &&
			(backendFilter == "" || backendFilter == kruntime.BackendCrun) {
			backendFilter = kruntime.BackendCloudHypervisor
		}
	} else if hostGroup == HostGroupMac && backendFilter == "" {
		backendFilter = kruntime.BackendAppleVZ
	}

	serverID, resolvedBackend, resolvedArch, err := s.pickServer(ctx, hostGroup, policy, backendFilter, arch, tpl)
	if err != nil {
		return err
	}
	_, err = s.Q.RemoteVMUpdatePlacement(ctx, queries.RemoteVMUpdatePlacementParams{
		ID:        sb.ID,
		HostGroup: hostGroup,
		Backend:   resolvedBackend,
		Arch:      resolvedArch,
		ServerID:  pguuid.ToPgtype(serverID),
	})
	if err != nil {
		return fmt.Errorf("update placement: %w", err)
	}
	return nil
}

type remoteVmPickCandidate struct {
	serverID uuid.UUID
	backend  string
	arch     string
}

// linuxRemoteVmBackendRank lower is preferred when isolation_policy is best_available.
func linuxRemoteVmBackendRank(backend string) int {
	switch strings.TrimSpace(backend) {
	case kruntime.BackendCloudHypervisor:
		return 0
	case kruntime.BackendCrun:
		return 1
	default:
		return 2
	}
}

func (s *Service) pickServer(ctx context.Context, hostGroup, isolationPolicy, backendFilter, arch string, tpl *queries.RemoteVmTemplate) (uuid.UUID, string, string, error) {
	if tpl != nil && tpl.ServerID.Valid {
		return uuid.UUID(tpl.ServerID.Bytes), firstNonEmpty(tpl.Backend, backendFilter), firstNonEmpty(tpl.Arch, arch), nil
	}

	servers, err := s.Q.ServerFindAll(ctx)
	if err != nil {
		return uuid.Nil, "", "", fmt.Errorf("list servers: %w", err)
	}
	statuses, err := s.Q.ServerComponentStatusFindAll(ctx)
	if err != nil {
		return uuid.Nil, "", "", fmt.Errorf("list server component statuses: %w", err)
	}
	metaByServer := make(map[[16]byte]workerMetadata)
	for _, st := range statuses {
		if st.Component != "worker" || !st.ServerID.Valid {
			continue
		}
		metaByServer[st.ServerID.Bytes] = decodeWorkerMetadata(st.Metadata)
	}
	var candidates []remoteVmPickCandidate
	for _, srv := range servers {
		if !srv.ID.Valid || srv.Status != "active" {
			continue
		}
		meta := metaByServer[srv.ID.Bytes]
		if !meta.RemoteVmEnabled {
			continue
		}
		wBackend := strings.TrimSpace(meta.RemoteVmBackend)
		if backendFilter != "" && wBackend != "" && wBackend != backendFilter {
			continue
		}
		wArch := strings.TrimSpace(meta.RemoteVmArch)
		if arch != "" && wArch != "" && wArch != arch {
			continue
		}
		if hostGroup == HostGroupMac && !meta.effectiveMacRemoteVmPlacement() {
			continue
		}
		if hostGroup == HostGroupLinux && !meta.effectiveLinuxRemoteVmPlacement() {
			continue
		}
		candidates = append(candidates, remoteVmPickCandidate{
			serverID: uuid.UUID(srv.ID.Bytes),
			backend:  firstNonEmpty(wBackend, backendFilter),
			arch:     firstNonEmpty(wArch, arch, runtime.GOARCH),
		})
	}
	if len(candidates) == 0 {
		return uuid.Nil, "", "", fmt.Errorf("no active sandbox worker is available for host group %q", hostGroup)
	}
	if hostGroup == HostGroupLinux && isolationPolicy == kruntime.RemoteVMIsolationBestAvailable && backendFilter == "" {
		sort.SliceStable(candidates, func(i, j int) bool {
			return linuxRemoteVmBackendRank(candidates[i].backend) < linuxRemoteVmBackendRank(candidates[j].backend)
		})
	}
	chosen := candidates[0]
	return chosen.serverID, chosen.backend, chosen.arch, nil
}

func (s *Service) reconcileStopped(ctx context.Context, sb queries.RemoteVm) error {
	if !sb.VmID.Valid {
		_, err := s.Q.RemoteVMUpdateObservedState(ctx, queries.RemoteVMUpdateObservedStateParams{
			ID:             sb.ID,
			ObservedState:  "stopped",
			RuntimeUrl:     "",
			FailureMessage: "",
		})
		return err
	}
	vm, err := s.Q.VMFirstByID(ctx, sb.VmID)
	if err != nil {
		return fmt.Errorf("fetch vm: %w", err)
	}
	if vm.Status == "suspended" || sb.ObservedState == "stopped" {
		_, err := s.Q.RemoteVMUpdateObservedState(ctx, queries.RemoteVMUpdateObservedStateParams{
			ID:             sb.ID,
			ObservedState:  "stopped",
			RuntimeUrl:     "",
			FailureMessage: "",
		})
		return err
	}
	if err := s.Runtime.Suspend(ctx, uuid.UUID(sb.ID.Bytes)); err != nil {
		_, _ = s.Q.RemoteVMUpdateObservedState(ctx, queries.RemoteVMUpdateObservedStateParams{
			ID:             sb.ID,
			ObservedState:  "failed",
			RuntimeUrl:     "",
			FailureMessage: err.Error(),
		})
		return fmt.Errorf("suspend sandbox: %w", err)
	}
	if _, err := s.Q.VMUpdateStatus(ctx, queries.VMUpdateStatusParams{
		ID:     sb.VmID,
		Status: "suspended",
	}); err != nil {
		return fmt.Errorf("mark vm suspended: %w", err)
	}
	_, err = s.Q.RemoteVMUpdateObservedState(ctx, queries.RemoteVMUpdateObservedStateParams{
		ID:             sb.ID,
		ObservedState:  "stopped",
		RuntimeUrl:     "",
		FailureMessage: "",
	})
	return err
}

func (s *Service) reconcileRunning(ctx context.Context, sb queries.RemoteVm) error {
	if sb.VmID.Valid {
		vm, err := s.Q.VMFirstByID(ctx, sb.VmID)
		if err == nil {
			if vm.Status == "suspended" || sb.ObservedState == "stopped" {
				return s.resumeSandbox(ctx, sb, vm)
			}
			if vm.Status == "running" && s.Runtime.Healthy(ctx, uuid.UUID(sb.ID.Bytes)) {
				_ = s.syncSandboxSSHAccess(ctx, sb)
				_, err := s.Q.RemoteVMUpdateObservedState(ctx, queries.RemoteVMUpdateObservedStateParams{
					ID:             sb.ID,
					ObservedState:  "running",
					RuntimeUrl:     sb.RuntimeUrl,
					FailureMessage: "",
				})
				return err
			}
			_ = s.Runtime.Stop(ctx, uuid.UUID(sb.ID.Bytes))
			_ = s.Q.VMSoftDelete(ctx, sb.VmID)
			if _, err := s.Q.RemoteVMClearVM(ctx, sb.ID); err != nil {
				return fmt.Errorf("clear stale vm: %w", err)
			}
			sb.VmID = pgtype.UUID{}
		}
	}

	if sb.TemplateID.Valid {
		tpl, err := s.Q.RemoteVMTemplateFirstByID(ctx, sb.TemplateID)
		if err != nil {
			return fmt.Errorf("fetch template: %w", err)
		}
		if tpl.Status != "ready" {
			return fmt.Errorf("template %s is not ready", pguuid.ToString(tpl.ID))
		}
		if tpl.ServerID.Valid && uuid.UUID(tpl.ServerID.Bytes) != s.ServerID {
			return nil
		}
		return s.startClone(ctx, sb, tpl)
	}
	return s.startCold(ctx, sb)
}

func (s *Service) resumeSandbox(ctx context.Context, sb queries.RemoteVm, vm queries.Vm) error {
	runtimeURL, err := s.Runtime.Resume(ctx, uuid.UUID(sb.ID.Bytes))
	if err != nil {
		return fmt.Errorf("resume sandbox: %w", err)
	}
	if err := s.persistRuntimeAddress(ctx, sb.VmID, runtimeURL); err != nil {
		return err
	}
	if _, err := s.Q.VMUpdateLifecycleMetadata(ctx, queries.VMUpdateLifecycleMetadataParams{
		ID:              sb.VmID,
		Status:          "running",
		SnapshotRef:     vm.SnapshotRef,
		SharedRootfsRef: vm.SharedRootfsRef,
		CloneSourceVmID: vm.CloneSourceVmID,
	}); err != nil {
		return fmt.Errorf("mark resumed vm running: %w", err)
	}
	_, err = s.Q.RemoteVMUpdateObservedState(ctx, queries.RemoteVMUpdateObservedStateParams{
		ID:             sb.ID,
		ObservedState:  "running",
		RuntimeUrl:     runtimeURL,
		FailureMessage: "",
	})
	if err != nil {
		return err
	}
	_ = s.syncSandboxSSHAccess(ctx, sb)
	return nil
}

func (s *Service) startCold(ctx context.Context, sb queries.RemoteVm) error {
	inst := kruntime.Instance{
		ID:       uuid.UUID(sb.ID.Bytes),
		ImageRef: firstNonEmpty(strings.TrimSpace(sb.BaseImageRef), DefaultBaseImageRef),
		VCPUs:    int(sb.Vcpu),
		MemoryMB: int(sb.MemoryMb),
		Port:     sandboxPort(sb),
		Env:      sandboxEnv(sb),
	}
	runtimeURL, err := s.Runtime.Start(ctx, inst)
	if err != nil {
		_, _ = s.Q.RemoteVMUpdateObservedState(ctx, queries.RemoteVMUpdateObservedStateParams{
			ID:             sb.ID,
			ObservedState:  "failed",
			RuntimeUrl:     "",
			FailureMessage: err.Error(),
		})
		return fmt.Errorf("start sandbox: %w", err)
	}
	vmID, err := s.persistSandboxVM(ctx, sb, inst.ImageRef, runtimeURL, "running", kruntime.StartMetadata{})
	if err != nil {
		return err
	}
	_, err = s.Q.RemoteVMAttachVM(ctx, queries.RemoteVMAttachVMParams{
		ID:            sb.ID,
		VmID:          pguuid.ToPgtype(vmID),
		ObservedState: "running",
		RuntimeUrl:    runtimeURL,
	})
	if err != nil {
		return err
	}
	_ = s.syncSandboxSSHAccess(ctx, sb)
	return nil
}

func (s *Service) startClone(ctx context.Context, sb queries.RemoteVm, tpl queries.RemoteVmTemplate) error {
	inst := kruntime.Instance{
		ID:       uuid.UUID(sb.ID.Bytes),
		ImageRef: firstNonEmpty(strings.TrimSpace(sb.BaseImageRef), strings.TrimSpace(tpl.BaseImageRef), DefaultBaseImageRef),
		VCPUs:    int(sb.Vcpu),
		MemoryMB: int(sb.MemoryMb),
		Port:     sandboxPort(sb),
		Env:      sandboxEnv(sb),
	}
	runtimeURL, meta, err := s.Runtime.StartClone(ctx, inst, tpl.SnapshotRef, uuid.Nil)
	if err != nil {
		_, _ = s.Q.RemoteVMUpdateObservedState(ctx, queries.RemoteVMUpdateObservedStateParams{
			ID:             sb.ID,
			ObservedState:  "failed",
			RuntimeUrl:     "",
			FailureMessage: err.Error(),
		})
		return fmt.Errorf("start sandbox clone: %w", err)
	}
	vmID, err := s.persistSandboxVM(ctx, sb, inst.ImageRef, runtimeURL, "running", meta)
	if err != nil {
		return err
	}
	_, err = s.Q.RemoteVMAttachVM(ctx, queries.RemoteVMAttachVMParams{
		ID:            sb.ID,
		VmID:          pguuid.ToPgtype(vmID),
		ObservedState: "running",
		RuntimeUrl:    runtimeURL,
	})
	if err != nil {
		return err
	}
	_ = s.syncSandboxSSHAccess(ctx, sb)
	return nil
}

func (s *Service) reconcileDelete(ctx context.Context, sb queries.RemoteVm) error {
	if sb.ServerID.Valid && uuid.UUID(sb.ServerID.Bytes) == s.ServerID {
		_ = s.Runtime.Stop(ctx, uuid.UUID(sb.ID.Bytes))
	}
	if sb.VmID.Valid {
		_ = s.Q.VMSoftDelete(ctx, sb.VmID)
	}
	_, err := s.Q.RemoteVMMarkDeleted(ctx, sb.ID)
	return err
}

func (s *Service) persistSandboxVM(ctx context.Context, sb queries.RemoteVm, imageRef, runtimeURL, status string, meta kruntime.StartMetadata) (uuid.UUID, error) {
	registry, repository, tag := splitImageRef(imageRef)
	img, err := s.Q.ImageFindOrCreate(ctx, queries.ImageFindOrCreateParams{
		ID:         pguuid.ToPgtype(uuid.New()),
		Registry:   registry,
		Repository: repository,
		Tag:        tag,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("ensure image row: %w", err)
	}

	addr, port, err := parseRuntimeAddress(runtimeURL)
	if err != nil {
		return uuid.Nil, err
	}
	envJSON, err := json.Marshal(sandboxEnv(sb))
	if err != nil {
		return uuid.Nil, fmt.Errorf("marshal sandbox env: %w", err)
	}
	vmID := uuid.New()
	if _, err := s.Q.VMCreate(ctx, queries.VMCreateParams{
		ID:              pguuid.ToPgtype(vmID),
		ServerID:        sb.ServerID,
		ImageID:         img.ID,
		Status:          status,
		Runtime:         sb.Backend,
		SnapshotRef:     pgtype.Text{String: meta.SnapshotRef, Valid: strings.TrimSpace(meta.SnapshotRef) != ""},
		SharedRootfsRef: meta.SharedRootfsRef,
		CloneSourceVmID: pguuid.ToPgtype(meta.CloneSourceVMID),
		Vcpus:           sb.Vcpu,
		Memory:          sb.MemoryMb,
		IpAddress:       addr,
		Port:            pgtype.Int4{Int32: int32(port), Valid: true},
		EnvVariables:    pgtype.Text{String: string(envJSON), Valid: true},
	}); err != nil {
		return uuid.Nil, fmt.Errorf("create vm row: %w", err)
	}
	return vmID, nil
}

func (s *Service) persistRuntimeAddress(ctx context.Context, vmID pgtype.UUID, runtimeURL string) error {
	addr, port, err := parseRuntimeAddress(runtimeURL)
	if err != nil {
		return err
	}
	if _, err := s.Q.VMUpdateRuntimeAddress(ctx, queries.VMUpdateRuntimeAddressParams{
		ID:        vmID,
		IpAddress: addr,
		Port:      pgtype.Int4{Int32: int32(port), Valid: true},
	}); err != nil {
		return fmt.Errorf("update vm runtime address: %w", err)
	}
	return nil
}

type workerMetadata struct {
	Runtime          string `json:"runtime"`
	RemoteVmEnabled  bool   `json:"remote_vm_enabled"`
	RemoteVmBackend  string `json:"remote_vm_backend"`
	RemoteVmArch     string `json:"remote_vm_arch"`
	RemoteVmRosetta  bool   `json:"remote_vm_rosetta"`
	RemoteVmCapacity int    `json:"remote_vm_capacity"`
	// LinuxPlacementEligible is nil for heartbeats predating Milestone 2; see effectiveLinuxRemoteVmPlacement.
	LinuxPlacementEligible *bool `json:"remote_vm_linux_placement_eligible,omitempty"`
	MacPlacementEligible   *bool `json:"remote_vm_mac_placement_eligible,omitempty"`
}

func decodeWorkerMetadata(raw []byte) workerMetadata {
	var out workerMetadata
	_ = json.Unmarshal(raw, &out)
	if out.RemoteVmBackend == "" {
		out.RemoteVmBackend = strings.TrimSpace(out.Runtime)
	}
	return out
}

func (m workerMetadata) effectiveLinuxRemoteVmPlacement() bool {
	if m.LinuxPlacementEligible != nil {
		return *m.LinuxPlacementEligible
	}
	return strings.TrimSpace(m.RemoteVmBackend) == kruntime.BackendCloudHypervisor
}

func (m workerMetadata) effectiveMacRemoteVmPlacement() bool {
	if m.MacPlacementEligible != nil {
		return *m.MacPlacementEligible
	}
	return strings.TrimSpace(m.RemoteVmBackend) == kruntime.BackendAppleVZ
}

func sandboxEnv(sb queries.RemoteVm) []string {
	out := []string{}
	var envMap map[string]string
	if len(sb.EnvJson) > 0 {
		_ = json.Unmarshal(sb.EnvJson, &envMap)
	}
	for k, v := range envMap {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		out = append(out, key+"="+v)
	}
	if repo := strings.TrimSpace(sb.GitRepo); repo != "" {
		out = append(out, "KINDLING_REMOTE_VM_GIT_REPO="+repo)
	}
	if ref := strings.TrimSpace(sb.GitRef); ref != "" {
		out = append(out, "KINDLING_REMOTE_VM_GIT_REF="+ref)
	}
	return out
}

func sandboxPort(sb queries.RemoteVm) int {
	if sb.PublishedHttpPort.Valid && sb.PublishedHttpPort.Int32 > 0 {
		return int(sb.PublishedHttpPort.Int32)
	}
	return DefaultPublishedHTTPPort
}

func defaultHostGroup() string {
	if runtime.GOOS == "darwin" {
		return HostGroupMac
	}
	return HostGroupLinux
}

func splitImageRef(ref string) (registry, repository, tag string) {
	s := strings.TrimSpace(strings.TrimPrefix(ref, "docker://"))
	if s == "" {
		s = DefaultBaseImageRef
	}
	tag = "latest"
	if idx := strings.LastIndex(s, ":"); idx > strings.LastIndex(s, "/") {
		tag = s[idx+1:]
		s = s[:idx]
	}
	parts := strings.SplitN(s, "/", 2)
	if len(parts) == 1 {
		return "docker.io", "library/" + parts[0], tag
	}
	first := parts[0]
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return first, parts[1], tag
	}
	return "docker.io", s, tag
}

func parseRuntimeAddress(raw string) (netip.Addr, int, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return netip.Addr{}, 0, fmt.Errorf("empty runtime address")
	}
	hostPort := s
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return netip.Addr{}, 0, fmt.Errorf("parse runtime url: %w", err)
		}
		hostPort = u.Host
	}
	host, portStr, err := net.SplitHostPort(hostPort)
	if err != nil {
		return netip.Addr{}, 0, fmt.Errorf("split host/port %q: %w", hostPort, err)
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, 0, fmt.Errorf("parse host ip %q: %w", host, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return netip.Addr{}, 0, fmt.Errorf("parse port %q: %w", portStr, err)
	}
	return addr, port, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func (s *Service) syncSandboxSSHAccess(ctx context.Context, sb queries.RemoteVm) error {
	access, ok := s.Runtime.(kruntime.GuestAccess)
	if !ok {
		return nil
	}
	rows, err := s.Q.OrgUserSSHKeysActive(ctx, sb.OrgID)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		if key := strings.TrimSpace(row.PublicKey); key != "" {
			keys = append(keys, key)
		}
	}
	content := strings.Join(keys, "\n")
	if content != "" {
		content += "\n"
	}
	const authKeysPath = "/tmp/kindling-authorized_keys"
	if err := access.WriteGuestFile(ctx, uuid.UUID(sb.ID.Bytes), authKeysPath, []byte(content)); err != nil {
		return err
	}
	script := fmt.Sprintf(`
set -eu
remote_vm_id=%q
if ! id -u kindling >/dev/null 2>&1; then
  if command -v useradd >/dev/null 2>&1; then
    useradd -m -s /bin/sh kindling || true
  elif command -v adduser >/dev/null 2>&1; then
    adduser -D -s /bin/sh kindling 2>/dev/null || adduser --disabled-password --gecos '' kindling 2>/dev/null || true
  fi
fi
home_dir="/home/kindling"
if command -v getent >/dev/null 2>&1; then
  maybe_home="$(getent passwd kindling | cut -d: -f6 || true)"
  if [ -n "$maybe_home" ]; then
    home_dir="$maybe_home"
  fi
fi
mkdir -p "$home_dir/.ssh"
cat /tmp/kindling-authorized_keys > "$home_dir/.ssh/authorized_keys"
chown -R kindling:kindling "$home_dir/.ssh" 2>/dev/null || true
chmod 700 "$home_dir/.ssh"
chmod 600 "$home_dir/.ssh/authorized_keys"
sshd_bin=""
if command -v sshd >/dev/null 2>&1; then
  sshd_bin="$(command -v sshd)"
elif [ -x /usr/sbin/sshd ]; then
  sshd_bin="/usr/sbin/sshd"
fi
ssh_keygen_bin=""
if command -v ssh-keygen >/dev/null 2>&1; then
  ssh_keygen_bin="$(command -v ssh-keygen)"
elif [ -x /usr/bin/ssh-keygen ]; then
  ssh_keygen_bin="/usr/bin/ssh-keygen"
fi
host_key_path="/etc/ssh/kindling_host_ed25519_key"
host_key_marker="/etc/ssh/kindling_host_ed25519_key.remote-vm-id"
managed_pidfile="/run/kindling-sshd.pid"
host_pub=""
restart_sshd=0
if [ -n "$sshd_bin" ] && [ -n "$ssh_keygen_bin" ]; then
  mkdir -p /etc/ssh
  current_marker=""
  if [ -f "$host_key_marker" ]; then
    current_marker="$(cat "$host_key_marker" 2>/dev/null || true)"
  fi
  if [ ! -f "$host_key_path" ] || [ ! -f "$host_key_path.pub" ] || [ "$current_marker" != "$remote_vm_id" ]; then
    rm -f "$host_key_path" "$host_key_path.pub"
    "$ssh_keygen_bin" -q -t ed25519 -N '' -f "$host_key_path" >/dev/null
    printf '%%s\n' "$remote_vm_id" > "$host_key_marker"
    restart_sshd=1
  fi
  if [ ! -f "$managed_pidfile" ] || ! kill -0 "$(cat "$managed_pidfile" 2>/dev/null)" 2>/dev/null; then
    restart_sshd=1
  fi
  if [ "$restart_sshd" -eq 1 ]; then
    if command -v pkill >/dev/null 2>&1; then
      pkill -x sshd >/dev/null 2>&1 || true
    fi
    mkdir -p /run/sshd
    "$sshd_bin" -o HostKey="$host_key_path" -o PidFile="$managed_pidfile" >/dev/null 2>&1 || true
  fi
  if [ -f "$host_key_path.pub" ]; then
    host_pub="$(tr -d '\r' < "$host_key_path.pub")"
  fi
fi
if [ -n "$host_pub" ]; then
  printf '%%s%%s\n' %q "$host_pub"
fi
`, uuid.UUID(sb.ID.Bytes).String(), sandboxSSHHostKeyPrefix)
	result, err := access.ExecGuest(ctx, uuid.UUID(sb.ID.Bytes), []string{"/bin/sh", "-lc", script}, "/", nil)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("sync sandbox ssh access: exit code %d: %s", result.ExitCode, strings.TrimSpace(result.Output))
	}
	hostKey, err := sshtrust.ExtractMarkedAuthorizedKey(result.Output, sandboxSSHHostKeyPrefix)
	if err != nil {
		return fmt.Errorf("parse sandbox ssh host key: %w", err)
	}
	if hostKey == sb.SshHostPublicKey {
		return nil
	}
	_, err = s.Q.RemoteVMUpdateSSHHostPublicKey(ctx, queries.RemoteVMUpdateSSHHostPublicKeyParams{
		ID:               sb.ID,
		SshHostPublicKey: hostKey,
	})
	return err
}

func RunExpiryOnce(ctx context.Context, databaseURL string, q *queries.Queries, sched interface{ ScheduleNow(uuid.UUID) }) {
	leaderConn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return
	}
	defer leaderConn.Close(context.Background())

	qLeader := queries.New(leaderConn)
	acquired, err := qLeader.TrySessionAdvisoryLock(ctx, "kindling_remote_vm_expiry")
	if err != nil || !acquired {
		return
	}
	rows, err := q.RemoteVMsDueForExpiry(ctx)
	if err != nil {
		return
	}
	for _, sb := range rows {
		if _, err := q.RemoteVMUpdateDesiredState(ctx, queries.RemoteVMUpdateDesiredStateParams{
			ID:           sb.ID,
			DesiredState: "deleted",
		}); err == nil && sched != nil {
			sched.ScheduleNow(uuid.UUID(sb.ID.Bytes))
		}
	}
}

func RunIdleSuspendOnce(ctx context.Context, databaseURL string, q *queries.Queries, sched interface{ ScheduleNow(uuid.UUID) }) {
	leaderConn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return
	}
	defer leaderConn.Close(context.Background())

	qLeader := queries.New(leaderConn)
	acquired, err := qLeader.TrySessionAdvisoryLock(ctx, "kindling_remote_vm_idle")
	if err != nil || !acquired {
		return
	}
	rows, err := q.RemoteVMsDueForIdleSuspend(ctx)
	if err != nil {
		return
	}
	for _, sb := range rows {
		if _, err := q.RemoteVMUpdateDesiredState(ctx, queries.RemoteVMUpdateDesiredStateParams{
			ID:           sb.ID,
			DesiredState: "stopped",
		}); err == nil && sched != nil {
			sched.ScheduleNow(uuid.UUID(sb.ID.Bytes))
		}
	}
}
