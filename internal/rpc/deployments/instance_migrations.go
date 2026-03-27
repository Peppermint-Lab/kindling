package deployments

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/rpc/rpcutil"
	"github.com/kindlingvm/kindling/internal/shared/conv"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

const migrationCutoverDeadline = 10 * time.Minute // deadline for live migration cutover

type liveMigrationWorkerMetadata struct {
	Runtime                string
	LiveMigrationEnabled   bool
	CloudHypervisorVersion string
	SharedRootfsDir        string
}

var errLiveMigrationPersistentVolume = errors.New("live migration does not support workloads with persistent volumes yet")

type deploymentInstanceMigrationOut struct {
	ID                    string  `json:"id"`
	DeploymentInstanceID  string  `json:"deployment_instance_id"`
	SourceServerID        string  `json:"source_server_id"`
	DestinationServerID   string  `json:"destination_server_id"`
	SourceVMID            string  `json:"source_vm_id"`
	State                 string  `json:"state"`
	Mode                  string  `json:"mode"`
	ReceiveAddr           string  `json:"receive_addr,omitempty"`
	DestinationRuntimeURL string  `json:"destination_runtime_url,omitempty"`
	FailureCode           string  `json:"failure_code,omitempty"`
	FailureMessage        string  `json:"failure_message,omitempty"`
	StartedAt             *string `json:"started_at,omitempty"`
	CompletedAt           *string `json:"completed_at,omitempty"`
	FailedAt              *string `json:"failed_at,omitempty"`
	AbortedAt             *string `json:"aborted_at,omitempty"`
}

// RegisterMigrationRoutes mounts instance migration routes on the given mux.
func (h *Handler) RegisterMigrationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/deployment-instances/{id}/migration", h.getDeploymentInstanceMigration)
	mux.HandleFunc("POST /api/deployment-instances/{id}/live-migrate", h.postDeploymentInstanceLiveMigrate)
}

func (h *Handler) getDeploymentInstanceMigration(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	if !rpcutil.RequirePlatformAdmin(w, p) {
		return
	}
	id, err := rpcutil.ParseUUID(r.PathValue("id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_id", "invalid deployment instance id")
		return
	}
	row, err := h.Q.InstanceMigrationLatestByDeploymentInstanceID(r.Context(), id)
	if err != nil {
		if err == pgx.ErrNoRows {
			rpcutil.WriteJSON(w, http.StatusOK, map[string]any{"migration": nil})
			return
		}
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "instance_migration", err)
		return
	}
	rpcutil.WriteJSON(w, http.StatusOK, map[string]any{"migration": migrationToOut(row)})
}

