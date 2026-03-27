package volumeops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/reconciler"
	"github.com/kindlingvm/kindling/internal/runtime"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
	"github.com/kindlingvm/kindling/internal/volumebackup"
)

const (
	defaultStalePendingAfter = 2 * time.Minute
	defaultStaleRunningAfter = 45 * time.Minute
)

type Handler struct {
	q        *queries.Queries
	cfg      *config.Manager
	serverID uuid.UUID
}

type operationRequest struct {
	BackupID       string `json:"backup_id,omitempty"`
	BackupKind     string `json:"backup_kind,omitempty"`
	TargetServerID string `json:"target_server_id,omitempty"`
	SourceServerID string `json:"source_server_id,omitempty"`
	Stage          string `json:"stage,omitempty"`
	StorageKey     string `json:"storage_key,omitempty"`
	DeleteAfter    bool   `json:"delete_after,omitempty"`
}

type operationResult struct {
	BackupID       string `json:"backup_id,omitempty"`
	TargetServerID string `json:"target_server_id,omitempty"`
	StorageKey     string `json:"storage_key,omitempty"`
	StorageURL     string `json:"storage_url,omitempty"`
	SizeBytes      int64  `json:"size_bytes,omitempty"`
}

func NewHandler(q *queries.Queries, cfg *config.Manager, serverID uuid.UUID) *Handler {
	return &Handler{q: q, cfg: cfg, serverID: serverID}
}

