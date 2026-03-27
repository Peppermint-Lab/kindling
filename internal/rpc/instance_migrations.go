package rpc

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
)

type liveMigrationWorkerMetadata struct {
	Runtime                string
	LiveMigrationEnabled   bool
	CloudHypervisorVersion string
	SharedRootfsDir        string
}

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

func (a *API) getDeploymentInstanceMigration(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requirePlatformAdmin(w, p) {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid deployment instance id")
		return
	}
	row, err := a.q.InstanceMigrationLatestByDeploymentInstanceID(r.Context(), id)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeJSON(w, http.StatusOK, map[string]any{"migration": nil})
			return
		}
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "instance_migration", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"migration": migrationToOut(row)})
}

func (a *API) postDeploymentInstanceLiveMigrate(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requirePlatformAdmin(w, p) {
		return
	}
	instanceID, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid deployment instance id")
		return
	}
	inst, err := a.q.DeploymentInstanceFirstByID(r.Context(), instanceID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "deployment instance not found")
		return
	}
	if inst.DeletedAt.Valid || !inst.ServerID.Valid || !inst.VmID.Valid || inst.Status != "running" || inst.Role != "active" {
		writeAPIError(w, http.StatusConflict, "invalid_state", "instance must be an active running instance with a server and VM")
		return
	}
	vm, err := a.q.VMFirstByID(r.Context(), inst.VmID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "instance_vm", err)
		return
	}
	if vm.Runtime != "cloud-hypervisor" {
		writeAPIError(w, http.StatusConflict, "unsupported_runtime", "live migration requires a cloud-hypervisor workload")
		return
	}
	if strings.TrimSpace(vm.SharedRootfsRef) == "" {
		writeAPIError(w, http.StatusConflict, "shared_rootfs_missing", "live migration requires a shared rootfs reference")
		return
	}
	if _, err := a.q.InstanceMigrationFindActiveByDeploymentInstanceID(r.Context(), instanceID); err == nil {
		writeAPIError(w, http.StatusConflict, "migration_in_progress", "an active migration already exists for this instance")
		return
	} else if err != nil && err != pgx.ErrNoRows {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "instance_migration", err)
		return
	}
	sourceServer, err := a.q.ServerFindByID(r.Context(), inst.ServerID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "source_server", err)
		return
	}
	if sourceServer.Status != "active" {
		writeAPIError(w, http.StatusConflict, "source_server_state", "source server must be active")
		return
	}
	sourceWorker, err := a.liveMigrationWorkerMetadataForServer(r.Context(), uuid.UUID(inst.ServerID.Bytes))
	if err != nil {
		writeAPIError(w, http.StatusConflict, "source_server_runtime", err.Error())
		return
	}
	destServerID, err := a.pickLiveMigrationDestination(r.Context(), sourceServer, sourceWorker)
	if err != nil {
		writeAPIError(w, http.StatusConflict, "destination_unavailable", err.Error())
		return
	}
	token := sha256.Sum256([]byte(uuid.NewString()))
	mig, err := a.q.InstanceMigrationCreate(r.Context(), queries.InstanceMigrationCreateParams{
		ID:                   pgtype.UUID{Bytes: uuid.New(), Valid: true},
		DeploymentInstanceID: inst.ID,
		SourceServerID:       inst.ServerID,
		DestinationServerID:  pgtype.UUID{Bytes: destServerID, Valid: true},
		SourceVmID:           inst.VmID,
		State:                "pending",
		Mode:                 "stop_and_copy",
		ReceiveTokenHash:     token[:],
		CutoverDeadlineAt:    pgtype.Timestamptz{Time: time.Now().Add(10 * time.Minute), Valid: true},
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "instance_migration_create", err)
		return
	}
	writeJSON(w, http.StatusAccepted, migrationToOut(mig))
}

func (a *API) pickLiveMigrationDestination(ctx context.Context, sourceServer queries.Server, sourceWorker liveMigrationWorkerMetadata) (uuid.UUID, error) {
	servers, err := a.q.ServerFindAll(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	statuses, err := a.q.ServerComponentStatusFindAll(ctx)
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
		meta := parseLiveMigrationWorkerMetadata(status)
		if err := validateLiveMigrationDestination(sourceWorker, server, meta); err != nil {
			continue
		}
		count, err := a.q.DeploymentInstanceActiveCountByServerID(ctx, server.ID)
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

func (a *API) liveMigrationWorkerMetadataForServer(ctx context.Context, serverID uuid.UUID) (liveMigrationWorkerMetadata, error) {
	statuses, err := a.q.ServerComponentStatusFindByServerID(ctx, pgtype.UUID{Bytes: serverID, Valid: true})
	if err != nil {
		return liveMigrationWorkerMetadata{}, err
	}
	for _, status := range statuses {
		if status.Component != "worker" {
			continue
		}
		meta := parseLiveMigrationWorkerMetadata(status)
		if err := validateLiveMigrationSource(meta); err != nil {
			return liveMigrationWorkerMetadata{}, err
		}
		return meta, nil
	}
	return liveMigrationWorkerMetadata{}, errors.New("source server worker heartbeat is missing")
}

var errNoLiveMigrationDestination = errors.New("no active cloud-hypervisor destination server is available for live migration")

func parseLiveMigrationWorkerMetadata(status queries.ServerComponentStatus) liveMigrationWorkerMetadata {
	var raw map[string]any
	_ = json.Unmarshal(status.Metadata, &raw)
	meta := liveMigrationWorkerMetadata{
		Runtime:                strings.TrimSpace(stringValue(raw["runtime"])),
		CloudHypervisorVersion: strings.TrimSpace(stringValue(raw["cloud_hypervisor_version"])),
		SharedRootfsDir:        strings.TrimSpace(stringValue(raw["shared_rootfs_dir"])),
	}
	if b, ok := raw["live_migration_enabled"].(bool); ok {
		meta.LiveMigrationEnabled = b
	}
	return meta
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

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func migrationToOut(row queries.InstanceMigration) deploymentInstanceMigrationOut {
	return deploymentInstanceMigrationOut{
		ID:                    pgUUIDToString(row.ID),
		DeploymentInstanceID:  pgUUIDToString(row.DeploymentInstanceID),
		SourceServerID:        pgUUIDToString(row.SourceServerID),
		DestinationServerID:   pgUUIDToString(row.DestinationServerID),
		SourceVMID:            pgUUIDToString(row.SourceVmID),
		State:                 row.State,
		Mode:                  row.Mode,
		ReceiveAddr:           row.ReceiveAddr,
		DestinationRuntimeURL: row.DestinationRuntimeUrl,
		FailureCode:           row.FailureCode,
		FailureMessage:        row.FailureMessage,
		StartedAt:             formatTS(row.StartedAt),
		CompletedAt:           formatTS(row.CompletedAt),
		FailedAt:              formatTS(row.FailedAt),
		AbortedAt:             formatTS(row.AbortedAt),
	}
}
