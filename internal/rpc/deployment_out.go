package rpc

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

// deploymentOut is the JSON shape for deployment resources (API v0.2).
type deploymentOut struct {
	ID                   string                     `json:"id"`
	ProjectID            string                     `json:"project_id"`
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
	RunningInstanceCount int                        `json:"running_instance_count,omitempty"`
	ScaledToZero         bool                       `json:"scaled_to_zero,omitempty"`
	ScaleToZeroEnabled   bool                       `json:"scale_to_zero_enabled,omitempty"`
	WakeRequestedAt      *string                    `json:"wake_requested_at,omitempty"`
	Reachable            *deploymentReachabilityOut `json:"reachable,omitempty"`
}

type deploymentListItemOut struct {
	deploymentOut
	ProjectName string `json:"project_name"`
}

type deploymentReachabilityOut struct {
	PublicURL           string                        `json:"public_url,omitempty"`
	RuntimeURL          string                        `json:"runtime_url,omitempty"`
	Domain              string                        `json:"domain,omitempty"`
	VmIP                string                        `json:"vm_ip,omitempty"`
	Port                *int                          `json:"port,omitempty"`
	ProxiesToDeployment *bool                         `json:"proxies_to_deployment,omitempty"`
	PublicEndpoints     []deploymentPublicEndpointOut `json:"public_endpoints,omitempty"`
}

type deploymentPublicEndpointOut struct {
	Domain              string `json:"domain"`
	PublicURL           string `json:"public_url"`
	RedirectTo          string `json:"redirect_to,omitempty"`
	RedirectStatusCode  *int   `json:"redirect_status_code,omitempty"`
	ProxiesToDeployment *bool  `json:"proxies_to_deployment,omitempty"`
}

func formatTS(t pgtype.Timestamptz) *string {
	if !t.Valid {
		return nil
	}
	s := t.Time.UTC().Format(time.RFC3339Nano)
	return &s
}

func optionalUUIDString(u pgtype.UUID) *string {
	if !u.Valid {
		return nil
	}
	s := pgUUIDToString(u)
	return &s
}

func deploymentToOut(dep queries.Deployment, build *queries.Build, reachable *deploymentReachabilityOut) deploymentOut {
	var bs string
	if build != nil {
		bs = build.Status
	}
	return deploymentOut{
		ID:           pgUUIDToString(dep.ID),
		ProjectID:    pgUUIDToString(dep.ProjectID),
		BuildID:      optionalUUIDString(dep.BuildID),
		ImageID:      optionalUUIDString(dep.ImageID),
		VmID:         optionalUUIDString(dep.VmID),
		GithubCommit: dep.GithubCommit,
		RunningAt:    formatTS(dep.RunningAt),
		StoppedAt:    formatTS(dep.StoppedAt),
		FailedAt:     formatTS(dep.FailedAt),
		CreatedAt:    formatTS(dep.CreatedAt),
		UpdatedAt:    formatTS(dep.UpdatedAt),
		BuildStatus:  bs,
		Phase:        deploymentPhase(dep, build),
		Reachable:    reachable,
	}
}

func (a *API) deploymentToOutCtx(ctx context.Context, dep queries.Deployment) deploymentOut {
	var build *queries.Build
	if dep.BuildID.Valid {
		b, err := a.q.BuildFirstByID(ctx, dep.BuildID)
		if err == nil {
			build = &b
		}
	}
	out := deploymentToOut(dep, build, a.deploymentReachability(ctx, dep))
	out.WakeRequestedAt = formatTS(dep.WakeRequestedAt)
	if proj, err := a.q.ProjectFirstByID(ctx, dep.ProjectID); err == nil {
		out.DesiredInstanceCount = int(proj.DesiredInstanceCount)
		out.ScaledToZero = proj.ScaledToZero
		out.ScaleToZeroEnabled = proj.ScaleToZeroEnabled
	}
	insts, err := a.q.DeploymentInstanceFindByDeploymentID(ctx, dep.ID)
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
					out.VmID = optionalUUIDString(inst.VmID)
					break
				}
			}
		}
	}
	return out
}

