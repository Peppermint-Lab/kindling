package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

type instanceVMMetadataStore interface {
	DeploymentInstanceFirstByID(context.Context, pgtype.UUID) (queries.DeploymentInstance, error)
	VMCreate(context.Context, queries.VMCreateParams) (queries.Vm, error)
	DeploymentInstanceAttachVM(context.Context, queries.DeploymentInstanceAttachVMParams) (queries.DeploymentInstance, error)
	VMSoftDelete(context.Context, pgtype.UUID) error
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
	if port < 1 || port > 65535 {
		return netip.Addr{}, 0, fmt.Errorf("port out of range: %d", port)
	}

	return addr, port, nil
}

// persistInstanceVMMetadata creates a `vms` row and attaches it to a deployment_instance.
func (d *Deployer) persistInstanceVMMetadata(
	ctx context.Context,
	store instanceVMMetadataStore,
	instanceID pgtype.UUID,
	imageID pgtype.UUID,
	serverID uuid.UUID,
	runtimeAddr string,
	vcpus int,
	memoryMB int,
	env []string,
	meta instanceVMMetadata,
) (uuid.UUID, error) {
	inst, err := store.DeploymentInstanceFirstByID(ctx, instanceID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("fetch deployment instance: %w", err)
	}
	if inst.VmID.Valid {
		return pguuid.FromPgtype(inst.VmID), nil
	}
	if !imageID.Valid {
		return uuid.Nil, fmt.Errorf("deployment image id is missing")
	}

	ip, port, err := parseRuntimeAddress(runtimeAddr)
	if err != nil {
		return uuid.Nil, err
	}

	envJSON, err := json.Marshal(env)
	if err != nil {
		return uuid.Nil, fmt.Errorf("marshal env variables: %w", err)
	}

	vmID := uuid.New()
	if _, err := store.VMCreate(ctx, queries.VMCreateParams{
		ID:              pguuid.ToPgtype(vmID),
		ServerID:        pguuid.ToPgtype(serverID),
		ImageID:         imageID,
		Status:          "running",
		Runtime:         meta.Runtime,
		SnapshotRef:     pgtype.Text{String: meta.SnapshotRef, Valid: strings.TrimSpace(meta.SnapshotRef) != ""},
		SharedRootfsRef: meta.SharedRootfsRef,
		CloneSourceVmID: meta.CloneSourceVMID,
		Vcpus:           int32(vcpus),
		Memory:          int32(memoryMB),
		IpAddress:       ip,
		Port:            pgtype.Int4{Int32: int32(port), Valid: true},
		EnvVariables:    pgtype.Text{String: string(envJSON), Valid: true},
	}); err != nil {
		return uuid.Nil, fmt.Errorf("create vm row: %w", err)
	}

	if _, err := store.DeploymentInstanceAttachVM(ctx, queries.DeploymentInstanceAttachVMParams{
		ID:     instanceID,
		VmID:   pguuid.ToPgtype(vmID),
		Status: "running",
	}); err != nil {
		_ = store.VMSoftDelete(ctx, pguuid.ToPgtype(vmID))
		return uuid.Nil, fmt.Errorf("attach vm to deployment instance: %w", err)
	}

	return vmID, nil
}
