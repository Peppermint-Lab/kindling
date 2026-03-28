// Package deployments provides deployment-related API handlers.
package deployments

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/rpc/rpcutil"
	"github.com/kindlingvm/kindling/internal/shared/netnames"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

// DeploymentOut is the JSON shape for deployment resources (API v0.2).
type DeploymentOut struct {
	ID                   string                     `json:"id"`
	ProjectID            string                     `json:"project_id"`
	ServiceID            *string                    `json:"service_id,omitempty"`
	BuildID              *string                    `json:"build_id,omitempty"`
	ImageID              *string                    `json:"image_id,omitempty"`
	VmID                 *string                    `json:"vm_id,omitempty"`
	GithubCommit         string                     `json:"github_commit"`
	RunningAt            *string                    `json:"running_at"`
	StoppedAt            *string                    `json:"stopped_at"`
	FailedAt             *string                    `json:"failed_at"`
	CreatedAt            *string                    `json:"created_at"`
	UpdatedAt            *string                    `json:"updated_at"`
	BuildStatus          string                     `json:"build_status,omitempty"`
	Phase                string                     `json:"phase"`
	DesiredInstanceCount int                        `json:"desired_instance_count,omitempty"`
	MinInstanceCount     int                        `json:"min_instance_count,omitempty"`
	MaxInstanceCount     int                        `json:"max_instance_count,omitempty"`
	RunningInstanceCount int                        `json:"running_instance_count,omitempty"`
	ScaledToZero         bool                       `json:"scaled_to_zero,omitempty"`
	ScaleToZeroEnabled   bool                       `json:"scale_to_zero_enabled,omitempty"`
	WakeRequestedAt      *string                    `json:"wake_requested_at,omitempty"`
	DeploymentKind       string                     `json:"deployment_kind,omitempty"`
	GithubBranch         string                     `json:"github_branch,omitempty"`
	PreviewEnvironmentID *string                    `json:"preview_environment_id,omitempty"`
	BlockedReason        string                     `json:"blocked_reason,omitempty"`
	ServiceName          string                     `json:"service_name,omitempty"`
	PersistentVolume     *DeploymentVolumeOut       `json:"persistent_volume,omitempty"`
	Reachable            *DeploymentReachabilityOut `json:"reachable,omitempty"`
}

// DeploymentListItemOut extends DeploymentOut with project name.
type DeploymentListItemOut struct {
	DeploymentOut
	ProjectName string `json:"project_name"`
}

// DeploymentReachabilityOut contains reachability info for a deployment.
type DeploymentReachabilityOut struct {
	PublicURL           string                         `json:"public_url,omitempty"`
	RuntimeURL          string                         `json:"runtime_url,omitempty"`
	Domain              string                         `json:"domain,omitempty"`
	VmIP                string                         `json:"vm_ip,omitempty"`
	Port                *int                           `json:"port,omitempty"`
	ProxiesToDeployment *bool                          `json:"proxies_to_deployment,omitempty"`
	PublicEndpoints     []DeploymentPublicEndpointOut  `json:"public_endpoints,omitempty"`
	PrivateEndpoints    []DeploymentPrivateEndpointOut `json:"private_endpoints,omitempty"`
}

// DeploymentVolumeOut contains volume info for a deployment.
type DeploymentVolumeOut struct {
	ID           string  `json:"id"`
	ProjectID    string  `json:"project_id"`
	ServerID     *string `json:"server_id,omitempty"`
	AttachedVMID *string `json:"attached_vm_id,omitempty"`
	MountPath    string  `json:"mount_path"`
	SizeGB       int32   `json:"size_gb"`
	Filesystem   string  `json:"filesystem"`
	Status       string  `json:"status"`
	Health       string  `json:"health"`
	LastError    string  `json:"last_error,omitempty"`
}

// DeploymentPublicEndpointOut represents a public endpoint.
type DeploymentPublicEndpointOut struct {
	Domain              string `json:"domain"`
	PublicURL           string `json:"public_url"`
	RedirectTo          string `json:"redirect_to,omitempty"`
	RedirectStatusCode  *int   `json:"redirect_status_code,omitempty"`
	ProxiesToDeployment *bool  `json:"proxies_to_deployment,omitempty"`
}

