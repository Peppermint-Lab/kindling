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
)

type runtimeVMMetadataStore interface {
	VMCreate(context.Context, queries.VMCreateParams) (queries.Vm, error)
	DeploymentUpdateVM(context.Context, queries.DeploymentUpdateVMParams) (queries.Deployment, error)
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

func (d *Deployer) persistRuntimeVMMetadata(
	ctx context.Context,
	store runtimeVMMetadataStore,
	dep queries.Deployment,
	runtimeAddr string,
	vcpus int,
	memoryMB int,
	env []string,
) (queries.Deployment, error) {
	if dep.VmID.Valid {
		return dep, nil
	}
	if !dep.ImageID.Valid {
		return dep, fmt.Errorf("deployment image id is missing")
	}

	ip, port, err := parseRuntimeAddress(runtimeAddr)
	if err != nil {
		return dep, err
	}

	envJSON, err := json.Marshal(env)
	if err != nil {
		return dep, fmt.Errorf("marshal env variables: %w", err)
	}

	vmID := uuid.New()
	if _, err := store.VMCreate(ctx, queries.VMCreateParams{
		ID:           uuidToPgtype(vmID),
		ServerID:     uuidToPgtype(d.serverID),
		ImageID:      dep.ImageID,
		Status:       "running",
		Vcpus:        int32(vcpus),
		Memory:       int32(memoryMB),
		IpAddress:    ip,
		Port:         pgtype.Int4{Int32: int32(port), Valid: true},
		EnvVariables: pgtype.Text{String: string(envJSON), Valid: true},
	}); err != nil {
		return dep, fmt.Errorf("create vm row: %w", err)
	}

	updated, err := store.DeploymentUpdateVM(ctx, queries.DeploymentUpdateVMParams{
		ID:   dep.ID,
		VmID: uuidToPgtype(vmID),
	})
	if err != nil {
		_ = store.VMSoftDelete(ctx, uuidToPgtype(vmID))
		return dep, fmt.Errorf("attach vm to deployment: %w", err)
	}

	return updated, nil
}