func (a *API) listRowForOrgToOutCtx(ctx context.Context, row queries.DeploymentFindRecentWithProjectForOrgRow) deploymentListItemOut {
	return a.listRowToOutCtx(ctx, queries.DeploymentFindRecentWithProjectRow{
		ID:              row.ID,
		ProjectID:       row.ProjectID,
		BuildID:         row.BuildID,
		ImageID:         row.ImageID,
		VmID:            row.VmID,
		GithubCommit:    row.GithubCommit,
		RunningAt:       row.RunningAt,
		StoppedAt:       row.StoppedAt,
		FailedAt:        row.FailedAt,
		DeletedAt:       row.DeletedAt,
		WakeRequestedAt: row.WakeRequestedAt,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
		ProjectName:     row.ProjectName,
		BuildStatus:     row.BuildStatus,
	})
}

func (a *API) listRowToOutCtx(ctx context.Context, row queries.DeploymentFindRecentWithProjectRow) deploymentListItemOut {
	st := pgTextString(row.BuildStatus)
	if row.BuildID.Valid && st == "" {
		st = "pending"
	}
	var buildPtr *queries.Build
	if row.BuildID.Valid {
		buildPtr = &queries.Build{Status: st}
	}
	dep := queries.Deployment{
		ID:              row.ID,
		ProjectID:       row.ProjectID,
		BuildID:         row.BuildID,
		ImageID:         row.ImageID,
		VmID:            row.VmID,
		GithubCommit:    row.GithubCommit,
		RunningAt:       row.RunningAt,
		StoppedAt:       row.StoppedAt,
		FailedAt:        row.FailedAt,
		DeletedAt:       row.DeletedAt,
		WakeRequestedAt: row.WakeRequestedAt,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
	out := deploymentToOut(dep, buildPtr, a.deploymentReachability(ctx, dep))
	out.WakeRequestedAt = formatTS(dep.WakeRequestedAt)
	if proj, err := a.q.ProjectFirstByID(ctx, dep.ProjectID); err == nil {
		out.DesiredInstanceCount = int(proj.DesiredInstanceCount)
		out.ScaledToZero = proj.ScaledToZero
		out.ScaleToZeroEnabled = proj.ScaleToZeroEnabled
	}
	if insts, err := a.q.DeploymentInstanceFindByDeploymentID(ctx, dep.ID); err == nil {
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
					out.VmID = optionalUUIDString(inst.VmID)
					break
				}
			}
		}
	}
	return deploymentListItemOut{deploymentOut: out, ProjectName: row.ProjectName}
}

func (a *API) deploymentReachability(ctx context.Context, dep queries.Deployment) *deploymentReachabilityOut {
	var vms []*queries.Vm
	if instances, err := a.q.DeploymentInstanceFindByDeploymentID(ctx, dep.ID); err == nil {
		for _, inst := range instances {
			if !inst.VmID.Valid {
				continue
			}
			v, err := a.q.VMFirstByID(ctx, inst.VmID)
			if err == nil && !v.DeletedAt.Valid {
				vms = append(vms, &v)
			}
		}
	}
	if len(vms) == 0 && dep.VmID.Valid {
		v, err := a.q.VMFirstByID(ctx, dep.VmID)
		if err == nil && !v.DeletedAt.Valid {
			vms = append(vms, &v)
		}
	}

	var domains []queries.Domain
	rows, err := a.q.DomainFindVerifiedByDeploymentID(ctx, dep.ID)
	if err == nil {
		domains = rows
	}

	return buildDeploymentReachabilityFromVMs(vms, domains)
}

func buildDeploymentReachabilityFromVMs(vms []*queries.Vm, domains []queries.Domain) *deploymentReachabilityOut {
	var out deploymentReachabilityOut

	if len(vms) > 0 {
		vm := vms[0]
		out.VmIP = vm.IpAddress.String()
		if vm.Port.Valid {
			port := int(vm.Port.Int32)
			out.Port = &port
			out.RuntimeURL = formatRuntimeURL(vm.IpAddress, port)
		}
	}

	if len(domains) > 0 {
		domains = append([]queries.Domain(nil), domains...)
		slices.SortFunc(domains, func(a, b queries.Domain) int {
			return strings.Compare(a.DomainName, b.DomainName)
		})

		out.PublicEndpoints = make([]deploymentPublicEndpointOut, 0, len(domains))
		for _, domain := range domains {
			proxies := domain.RedirectTo.String == ""
			endpoint := deploymentPublicEndpointOut{
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

	if out.PublicURL == "" && out.RuntimeURL == "" && len(out.PublicEndpoints) == 0 {
		return nil
	}
	return &out
}

func formatRuntimeURL(addr netip.Addr, port int) string {
	return "http://" + net.JoinHostPort(addr.String(), strconv.Itoa(port))
}

func boolPtr(v bool) *bool {
	return &v
}