func (h *Handler) postDeploymentInstanceLiveMigrate(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	if !rpcutil.RequirePlatformAdmin(w, p) {
		return
	}
	instanceID, err := rpcutil.ParseUUID(r.PathValue("id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_id", "invalid deployment instance id")
		return
	}
	inst, err := h.Q.DeploymentInstanceFirstByID(r.Context(), instanceID)
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "deployment instance not found")
		return
	}
	if inst.DeletedAt.Valid || !inst.ServerID.Valid || !inst.VmID.Valid || inst.Status != "running" || inst.Role != "active" {
		rpcutil.WriteAPIError(w, http.StatusConflict, "invalid_state", "instance must be an active running instance with a server and VM")
		return
	}
	vm, err := h.Q.VMFirstByID(r.Context(), inst.VmID)
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "instance_vm", err)
		return
	}
	if vm.Runtime != "cloud-hypervisor" {
		rpcutil.WriteAPIError(w, http.StatusConflict, "unsupported_runtime", "live migration requires a cloud-hypervisor workload")
		return
	}
	if strings.TrimSpace(vm.SharedRootfsRef) == "" {
		rpcutil.WriteAPIError(w, http.StatusConflict, "shared_rootfs_missing", "live migration requires a shared rootfs reference")
		return
	}
	dep, err := h.Q.DeploymentFirstByID(r.Context(), inst.DeploymentID)
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "deployment", err)
		return
	}
	if err := validateLiveMigrationProjectVolume(h.Q.ProjectVolumeFindByProjectID(r.Context(), dep.ProjectID)); err != nil {
		if errors.Is(err, errLiveMigrationPersistentVolume) {
			rpcutil.WriteAPIError(w, http.StatusConflict, "unsupported_volume", err.Error())
			return
		}
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "project_volume", err)
		return
	}
	if _, err := h.Q.InstanceMigrationFindActiveByDeploymentInstanceID(r.Context(), instanceID); err == nil {
		rpcutil.WriteAPIError(w, http.StatusConflict, "migration_in_progress", "an active migration already exists for this instance")
		return
	} else if err != nil && err != pgx.ErrNoRows {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "instance_migration", err)
		return
	}
	sourceServer, err := h.Q.ServerFindByID(r.Context(), inst.ServerID)
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "source_server", err)
		return
	}
	if sourceServer.Status != "active" {
		rpcutil.WriteAPIError(w, http.StatusConflict, "source_server_state", "source server must be active")
		return
	}
	sourceWorker, err := h.liveMigrationWorkerMetadataForServer(r.Context(), uuid.UUID(inst.ServerID.Bytes))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusConflict, "source_server_runtime", err.Error())
		return
	}
	destServerID, err := h.pickLiveMigrationDestination(r.Context(), sourceServer, sourceWorker)
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusConflict, "destination_unavailable", err.Error())
		return
	}
	token := sha256.Sum256([]byte(uuid.NewString()))
	mig, err := h.Q.InstanceMigrationCreate(r.Context(), queries.InstanceMigrationCreateParams{
		ID:                   pgtype.UUID{Bytes: uuid.New(), Valid: true},
		DeploymentInstanceID: inst.ID,
		SourceServerID:       inst.ServerID,
		DestinationServerID:  pgtype.UUID{Bytes: destServerID, Valid: true},
		SourceVmID:           inst.VmID,
		State:                "pending",
		Mode:                 "stop_and_copy",
		ReceiveTokenHash:     token[:],
		CutoverDeadlineAt:    pgtype.Timestamptz{Time: time.Now().Add(migrationCutoverDeadline), Valid: true},
	})
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "instance_migration_create", err)
		return
	}
	rpcutil.WriteJSON(w, http.StatusAccepted, migrationToOut(mig))
}

func validateLiveMigrationProjectVolume(_ queries.ProjectVolume, err error) error {
	switch {
	case err == nil:
		return errLiveMigrationPersistentVolume
	case errors.Is(err, pgx.ErrNoRows):
		return nil
	default:
		return fmt.Errorf("check project volume: %w", err)
	}
}