func (h *Handler) Reconcile(ctx context.Context, operationID uuid.UUID) error {
	if h == nil || h.q == nil {
		return nil
	}
	op, err := h.q.ProjectVolumeOperationFindByID(ctx, pguuid.ToPgtype(operationID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if op.Status == "succeeded" || op.Status == "failed" {
		return nil
	}

	vol, err := h.q.ProjectVolumeFindByID(ctx, op.ProjectVolumeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}

	if h.isStaleRunning(op) {
		return h.failOperation(ctx, op, vol, "volume operation timed out", "", "")
	}

	if op.ServerID.Valid && uuid.UUID(op.ServerID.Bytes) != h.serverID {
		serverHealthy, serverMessage := h.operationServerHealthy(ctx, op.ServerID)
		if serverHealthy && op.Status == "pending" {
			return nil
		}
		if serverHealthy && op.Status == "running" {
			return nil
		}
		return h.failOperation(ctx, op, vol, serverMessage, "", "")
	}

	if op.Status == "pending" {
		op, err = h.q.ProjectVolumeOperationUpdateRunning(ctx, op.ID)
		if err != nil {
			return err
		}
	}

	switch op.Kind {
	case "backup":
		return h.runBackup(ctx, op, vol)
	case "restore":
		return h.runRestore(ctx, op, vol)
	case "move":
		return h.runMove(ctx, op, vol)
	case "repair":
		return h.runRepair(ctx, op, vol)
	default:
		return h.failOperation(ctx, op, vol, fmt.Sprintf("unsupported volume operation kind %q", op.Kind), "", "")
	}
}

func QueueRecoverableOperations(ctx context.Context, q *queries.Queries, sched *reconciler.Scheduler) error {
	if q == nil || sched == nil {
		return nil
	}
	pending, err := q.ProjectVolumeOperationFindStalePending(ctx, int64(defaultStalePendingAfter/time.Second))
	if err != nil {
		return err
	}
	for _, op := range pending {
		if op.ID.Valid {
			sched.ScheduleNow(uuid.UUID(op.ID.Bytes))
		}
	}
	running, err := q.ProjectVolumeOperationFindStaleRunning(ctx, int64(defaultStaleRunningAfter/time.Second))
	if err != nil {
		return err
	}
	for _, op := range running {
		if op.ID.Valid {
			sched.ScheduleNow(uuid.UUID(op.ID.Bytes))
		}
	}
	return nil
}

func (h *Handler) runBackup(ctx context.Context, op queries.ProjectVolumeOperation, vol queries.ProjectVolume) error {
	req, err := decodeOperationRequest(op.RequestMetadata)
	if err != nil {
		return h.failOperation(ctx, op, vol, fmt.Sprintf("decode backup request: %v", err), "", "")
	}
	if vol.AttachedVmID.Valid || vol.Status == "attached" {
		return h.failOperation(ctx, op, vol, "detach the volume before creating a backup", "", "")
	}
	if !vol.ServerID.Valid {
		return h.failOperation(ctx, op, vol, "volume is not pinned to a server", "", "")
	}
	backupID, err := uuid.Parse(strings.TrimSpace(req.BackupID))
	if err != nil {
		return h.failOperation(ctx, op, vol, "backup operation is missing a backup id", "", "")
	}
	store, err := h.backupStore(ctx)
	if err != nil {
		return h.failOperation(ctx, op, vol, err.Error(), "", "")
	}
	backup, err := h.q.ProjectVolumeBackupFindByID(ctx, pguuid.ToPgtype(backupID))
	if err != nil {
		return h.failOperation(ctx, op, vol, fmt.Sprintf("load backup: %v", err), "", "")
	}
	if _, err := h.q.ProjectVolumeBackupUpdateState(ctx, queries.ProjectVolumeBackupUpdateStateParams{
		ID:         backup.ID,
		Status:     "running",
		StorageUrl: backup.StorageUrl,
		StorageKey: backup.StorageKey,
		SizeBytes:  backup.SizeBytes,
		Error:      "",
		Metadata:   backup.Metadata,
	}); err != nil {
		return h.failOperation(ctx, op, vol, fmt.Sprintf("mark backup running: %v", err), "", "")
	}

	path := runtime.PersistentVolumePath(uuid.UUID(vol.ID.Bytes))
	health, healthErr := localVolumeHealth(ctx, path)
	if healthErr != "" {
		return h.failOperation(ctx, op, vol, healthErr, health, req.BackupID)
	}
	key := volumebackup.BackupObjectKey(uuid.UUID(vol.ID.Bytes), backupID)
	storageURL, sizeBytes, err := store.UploadFile(ctx, key, path)
	if err != nil {
		return h.failOperation(ctx, op, vol, fmt.Sprintf("upload backup: %v", err), "degraded", req.BackupID)
	}

	meta := backup.Metadata
	if len(meta) == 0 {
		meta = []byte(`{}`)
	}
	if _, err := h.q.ProjectVolumeBackupUpdateState(ctx, queries.ProjectVolumeBackupUpdateStateParams{
		ID:         backup.ID,
		Status:     "succeeded",
		StorageUrl: storageURL,
		StorageKey: key,
		SizeBytes:  sizeBytes,
		Error:      "",
		Metadata:   meta,
	}); err != nil {
		return h.failOperation(ctx, op, vol, fmt.Sprintf("mark backup succeeded: %v", err), "degraded", req.BackupID)
	}
	if err := h.trimBackups(ctx, vol, store); err != nil {
		return h.failOperation(ctx, op, vol, fmt.Sprintf("trim backups: %v", err), "degraded", req.BackupID)
	}

	result := operationResult{
		BackupID:   req.BackupID,
		StorageKey: key,
		StorageURL: storageURL,
		SizeBytes:  sizeBytes,
	}
	if req.DeleteAfter {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return h.failOperation(ctx, op, vol, fmt.Sprintf("delete local volume after backup: %v", err), "degraded", req.BackupID)
		}
		if _, err := h.q.ProjectVolumeOperationUpdateState(ctx, queries.ProjectVolumeOperationUpdateStateParams{
			ID:              op.ID,
			ServerID:        op.ServerID,
			Status:          "succeeded",
			RequestMetadata: op.RequestMetadata,
			ResultMetadata:  mustJSON(result),
			Error:           "",
		}); err != nil {
			return err
		}
		return h.q.ProjectVolumeSoftDelete(ctx, vol.ProjectID)
	}
	return h.completeOperation(ctx, op, vol, result, "detached", "healthy", "")
}

func (h *Handler) runRestore(ctx context.Context, op queries.ProjectVolumeOperation, vol queries.ProjectVolume) error {
	req, err := decodeOperationRequest(op.RequestMetadata)
	if err != nil {
		return h.failOperation(ctx, op, vol, fmt.Sprintf("decode restore request: %v", err), "", "")
	}
	if vol.AttachedVmID.Valid || vol.Status == "attached" {
		return h.failOperation(ctx, op, vol, "detach the volume before restoring it", "", "")
	}
	backupID, err := uuid.Parse(strings.TrimSpace(req.BackupID))
	if err != nil {
		return h.failOperation(ctx, op, vol, "restore operation is missing a backup id", "", "")
	}
	targetServerID, err := uuid.Parse(strings.TrimSpace(req.TargetServerID))
	if err != nil {
		return h.failOperation(ctx, op, vol, "restore operation is missing a target server", "", "")
	}
	store, err := h.backupStore(ctx)
	if err != nil {
		return h.failOperation(ctx, op, vol, err.Error(), "", "")
	}
	backup, err := h.q.ProjectVolumeBackupFindByID(ctx, pguuid.ToPgtype(backupID))
	if err != nil {
		return h.failOperation(ctx, op, vol, fmt.Sprintf("load backup: %v", err), "", "")
	}
	if backup.Status != "succeeded" || strings.TrimSpace(backup.StorageKey) == "" {
		return h.failOperation(ctx, op, vol, "backup is not ready to restore", "", "")
	}
	path := runtime.PersistentVolumePath(uuid.UUID(vol.ID.Bytes))
	if err := downloadToPath(ctx, store, backup.StorageKey, path); err != nil {
		return h.failOperation(ctx, op, vol, fmt.Sprintf("restore backup: %v", err), "degraded", "")
	}
	if _, err := h.q.ProjectVolumeAssignServer(ctx, queries.ProjectVolumeAssignServerParams{
		ProjectID: vol.ProjectID,
		ServerID:  pguuid.ToPgtype(targetServerID),
	}); err != nil {
		return h.failOperation(ctx, op, vol, fmt.Sprintf("pin restored volume server: %v", err), "degraded", "")
	}
	result := operationResult{
		BackupID:       req.BackupID,
		TargetServerID: req.TargetServerID,
		StorageKey:     backup.StorageKey,
		StorageURL:     backup.StorageUrl,
		SizeBytes:      backup.SizeBytes,
	}
	return h.completeOperation(ctx, op, vol, result, "detached", "healthy", "")
}

func (h *Handler) runMove(ctx context.Context, op queries.ProjectVolumeOperation, vol queries.ProjectVolume) error {
	req, err := decodeOperationRequest(op.RequestMetadata)
	if err != nil {
		return h.failOperation(ctx, op, vol, fmt.Sprintf("decode move request: %v", err), "", "")
	}
	if vol.AttachedVmID.Valid || vol.Status == "attached" {
		return h.failOperation(ctx, op, vol, "detach the volume before moving it", "", "")
	}
	if strings.TrimSpace(req.SourceServerID) == "" {
		req.SourceServerID = pguuid.ToString(vol.ServerID)
	}
	if strings.TrimSpace(req.Stage) == "" {
		req.Stage = "upload"
	}
	targetServerID, err := uuid.Parse(strings.TrimSpace(req.TargetServerID))
	if err != nil {
		return h.failOperation(ctx, op, vol, "move operation is missing a target server", "", "")
	}
	if strings.TrimSpace(req.SourceServerID) == targetServerID.String() {
		return h.failOperation(ctx, op, vol, "volume is already pinned to that server", "", "")
	}
	store, err := h.backupStore(ctx)
	if err != nil {
		return h.failOperation(ctx, op, vol, err.Error(), "", "")
	}
	switch req.Stage {
	case "upload":
		path := runtime.PersistentVolumePath(uuid.UUID(vol.ID.Bytes))
		health, healthErr := localVolumeHealth(ctx, path)
		if healthErr != "" {
			return h.failOperation(ctx, op, vol, healthErr, health, "")
		}
		key := strings.TrimSpace(req.StorageKey)
		if key == "" {
			key = volumebackup.MoveObjectKey(uuid.UUID(op.ID.Bytes))
			req.StorageKey = key
		}
		storageURL, sizeBytes, err := store.UploadFile(ctx, key, path)
		if err != nil {
			return h.failOperation(ctx, op, vol, fmt.Sprintf("upload volume for move: %v", err), "degraded", "")
		}
		req.Stage = "download"
		reqBytes, _ := json.Marshal(req)
		result := operationResult{
			TargetServerID: req.TargetServerID,
			StorageKey:     key,
			StorageURL:     storageURL,
			SizeBytes:      sizeBytes,
		}
		resultBytes, _ := json.Marshal(result)
		_, err = h.q.ProjectVolumeOperationUpdateState(ctx, queries.ProjectVolumeOperationUpdateStateParams{
			ID:              op.ID,
			ServerID:        pguuid.ToPgtype(targetServerID),
			Status:          "pending",
			RequestMetadata: reqBytes,
			ResultMetadata:  resultBytes,
			Error:           "",
		})
		return err
	case "download":
		path := runtime.PersistentVolumePath(uuid.UUID(vol.ID.Bytes))
		if err := downloadToPath(ctx, store, req.StorageKey, path); err != nil {
			return h.failOperation(ctx, op, vol, fmt.Sprintf("download moved volume: %v", err), "degraded", "")
		}
		if err := store.DeleteObject(ctx, req.StorageKey); err != nil {
			return h.failOperation(ctx, op, vol, fmt.Sprintf("delete move staging object: %v", err), "degraded", "")
		}
		if _, err := h.q.ProjectVolumeAssignServer(ctx, queries.ProjectVolumeAssignServerParams{
			ProjectID: vol.ProjectID,
			ServerID:  pguuid.ToPgtype(targetServerID),
		}); err != nil {
			return h.failOperation(ctx, op, vol, fmt.Sprintf("pin moved volume server: %v", err), "degraded", "")
		}
		result := operationResult{
			TargetServerID: req.TargetServerID,
			StorageKey:     req.StorageKey,
		}
		return h.completeOperation(ctx, op, vol, result, "detached", "healthy", "")
	default:
		return h.failOperation(ctx, op, vol, fmt.Sprintf("unsupported move stage %q", req.Stage), "", "")
	}
}

func (h *Handler) runRepair(ctx context.Context, op queries.ProjectVolumeOperation, vol queries.ProjectVolume) error {
	if vol.AttachedVmID.Valid || vol.Status == "attached" {
		return h.failOperation(ctx, op, vol, "detach the volume before repairing it", "", "")
	}
	path := runtime.PersistentVolumePath(uuid.UUID(vol.ID.Bytes))
	health, healthErr := localVolumeHealth(ctx, path)
	if healthErr != "" && health == "missing" {
		return h.failOperation(ctx, op, vol, healthErr, health, "")
	}
	if out, err := exec.CommandContext(ctx, "qemu-img", "check", "-r", "all", path).CombinedOutput(); err != nil {
		return h.failOperation(ctx, op, vol, fmt.Sprintf("qemu-img check: %s: %v", strings.TrimSpace(string(out)), err), "corrupt", "")
	}
	return h.completeOperation(ctx, op, vol, operationResult{}, "detached", "healthy", "")
}

func (h *Handler) completeOperation(ctx context.Context, op queries.ProjectVolumeOperation, vol queries.ProjectVolume, result operationResult, status, health, lastError string) error {
	resultBytes, _ := json.Marshal(result)
	if _, err := h.q.ProjectVolumeOperationUpdateState(ctx, queries.ProjectVolumeOperationUpdateStateParams{
		ID:              op.ID,
		ServerID:        op.ServerID,
		Status:          "succeeded",
		RequestMetadata: op.RequestMetadata,
		ResultMetadata:  resultBytes,
		Error:           "",
	}); err != nil {
		return err
	}
	_, err := h.q.ProjectVolumeUpdateStatusAndHealth(ctx, queries.ProjectVolumeUpdateStatusAndHealthParams{
		ProjectID: vol.ProjectID,
		Status:    status,
		Health:    health,
		LastError: strings.TrimSpace(lastError),
	})
	return err
}

func (h *Handler) failOperation(ctx context.Context, op queries.ProjectVolumeOperation, vol queries.ProjectVolume, message, health, backupID string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "volume operation failed"
	}
	if backupID != "" {
		if id, err := uuid.Parse(backupID); err == nil {
			backup, getErr := h.q.ProjectVolumeBackupFindByID(ctx, pguuid.ToPgtype(id))
			if getErr == nil {
				_, _ = h.q.ProjectVolumeBackupUpdateState(ctx, queries.ProjectVolumeBackupUpdateStateParams{
					ID:         backup.ID,
					Status:     "failed",
					StorageUrl: backup.StorageUrl,
					StorageKey: backup.StorageKey,
					SizeBytes:  backup.SizeBytes,
					Error:      message,
					Metadata:   backup.Metadata,
				})
			}
		}
	}
	if _, err := h.q.ProjectVolumeOperationUpdateState(ctx, queries.ProjectVolumeOperationUpdateStateParams{
		ID:              op.ID,
		ServerID:        op.ServerID,
		Status:          "failed",
		RequestMetadata: op.RequestMetadata,
		ResultMetadata:  op.ResultMetadata,
		Error:           message,
	}); err != nil {
		return err
	}
	nextStatus := "detached"
	nextHealth := strings.TrimSpace(health)
	if nextHealth == "" {
		nextHealth = vol.Health
	}
	if nextHealth == "" || nextHealth == "unknown" {
		nextHealth = "degraded"
	}
	if nextHealth == "missing" || nextHealth == "corrupt" {
		nextStatus = "unavailable"
	}
	_, err := h.q.ProjectVolumeUpdateStatusAndHealth(ctx, queries.ProjectVolumeUpdateStatusAndHealthParams{
		ProjectID: vol.ProjectID,
		Status:    nextStatus,
		Health:    nextHealth,
		LastError: message,
	})
	return err
}

