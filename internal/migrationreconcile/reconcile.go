package migrationreconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/reconciler"
	"github.com/kindlingvm/kindling/internal/runtime"
	"github.com/kindlingvm/kindling/internal/shared/conv"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

type Handler struct {
	q           *queries.Queries
	pool        *pgxpool.Pool
	rt          runtime.Runtime
	serverID    uuid.UUID
	deployments *reconciler.Scheduler
	notifyRoute func()
}

func NewHandler(q *queries.Queries, pool *pgxpool.Pool, rt runtime.Runtime, serverID uuid.UUID, deployments *reconciler.Scheduler, notifyRoute func()) *Handler {
	return &Handler{q: q, pool: pool, rt: rt, serverID: serverID, deployments: deployments, notifyRoute: notifyRoute}
}

func (h *Handler) Reconcile(ctx context.Context, migrationID uuid.UUID) error {
	if h == nil || h.q == nil {
		return nil
	}
	mig, err := h.q.InstanceMigrationFirstByID(ctx, pguuid.ToPgtype(migrationID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("fetch migration: %w", err)
	}
	instanceID := uuid.UUID(mig.DeploymentInstanceID.Bytes)
	switch mig.State {
	case "pending":
		if uuid.UUID(mig.DestinationServerID.Bytes) == h.serverID {
			return h.prepareDestination(ctx, mig, instanceID)
		}
	case "destination_prepared":
		if uuid.UUID(mig.SourceServerID.Bytes) == h.serverID {
			return h.sendFromSource(ctx, mig, instanceID)
		}
	case "received":
		if uuid.UUID(mig.DestinationServerID.Bytes) == h.serverID {
			return h.commitOnDestination(ctx, mig, instanceID)
		}
	case "failed", "aborted", "completed", "fallback_evacuating":
		if uuid.UUID(mig.DestinationServerID.Bytes) == h.serverID && h.rt != nil && h.rt.Supports(runtime.CapabilityLiveMigration) {
			_ = h.rt.AbortMigrationTarget(ctx, instanceID)
		}
		if mig.State == "fallback_evacuating" && h.deployments != nil {
			inst, err := h.q.DeploymentInstanceFirstByID(ctx, mig.DeploymentInstanceID)
			if err == nil && inst.DeploymentID.Valid {
				h.deployments.ScheduleNow(uuid.UUID(inst.DeploymentID.Bytes))
			}
		}
	}
	return nil
}

func (h *Handler) prepareDestination(ctx context.Context, mig queries.InstanceMigration, instanceID uuid.UUID) error {
	if h.rt == nil || !h.rt.Supports(runtime.CapabilityLiveMigration) {
		_, _ = h.q.InstanceMigrationUpdateFailed(ctx, queries.InstanceMigrationUpdateFailedParams{
			ID:             mig.ID,
			FailureCode:    "unsupported_runtime",
			FailureMessage: "live migration is not supported on this destination runtime",
		})
		return nil
	}
	server, err := h.q.ServerFindByID(ctx, mig.DestinationServerID)
	if err != nil {
		return fmt.Errorf("fetch destination server: %w", err)
	}
	if server.Status != "active" {
		_, _ = h.q.InstanceMigrationUpdateFailed(ctx, queries.InstanceMigrationUpdateFailedParams{
			ID:             mig.ID,
			FailureCode:    "destination_inactive",
			FailureMessage: "destination server is not active",
		})
		return nil
	}
	if ip := strings.TrimSpace(server.InternalIp); ip == "" || ip == "127.0.0.1" || ip == "0.0.0.0" {
		_, _ = h.q.InstanceMigrationUpdateFailed(ctx, queries.InstanceMigrationUpdateFailedParams{
			ID:             mig.ID,
			FailureCode:    "destination_internal_ip",
			FailureMessage: "destination server has no usable internal IP for live migration",
		})
		return nil
	}
	prepared, err := h.rt.PrepareMigrationTarget(ctx, instanceID)
	if err != nil {
		_, _ = h.q.InstanceMigrationUpdateFailed(ctx, queries.InstanceMigrationUpdateFailedParams{
			ID:             mig.ID,
			FailureCode:    "destination_prepare",
			FailureMessage: err.Error(),
		})
		return nil
	}
	_, portStr, err := net.SplitHostPort(prepared.ReceiveAddr)
	if err != nil {
		_ = h.rt.AbortMigrationTarget(ctx, instanceID)
		_, _ = h.q.InstanceMigrationUpdateFailed(ctx, queries.InstanceMigrationUpdateFailedParams{
			ID:             mig.ID,
			FailureCode:    "destination_prepare",
			FailureMessage: fmt.Sprintf("invalid prepared receive address %q", prepared.ReceiveAddr),
		})
		return nil
	}
	receiveAddr := net.JoinHostPort(strings.TrimSpace(server.InternalIp), portStr)
	_, err = h.q.InstanceMigrationUpdatePrepared(ctx, queries.InstanceMigrationUpdatePreparedParams{
		ID:                mig.ID,
		ReceiveAddr:       receiveAddr,
		CutoverDeadlineAt: pgtype.Timestamptz{Time: time.Now().Add(10 * time.Minute), Valid: true},
	})
	if err != nil {
		return fmt.Errorf("update migration prepared state: %w", err)
	}
	return nil
}

func (h *Handler) sendFromSource(ctx context.Context, mig queries.InstanceMigration, instanceID uuid.UUID) error {
	if h.rt == nil || !h.rt.Supports(runtime.CapabilityLiveMigration) {
		return nil
	}
	inst, err := h.q.DeploymentInstanceFirstByID(ctx, mig.DeploymentInstanceID)
	if err != nil {
		return fmt.Errorf("fetch deployment instance: %w", err)
	}
	dep, err := h.q.DeploymentFirstByID(ctx, inst.DeploymentID)
	if err != nil {
		return fmt.Errorf("fetch deployment: %w", err)
	}
	if _, err := h.q.ProjectVolumeFindByProjectID(ctx, dep.ProjectID); err == nil {
		_, _ = h.q.InstanceMigrationUpdateFailed(ctx, queries.InstanceMigrationUpdateFailedParams{
			ID:             mig.ID,
			FailureCode:    "unsupported_volume",
			FailureMessage: "live migration does not support workloads with persistent volumes yet",
		})
		return nil
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check project volume: %w", err)
	}
	vm, err := h.q.VMFirstByID(ctx, mig.SourceVmID)
	if err != nil {
		return fmt.Errorf("fetch source VM: %w", err)
	}
	if strings.TrimSpace(vm.SharedRootfsRef) == "" {
		_, _ = h.q.InstanceMigrationUpdateFailed(ctx, queries.InstanceMigrationUpdateFailedParams{
			ID:             mig.ID,
			FailureCode:    "shared_rootfs_missing",
			FailureMessage: "source VM is missing a shared rootfs reference",
		})
		return nil
	}
	sourceMeta, err := h.rt.MigrationMetadata(ctx, instanceID)
	if err != nil {
		_, _ = h.q.InstanceMigrationUpdateFailed(ctx, queries.InstanceMigrationUpdateFailedParams{
			ID:             mig.ID,
			FailureCode:    "source_metadata",
			FailureMessage: err.Error(),
		})
		return nil
	}
	destinationStatus, err := h.q.ServerComponentStatusFindByServerID(ctx, mig.DestinationServerID)
	if err != nil {
		return fmt.Errorf("fetch destination server status: %w", err)
	}
	destinationMeta, err := liveMigrationWorkerMetadataFromStatuses(destinationStatus)
	if err != nil {
		_, _ = h.q.InstanceMigrationUpdateFailed(ctx, queries.InstanceMigrationUpdateFailedParams{
			ID:             mig.ID,
			FailureCode:    "destination_runtime",
			FailureMessage: err.Error(),
		})
		return nil
	}
	if destinationMeta.Runtime != "cloud-hypervisor" {
		_, _ = h.q.InstanceMigrationUpdateFailed(ctx, queries.InstanceMigrationUpdateFailedParams{
			ID:             mig.ID,
			FailureCode:    "destination_runtime",
			FailureMessage: "destination server worker runtime is not cloud-hypervisor",
		})
		return nil
	}
	if !destinationMeta.LiveMigrationEnabled {
		_, _ = h.q.InstanceMigrationUpdateFailed(ctx, queries.InstanceMigrationUpdateFailedParams{
			ID:             mig.ID,
			FailureCode:    "destination_runtime",
			FailureMessage: "destination server worker does not advertise live migration support",
		})
		return nil
	}
	if destinationMeta.SharedRootfsDir == "" {
		_, _ = h.q.InstanceMigrationUpdateFailed(ctx, queries.InstanceMigrationUpdateFailedParams{
			ID:             mig.ID,
			FailureCode:    "destination_runtime",
			FailureMessage: "destination server worker does not advertise shared rootfs storage",
		})
		return nil
	}
	if sourceMeta.Version != "" && destinationMeta.CloudHypervisorVersion != "" && sourceMeta.Version != destinationMeta.CloudHypervisorVersion {
		_, _ = h.q.InstanceMigrationUpdateFailed(ctx, queries.InstanceMigrationUpdateFailedParams{
			ID:             mig.ID,
			FailureCode:    "version_mismatch",
			FailureMessage: fmt.Sprintf("destination cloud-hypervisor version %q does not match source %q", destinationMeta.CloudHypervisorVersion, sourceMeta.Version),
		})
		return nil
	}
	if _, err := h.q.InstanceMigrationUpdateSending(ctx, mig.ID); err != nil {
		return fmt.Errorf("update migration to sending: %w", err)
	}
	err = h.rt.SendMigration(ctx, instanceID, runtime.SendMigrationRequest{
		DestinationURL: "tcp:" + strings.TrimSpace(mig.ReceiveAddr),
		DowntimeMS:     300,
		TimeoutSeconds: 3600,
	})
	if err != nil {
		_, _ = h.q.InstanceMigrationUpdateFailed(ctx, queries.InstanceMigrationUpdateFailedParams{
			ID:             mig.ID,
			FailureCode:    "send_failed",
			FailureMessage: err.Error(),
		})
		return nil
	}
	if _, err := h.q.InstanceMigrationUpdateReceived(ctx, mig.ID); err != nil {
		return fmt.Errorf("update migration to received: %w", err)
	}
	return nil
}

func (h *Handler) commitOnDestination(ctx context.Context, mig queries.InstanceMigration, instanceID uuid.UUID) error {
	runtimeURL, meta, err := h.rt.FinalizeMigrationTarget(ctx, instanceID)
	if err != nil {
		_, _ = h.q.InstanceMigrationUpdateFallbackEvacuating(ctx, queries.InstanceMigrationUpdateFallbackEvacuatingParams{
			ID:             mig.ID,
			FailureCode:    "destination_finalize",
			FailureMessage: err.Error(),
		})
		return nil
	}
	ip, port, err := parseRuntimeAddress(runtimeURL)
	if err != nil {
		_, _ = h.q.InstanceMigrationUpdateFallbackEvacuating(ctx, queries.InstanceMigrationUpdateFallbackEvacuatingParams{
			ID:             mig.ID,
			FailureCode:    "destination_runtime_url",
			FailureMessage: err.Error(),
		})
		return nil
	}
	inst, err := h.q.DeploymentInstanceFirstByID(ctx, mig.DeploymentInstanceID)
	if err != nil {
		return fmt.Errorf("fetch deployment instance: %w", err)
	}
	sourceVM, err := h.q.VMFirstByID(ctx, mig.SourceVmID)
	if err != nil {
		return fmt.Errorf("fetch source VM: %w", err)
	}
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := h.q.WithTx(tx)
	newVMID := uuid.New()
	newVM, err := qtx.VMCreate(ctx, queries.VMCreateParams{
		ID:              pguuid.ToPgtype(newVMID),
		ServerID:        mig.DestinationServerID,
		ImageID:         sourceVM.ImageID,
		Status:          "running",
		Runtime:         sourceVM.Runtime,
		SnapshotRef:     sourceVM.SnapshotRef,
		SharedRootfsRef: chooseSharedRootfsRef(meta.SharedRootfsRef, sourceVM.SharedRootfsRef),
		CloneSourceVmID: pgtype.UUID{},
		Vcpus:           sourceVM.Vcpus,
		Memory:          sourceVM.Memory,
		IpAddress:       ip,
		Port:            pgtype.Int4{Int32: int32(port), Valid: true},
		EnvVariables:    sourceVM.EnvVariables,
	})
	if err != nil {
		return fmt.Errorf("create destination VM: %w", err)
	}
	if _, err := qtx.DeploymentInstanceUpdateServer(ctx, queries.DeploymentInstanceUpdateServerParams{
		ID:       mig.DeploymentInstanceID,
		ServerID: mig.DestinationServerID,
	}); err != nil {
		return fmt.Errorf("update instance server: %w", err)
	}
	if _, err := qtx.DeploymentInstanceAttachVM(ctx, queries.DeploymentInstanceAttachVMParams{
		ID:     mig.DeploymentInstanceID,
		VmID:   newVM.ID,
		Status: inst.Status,
	}); err != nil {
		return fmt.Errorf("attach VM to instance: %w", err)
	}
	if err := qtx.VMSoftDelete(ctx, mig.SourceVmID); err != nil {
		return fmt.Errorf("soft-delete source VM: %w", err)
	}
	if _, err := qtx.InstanceMigrationUpdateCompleted(ctx, queries.InstanceMigrationUpdateCompletedParams{
		ID:                    mig.ID,
		DestinationRuntimeUrl: runtimeURL,
	}); err != nil {
		return fmt.Errorf("update migration completed: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration transaction: %w", err)
	}
	if h.notifyRoute != nil {
		h.notifyRoute()
	}
	return nil
}

func chooseSharedRootfsRef(primary, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return primary
	}
	return fallback
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
			return netip.Addr{}, 0, fmt.Errorf("parse runtime URL: %w", err)
		}
		hostPort = u.Host
	}
	host, portStr, err := net.SplitHostPort(hostPort)
	if err != nil {
		return netip.Addr{}, 0, fmt.Errorf("split host port %q: %w", hostPort, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, 0, fmt.Errorf("invalid host ip %q", host)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return netip.Addr{}, 0, fmt.Errorf("parse port %q: %w", portStr, err)
	}
	return ip, port, nil
}

type liveMigrationWorkerMetadata struct {
	Runtime                string
	LiveMigrationEnabled   bool
	CloudHypervisorVersion string
	SharedRootfsDir        string
}

func liveMigrationWorkerMetadataFromStatuses(statuses []queries.ServerComponentStatus) (liveMigrationWorkerMetadata, error) {
	for _, status := range statuses {
		if status.Component != "worker" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal(status.Metadata, &raw); err != nil {
			return liveMigrationWorkerMetadata{}, fmt.Errorf("unmarshal worker metadata: %w", err)
		}
		meta := liveMigrationWorkerMetadata{
			Runtime:                strings.TrimSpace(conv.String(raw["runtime"])),
			CloudHypervisorVersion: strings.TrimSpace(conv.String(raw["cloud_hypervisor_version"])),
			SharedRootfsDir:        strings.TrimSpace(conv.String(raw["shared_rootfs_dir"])),
		}
		if b, ok := raw["live_migration_enabled"].(bool); ok {
			meta.LiveMigrationEnabled = b
		}
		return meta, nil
	}
	return liveMigrationWorkerMetadata{}, errors.New("destination server worker heartbeat is missing")
}