func (h *Handler) pickLiveMigrationDestination(ctx context.Context, sourceServer queries.Server, sourceWorker liveMigrationWorkerMetadata) (uuid.UUID, error) {
	servers, err := h.Q.ServerFindAll(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	statuses, err := h.Q.ServerComponentStatusFindAll(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	workerByServer := make(map[uuid.UUID]queries.ServerComponentStatus)
	for _, row := range statuses {
		if row.Component == "worker" {
			workerByServer[uuid.UUID(row.ServerID.Bytes)] = row
		}
	}
	best := uuid.Nil
	bestCount := int64(0)
	for _, server := range servers {
		sid := uuid.UUID(server.ID.Bytes)
		if sid == uuid.UUID(sourceServer.ID.Bytes) || server.Status != "active" {
			continue
		}
		status, ok := workerByServer[sid]
		if !ok {
			continue
		}
		meta, err := parseLiveMigrationWorkerMetadata(status)
		if err != nil {
			continue
		}
		if err := validateLiveMigrationDestination(sourceWorker, server, meta); err != nil {
			continue
		}
		count, err := h.Q.DeploymentInstanceActiveCountByServerID(ctx, server.ID)
		if err != nil {
			return uuid.Nil, err
		}
		if best == uuid.Nil || count < bestCount {
			best = sid
			bestCount = count
		}
	}
	if best == uuid.Nil {
		return uuid.Nil, errNoLiveMigrationDestination
	}
	return best, nil
}

func (h *Handler) liveMigrationWorkerMetadataForServer(ctx context.Context, serverID uuid.UUID) (liveMigrationWorkerMetadata, error) {
	statuses, err := h.Q.ServerComponentStatusFindByServerID(ctx, pgtype.UUID{Bytes: serverID, Valid: true})
	if err != nil {
		return liveMigrationWorkerMetadata{}, err
	}
	for _, status := range statuses {
		if status.Component != "worker" {
			continue
		}
		meta, err := parseLiveMigrationWorkerMetadata(status)
		if err != nil {
			return liveMigrationWorkerMetadata{}, err
		}
		if err := validateLiveMigrationSource(meta); err != nil {
			return liveMigrationWorkerMetadata{}, err
		}
		return meta, nil
	}
	return liveMigrationWorkerMetadata{}, errors.New("source server worker heartbeat is missing")
}

var errNoLiveMigrationDestination = errors.New("no active cloud-hypervisor destination server is available for live migration")

// ParseLiveMigrationWorkerMetadata extracts migration metadata from a server component status.
func ParseLiveMigrationWorkerMetadata(status queries.ServerComponentStatus) (liveMigrationWorkerMetadata, error) {
	return parseLiveMigrationWorkerMetadata(status)
}

func parseLiveMigrationWorkerMetadata(status queries.ServerComponentStatus) (liveMigrationWorkerMetadata, error) {
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

// ValidateLiveMigrationSource checks that a server is a valid live migration source.
func ValidateLiveMigrationSource(meta liveMigrationWorkerMetadata) error {
	return validateLiveMigrationSource(meta)
}

func validateLiveMigrationSource(meta liveMigrationWorkerMetadata) error {
	if meta.Runtime != "cloud-hypervisor" {
		return errors.New("source server worker runtime is not cloud-hypervisor")
	}
	if !meta.LiveMigrationEnabled {
		return errors.New("source server worker does not advertise live migration support")
	}
	if meta.SharedRootfsDir == "" {
		return errors.New("source server worker does not advertise shared rootfs storage")
	}
	return nil
}

// ValidateLiveMigrationDestination checks that a server is a valid live migration destination.
func ValidateLiveMigrationDestination(source liveMigrationWorkerMetadata, server queries.Server, destination liveMigrationWorkerMetadata) error {
	return validateLiveMigrationDestination(source, server, destination)
}

func validateLiveMigrationDestination(source liveMigrationWorkerMetadata, server queries.Server, destination liveMigrationWorkerMetadata) error {
	if strings.TrimSpace(server.InternalIp) == "" || server.InternalIp == "127.0.0.1" || server.InternalIp == "0.0.0.0" {
		return errors.New("destination server has no usable internal IP")
	}
	if destination.Runtime != "cloud-hypervisor" {
		return errors.New("destination worker runtime is not cloud-hypervisor")
	}
	if !destination.LiveMigrationEnabled {
		return errors.New("destination worker does not advertise live migration support")
	}
	if destination.SharedRootfsDir == "" {
		return errors.New("destination worker does not advertise shared rootfs storage")
	}
	if source.SharedRootfsDir != "" && destination.SharedRootfsDir != "" && source.SharedRootfsDir != destination.SharedRootfsDir {
		return fmt.Errorf("destination worker shared rootfs dir %q does not match source %q", destination.SharedRootfsDir, source.SharedRootfsDir)
	}
	if source.CloudHypervisorVersion != "" && destination.CloudHypervisorVersion != "" && source.CloudHypervisorVersion != destination.CloudHypervisorVersion {
		return fmt.Errorf("destination worker cloud-hypervisor version %q does not match source %q", destination.CloudHypervisorVersion, source.CloudHypervisorVersion)
	}
	return nil
}

func migrationToOut(row queries.InstanceMigration) deploymentInstanceMigrationOut {
	return deploymentInstanceMigrationOut{
		ID:                    pguuid.ToString(row.ID),
		DeploymentInstanceID:  pguuid.ToString(row.DeploymentInstanceID),
		SourceServerID:        pguuid.ToString(row.SourceServerID),
		DestinationServerID:   pguuid.ToString(row.DestinationServerID),
		SourceVMID:            pguuid.ToString(row.SourceVmID),
		State:                 row.State,
		Mode:                  row.Mode,
		ReceiveAddr:           row.ReceiveAddr,
		DestinationRuntimeURL: row.DestinationRuntimeUrl,
		FailureCode:           row.FailureCode,
		FailureMessage:        row.FailureMessage,
		StartedAt:             rpcutil.FormatTS(row.StartedAt),
		CompletedAt:           rpcutil.FormatTS(row.CompletedAt),
		FailedAt:              rpcutil.FormatTS(row.FailedAt),
		AbortedAt:             rpcutil.FormatTS(row.AbortedAt),
	}
}