type DeploymentPrivateEndpointOut struct {
	Name       string `json:"name"`
	Protocol   string `json:"protocol"`
	Port       int32  `json:"port"`
	Visibility string `json:"visibility"`
	PrivateIP  string `json:"private_ip"`
	DNSName    string `json:"dns_name"`
}

func deploymentVolumeToOut(v queries.ProjectVolume) DeploymentVolumeOut {
	return DeploymentVolumeOut{
		ID:           pguuid.ToString(v.ID),
		ProjectID:    pguuid.ToString(v.ProjectID),
		ServerID:     rpcutil.OptionalUUIDString(v.ServerID),
		AttachedVMID: rpcutil.OptionalUUIDString(v.AttachedVmID),
		MountPath:    v.MountPath,
		SizeGB:       v.SizeGb,
		Filesystem:   v.Filesystem,
		Status:       v.Status,
		Health:       v.Health,
		LastError:    strings.TrimSpace(v.LastError),
	}
}

func isProjectVolumeTransitionalStatus(status string) bool {
	switch status {
	case "backing_up", "restoring", "repairing", "deleting":
		return true
	default:
		return false
	}
}

func decorateDeploymentOutWithVolume(out *DeploymentOut, dep queries.Deployment, vol *queries.ProjectVolume) {
	if out == nil || vol == nil {
		return
	}
	volOut := deploymentVolumeToOut(*vol)
	out.PersistentVolume = &volOut
	if !dep.RunningAt.Valid && (vol.Status == "unavailable" || isProjectVolumeTransitionalStatus(vol.Status)) {
		out.BlockedReason = strings.TrimSpace(vol.LastError)
		if out.BlockedReason == "" {
			out.BlockedReason = "persistent volume is busy"
		}
	}
}

func deploymentDesiredReplicaCount(proj queries.Project, service *queries.Service) int {
	if service != nil && service.DesiredInstanceCount > 0 {
		return int(service.DesiredInstanceCount)
	}
	return int(proj.DesiredInstanceCount)
}