func (h *Handler) backupStore(ctx context.Context) (volumebackup.Store, error) {
	if h.cfg == nil {
		return nil, fmt.Errorf("volume backup store is not configured")
	}
	snap := h.cfg.Snapshot()
	if snap == nil {
		return nil, fmt.Errorf("volume backup store is not configured")
	}
	return volumebackup.NewStoreFromSnapshot(ctx, snap)
}

func (h *Handler) trimBackups(ctx context.Context, vol queries.ProjectVolume, store volumebackup.Store) error {
	if vol.BackupRetentionCount <= 0 {
		return nil
	}
	backups, err := h.q.ProjectVolumeBackupFindByProjectID(ctx, vol.ProjectID)
	if err != nil {
		return err
	}
	kept := int32(0)
	for _, backup := range backups {
		if backup.Status != "succeeded" {
			continue
		}
		kept++
		if kept <= vol.BackupRetentionCount {
			continue
		}
		if key := strings.TrimSpace(backup.StorageKey); key != "" {
			if err := store.DeleteObject(ctx, key); err != nil {
				return err
			}
		}
		if err := h.q.ProjectVolumeBackupDeleteByID(ctx, backup.ID); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) operationServerHealthy(ctx context.Context, serverID pgtype.UUID) (bool, string) {
	if !serverID.Valid {
		return false, "volume operation has no assigned server"
	}
	server, err := h.q.ServerFindByID(ctx, serverID)
	if err != nil {
		return false, fmt.Sprintf("assigned server lookup failed: %v", err)
	}
	if server.Status != "active" {
		return false, fmt.Sprintf("assigned server %s is %s", uuid.UUID(server.ID.Bytes), server.Status)
	}
	if !h.serverSupportsCloudHypervisor(ctx, server.ID) {
		return false, fmt.Sprintf("assigned server %s is not a cloud-hypervisor worker", uuid.UUID(server.ID.Bytes))
	}
	return true, ""
}

func (h *Handler) serverSupportsCloudHypervisor(ctx context.Context, serverID pgtype.UUID) bool {
	rows, err := h.q.ServerComponentStatusFindByServerID(ctx, serverID)
	if err != nil {
		return false
	}
	for _, row := range rows {
		if row.Component != "worker" || row.Status != "healthy" {
			continue
		}
		var meta map[string]any
		if err := json.Unmarshal(row.Metadata, &meta); err != nil {
			continue
		}
		if runtimeName, _ := meta["runtime"].(string); runtimeName == "cloud-hypervisor" {
			return true
		}
	}
	return false
}

func (h *Handler) isStaleRunning(op queries.ProjectVolumeOperation) bool {
	if op.Status != "running" || !op.StartedAt.Valid {
		return false
	}
	return time.Since(op.StartedAt.Time) > defaultStaleRunningAfter
}

func decodeOperationRequest(raw []byte) (operationRequest, error) {
	if len(raw) == 0 {
		return operationRequest{}, nil
	}
	var req operationRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return operationRequest{}, err
	}
	return req, nil
}

func mustJSON(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}

func localVolumeHealth(ctx context.Context, path string) (health string, message string) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "missing", fmt.Sprintf("persistent volume disk is missing at %s", path)
		}
		return "degraded", fmt.Sprintf("stat persistent volume disk: %v", err)
	}
	if out, err := exec.CommandContext(ctx, "qemu-img", "info", "--output=json", path).CombinedOutput(); err != nil {
		return "corrupt", fmt.Sprintf("qemu-img info: %s: %v", strings.TrimSpace(string(out)), err)
	}
	return "healthy", ""
}

func downloadToPath(ctx context.Context, store volumebackup.Store, storageKey, dstPath string) error {
	if strings.TrimSpace(storageKey) == "" {
		return fmt.Errorf("storage key is required")
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(dstPath), ".kindling-volume-*")
	if err != nil {
		return fmt.Errorf("create temp volume file: %w", err)
	}
	tmpPath := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp volume file: %w", err)
	}
	defer os.Remove(tmpPath)
	if err := store.DownloadFile(ctx, storageKey, tmpPath); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		return fmt.Errorf("replace destination volume: %w", err)
	}
	return nil
}