func (h *Handler) deploymentVolume(ctx context.Context, dep queries.Deployment) (*queries.ProjectVolume, error) {
	if dep.ServiceID.Valid {
		vol, err := h.Q.ProjectVolumeFindByServiceID(ctx, dep.ServiceID)
		if err == nil {
			return &vol, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	vol, err := h.Q.ProjectVolumeFindByProjectID(ctx, dep.ProjectID)
	if err != nil {
		return nil, err
	}
	return &vol, nil
}

// DeploymentToOut converts a deployment and optional build/reachability into the API output.
func DeploymentToOut(dep queries.Deployment, build *queries.Build, reachable *DeploymentReachabilityOut) DeploymentOut {
	var bs string
	if build != nil {
		bs = build.Status
	}
	out := DeploymentOut{
		ID:           pguuid.ToString(dep.ID),
		ProjectID:    pguuid.ToString(dep.ProjectID),
		ServiceID:    rpcutil.OptionalUUIDString(dep.ServiceID),
		BuildID:      rpcutil.OptionalUUIDString(dep.BuildID),
		ImageID:      rpcutil.OptionalUUIDString(dep.ImageID),
		VmID:         rpcutil.OptionalUUIDString(dep.VmID),
		GithubCommit: dep.GithubCommit,
		RunningAt:    rpcutil.FormatTS(dep.RunningAt),
		StoppedAt:    rpcutil.FormatTS(dep.StoppedAt),
		FailedAt:     rpcutil.FormatTS(dep.FailedAt),
		CreatedAt:    rpcutil.FormatTS(dep.CreatedAt),
		UpdatedAt:    rpcutil.FormatTS(dep.UpdatedAt),
		BuildStatus:  bs,
		Phase:        DeploymentPhase(dep, build),
		Reachable:    reachable,
	}
	kind := strings.TrimSpace(dep.DeploymentKind)
	if kind == "" {
		kind = "production"
	}
	out.DeploymentKind = kind
	if strings.TrimSpace(dep.GithubBranch) != "" {
		out.GithubBranch = dep.GithubBranch
	}
	out.PreviewEnvironmentID = rpcutil.OptionalUUIDString(dep.PreviewEnvironmentID)
	return out
}

// ToOutCtx builds a full deployment output using DB context.
func (h *Handler) ToOutCtx(ctx context.Context, dep queries.Deployment) DeploymentOut {
	var build *queries.Build
	if dep.BuildID.Valid {
		b, err := h.Q.BuildFirstByID(ctx, dep.BuildID)
		if err == nil {
			build = &b
		}
	}
	out := DeploymentToOut(dep, build, h.reachability(ctx, dep))
	out.WakeRequestedAt = rpcutil.FormatTS(dep.WakeRequestedAt)
	var service *queries.Service
	if proj, err := h.Q.ProjectFirstByID(ctx, dep.ProjectID); err == nil {
		out.MinInstanceCount = int(proj.MinInstanceCount)
		out.MaxInstanceCount = int(proj.MaxInstanceCount)
		out.ScaledToZero = proj.ScaledToZero
		out.ScaleToZeroEnabled = proj.ScaleToZeroEnabled
		if dep.ServiceID.Valid {
			if svc, err := h.Q.ServiceFirstByID(ctx, dep.ServiceID); err == nil {
				service = &svc
				out.ServiceName = svc.Name
			}
		}
		out.DesiredInstanceCount = deploymentDesiredReplicaCount(proj, service)
	}
	if vol, err := h.deploymentVolume(ctx, dep); err == nil {
		decorateDeploymentOutWithVolume(&out, dep, vol)
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		// Ignore volume lookup failures and preserve deployment response.
	}
	insts, err := h.Q.DeploymentInstanceFindByDeploymentID(ctx, dep.ID)
	if err == nil {
		rc := 0
		for _, inst := range insts {
			if inst.Status == "running" && inst.VmID.Valid {
				rc++
			}
		}
		out.RunningInstanceCount = rc
		if out.VmID == nil {
			for _, inst := range insts {
				if inst.VmID.Valid {
					out.VmID = rpcutil.OptionalUUIDString(inst.VmID)
					break
				}
			}
		}
	}
	return out
}

// ListRowForOrgToOutCtx converts an org-scoped deployment row.
func (h *Handler) ListRowForOrgToOutCtx(ctx context.Context, row queries.DeploymentFindRecentWithProjectForOrgRow) DeploymentListItemOut {
	return h.ListRowToOutCtx(ctx, queries.DeploymentFindRecentWithProjectRow{
		ID:                   row.ID,
		ProjectID:            row.ProjectID,
		BuildID:              row.BuildID,
		ImageID:              row.ImageID,
		VmID:                 row.VmID,
		GithubCommit:         row.GithubCommit,
		GithubBranch:         row.GithubBranch,
		DeploymentKind:       row.DeploymentKind,
		PreviewEnvironmentID: row.PreviewEnvironmentID,
		PreviewLastRequestAt: row.PreviewLastRequestAt,
		PreviewScaledToZero:  row.PreviewScaledToZero,
		RunningAt:            row.RunningAt,
		StoppedAt:            row.StoppedAt,
		FailedAt:             row.FailedAt,
		DeletedAt:            row.DeletedAt,
		WakeRequestedAt:      row.WakeRequestedAt,
		CreatedAt:            row.CreatedAt,
		UpdatedAt:            row.UpdatedAt,
		ProjectName:          row.ProjectName,
		BuildStatus:          row.BuildStatus,
	})
}

// ListRowToOutCtx converts a deployment list row using DB context.
func (h *Handler) ListRowToOutCtx(ctx context.Context, row queries.DeploymentFindRecentWithProjectRow) DeploymentListItemOut {
	st := rpcutil.PgTextString(row.BuildStatus)
	if row.BuildID.Valid && st == "" {
		st = "pending"
	}
	var buildPtr *queries.Build
	if row.BuildID.Valid {
		buildPtr = &queries.Build{Status: st}
	}
	dep := queries.Deployment{
		ID:                   row.ID,
		ProjectID:            row.ProjectID,
		ServiceID:            row.ServiceID,
		BuildID:              row.BuildID,
		ImageID:              row.ImageID,
		VmID:                 row.VmID,
		GithubCommit:         row.GithubCommit,
		GithubBranch:         row.GithubBranch,
		DeploymentKind:       row.DeploymentKind,
		PreviewEnvironmentID: row.PreviewEnvironmentID,
		PreviewLastRequestAt: row.PreviewLastRequestAt,
		PreviewScaledToZero:  row.PreviewScaledToZero,
		RunningAt:            row.RunningAt,
		StoppedAt:            row.StoppedAt,
		FailedAt:             row.FailedAt,
		DeletedAt:            row.DeletedAt,
		WakeRequestedAt:      row.WakeRequestedAt,
		CreatedAt:            row.CreatedAt,
		UpdatedAt:            row.UpdatedAt,
	}
	out := DeploymentToOut(dep, buildPtr, h.reachability(ctx, dep))
	out.WakeRequestedAt = rpcutil.FormatTS(dep.WakeRequestedAt)
	var service *queries.Service
	if proj, err := h.Q.ProjectFirstByID(ctx, dep.ProjectID); err == nil {
		out.MinInstanceCount = int(proj.MinInstanceCount)
		out.MaxInstanceCount = int(proj.MaxInstanceCount)
		out.ScaledToZero = proj.ScaledToZero
		out.ScaleToZeroEnabled = proj.ScaleToZeroEnabled
		if dep.ServiceID.Valid {
			if svc, err := h.Q.ServiceFirstByID(ctx, dep.ServiceID); err == nil {
				service = &svc
				out.ServiceName = svc.Name
			}
		}
		out.DesiredInstanceCount = deploymentDesiredReplicaCount(proj, service)
	}
	if vol, err := h.deploymentVolume(ctx, dep); err == nil {
		decorateDeploymentOutWithVolume(&out, dep, vol)
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		// Ignore volume lookup failures and preserve deployment response.
	}
	insts, err := h.Q.DeploymentInstanceFindByDeploymentID(ctx, dep.ID)
	if err == nil {
		rc := 0
		for _, inst := range insts {
			if inst.Status == "running" && inst.VmID.Valid {
				rc++
			}
		}
		out.RunningInstanceCount = rc
		if out.VmID == nil {
			for _, inst := range insts {
				if inst.VmID.Valid {
					out.VmID = rpcutil.OptionalUUIDString(inst.VmID)
					break
				}
			}
		}
	}
	return DeploymentListItemOut{DeploymentOut: out, ProjectName: row.ProjectName}
}

func (h *Handler) reachability(ctx context.Context, dep queries.Deployment) *DeploymentReachabilityOut {
	var vms []*queries.Vm
	if instances, err := h.Q.DeploymentInstanceFindByDeploymentID(ctx, dep.ID); err == nil {
		for _, inst := range instances {
			if !inst.VmID.Valid {
				continue
			}
			v, err := h.Q.VMFirstByID(ctx, inst.VmID)
			if err == nil && !v.DeletedAt.Valid {
				vms = append(vms, &v)
			}
		}
	}
	if len(vms) == 0 && dep.VmID.Valid {
		v, err := h.Q.VMFirstByID(ctx, dep.VmID)
		if err == nil && !v.DeletedAt.Valid {
			vms = append(vms, &v)
		}
	}

	var doms []queries.Domain
	rows, err := h.Q.DomainFindVerifiedByDeploymentID(ctx, dep.ID)
	if err == nil {
		doms = rows
	}
	privateEndpoints := h.privateEndpointsForDeployment(ctx, dep)
	return BuildDeploymentReachability(vms, doms, privateEndpoints)
}

func (h *Handler) privateEndpointsForDeployment(ctx context.Context, dep queries.Deployment) []DeploymentPrivateEndpointOut {
	if !dep.ServiceID.Valid {
		return nil
	}
	service, err := h.Q.ServiceFirstByID(ctx, dep.ServiceID)
	if err != nil {
		return nil
	}
	project, err := h.Q.ProjectFirstByID(ctx, dep.ProjectID)
	if err != nil {
		return nil
	}
	org, err := h.Q.OrganizationByID(ctx, project.OrgID)
	if err != nil {
		return nil
	}
	envSlug := "prod"
	if strings.EqualFold(dep.DeploymentKind, "preview") {
		envSlug = "preview"
		if dep.PreviewEnvironmentID.Valid {
			if pe, err := h.Q.PreviewEnvironmentByID(ctx, dep.PreviewEnvironmentID); err == nil && pe.PrNumber > 0 {
				envSlug = fmt.Sprintf("pr-%d", pe.PrNumber)
			}
		}
	}
	rows, err := h.Q.ServiceEndpointListByServiceID(ctx, dep.ServiceID)
	if err != nil {
		return nil
	}
	out := make([]DeploymentPrivateEndpointOut, 0, len(rows))
	for _, endpoint := range rows {
		out = append(out, DeploymentPrivateEndpointOut{
			Name:       endpoint.Name,
			Protocol:   endpoint.Protocol,
			Port:       endpoint.TargetPort,
			Visibility: endpoint.Visibility,
			PrivateIP:  endpoint.PrivateIp.String(),
			DNSName:    netnames.PrivateDNSName(endpoint.Name, service.Slug, project.Name, envSlug, org.Slug),
		})
	}
	return out
}

func BuildDeploymentReachability(vms []*queries.Vm, doms []queries.Domain, privateEndpoints []DeploymentPrivateEndpointOut) *DeploymentReachabilityOut {
	var out DeploymentReachabilityOut

	if len(vms) > 0 {
		vm := vms[0]
		out.VmIP = vm.IpAddress.String()
		if vm.Port.Valid {
			port := int(vm.Port.Int32)
			out.Port = &port
			out.RuntimeURL = formatRuntimeURL(vm.IpAddress, port)
		}
	}

	if len(doms) > 0 {
		doms = append([]queries.Domain(nil), doms...)
		slices.SortFunc(doms, func(a, b queries.Domain) int {
			return strings.Compare(a.DomainName, b.DomainName)
		})

		out.PublicEndpoints = make([]DeploymentPublicEndpointOut, 0, len(doms))
		for _, domain := range doms {
			proxies := domain.RedirectTo.String == ""
			endpoint := DeploymentPublicEndpointOut{
				Domain:              domain.DomainName,
				PublicURL:           "https://" + domain.DomainName,
				ProxiesToDeployment: boolPtr(proxies),
			}
			if domain.RedirectTo.Valid {
				endpoint.RedirectTo = domain.RedirectTo.String
			}
			if domain.RedirectStatusCode.Valid {
				code := int(domain.RedirectStatusCode.Int32)
				endpoint.RedirectStatusCode = &code
			}
			out.PublicEndpoints = append(out.PublicEndpoints, endpoint)
		}

		primary := out.PublicEndpoints[0]
		out.PublicURL = primary.PublicURL
		out.Domain = primary.Domain
		out.ProxiesToDeployment = primary.ProxiesToDeployment
	}

	if len(privateEndpoints) > 0 {
		out.PrivateEndpoints = append([]DeploymentPrivateEndpointOut(nil), privateEndpoints...)
	}

	if out.PublicURL == "" && out.RuntimeURL == "" && len(out.PublicEndpoints) == 0 && len(out.PrivateEndpoints) == 0 {
		return nil
	}
	return &out
}

// BuildDeploymentReachabilityFromVMs constructs reachability info from VMs and domains.
func BuildDeploymentReachabilityFromVMs(vms []*queries.Vm, doms []queries.Domain) *DeploymentReachabilityOut {
	return BuildDeploymentReachability(vms, doms, nil)
}

func formatRuntimeURL(addr netip.Addr, port int) string {
	return "http://" + net.JoinHostPort(addr.String(), strconv.Itoa(port))
}

func boolPtr(v bool) *bool {
	return &v
}

// DeploymentPhase derives a coarse UI phase from deployment + optional build row.
func DeploymentPhase(dep queries.Deployment, build *queries.Build) string {
	if build != nil && build.Status == "failed" {
		return "failed"
	}
	if dep.FailedAt.Valid {
		return "failed"
	}
	if dep.StoppedAt.Valid {
		return "stopped"
	}
	if dep.RunningAt.Valid {
		return "running"
	}
	if build != nil {
		switch build.Status {
		case "building":
			return "building"
		case "successful":
			// Image ready but VM not running yet — instance starting.
			return "starting"
		case "pending":
			return "queued"
		}
	}
	return "pending"
}
