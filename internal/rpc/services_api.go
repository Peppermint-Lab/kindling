package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/shared/netnames"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

func normalizeServiceSlug(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	var b strings.Builder
	b.Grow(len(raw))
	lastHyphen := false
	for _, r := range raw {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastHyphen = false
		case r == '-' || r == '_' || unicode.IsSpace(r):
			if b.Len() == 0 || lastHyphen {
				continue
			}
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func (a *API) primaryServiceForProject(ctx context.Context, projectID pgtype.UUID) (queries.Service, error) {
	return a.q.ServicePrimaryByProjectID(ctx, projectID)
}

type serviceEndpointOut struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Protocol        string  `json:"protocol"`
	TargetPort      int32   `json:"target_port"`
	Visibility      string  `json:"visibility"`
	PrivateIP       string  `json:"private_ip"`
	DNSName         string  `json:"dns_name"`
	PublicHostname  string  `json:"public_hostname,omitempty"`
	LastHealthyAt   *string `json:"last_healthy_at,omitempty"`
	LastUnhealthyAt *string `json:"last_unhealthy_at,omitempty"`
}

type serviceOut struct {
	ID                     string               `json:"id"`
	ProjectID              string               `json:"project_id"`
	Name                   string               `json:"name"`
	Slug                   string               `json:"slug"`
	RootDirectory          string               `json:"root_directory"`
	DockerfilePath         string               `json:"dockerfile_path"`
	DesiredInstanceCount   int32                `json:"desired_instance_count"`
	BuildOnlyOnRootChanges bool                 `json:"build_only_on_root_changes"`
	PublicDefault          bool                 `json:"public_default"`
	IsPrimary              bool                 `json:"is_primary"`
	OrgNetworkCIDR         string               `json:"org_network_cidr,omitempty"`
	Endpoints              []serviceEndpointOut `json:"endpoints,omitempty"`
	CreatedAt              *string              `json:"created_at,omitempty"`
	UpdatedAt              *string              `json:"updated_at,omitempty"`
}

func serviceFromCreateRow(row queries.ServiceCreateRow) queries.Service {
	return queries.Service{
		ID:                     row.ID,
		ProjectID:              row.ProjectID,
		Name:                   row.Name,
		Slug:                   row.Slug,
		RootDirectory:          row.RootDirectory,
		DockerfilePath:         row.DockerfilePath,
		DesiredInstanceCount:   row.DesiredInstanceCount,
		BuildOnlyOnRootChanges: row.BuildOnlyOnRootChanges,
		PublicDefault:          row.PublicDefault,
		IsPrimary:              row.IsPrimary,
		CreatedAt:              row.CreatedAt,
		UpdatedAt:              row.UpdatedAt,
	}
}

func serviceEndpointFromCreateRow(row queries.ServiceEndpointCreateRow) queries.ServiceEndpoint {
	return queries.ServiceEndpoint{
		ID:              row.ID,
		ServiceID:       row.ServiceID,
		Name:            row.Name,
		Protocol:        row.Protocol,
		TargetPort:      row.TargetPort,
		Visibility:      row.Visibility,
		PrivateIp:       row.PrivateIp,
		PublicHostname:  row.PublicHostname,
		LastHealthyAt:   row.LastHealthyAt,
		LastUnhealthyAt: row.LastUnhealthyAt,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

func serviceEndpointToOut(ep queries.ServiceEndpoint, service queries.Service, project queries.Project, org queries.Organization) serviceEndpointOut {
	return serviceEndpointOut{
		ID:              pguuid.ToString(ep.ID),
		Name:            ep.Name,
		Protocol:        ep.Protocol,
		TargetPort:      ep.TargetPort,
		Visibility:      ep.Visibility,
		PrivateIP:       ep.PrivateIp.String(),
		DNSName:         netnames.PrivateDNSName(ep.Name, service.Slug, project.Name, "prod", org.Slug),
		PublicHostname:  strings.TrimSpace(ep.PublicHostname),
		LastHealthyAt:   formatTS(ep.LastHealthyAt),
		LastUnhealthyAt: formatTS(ep.LastUnhealthyAt),
	}
}

func (a *API) serviceToOut(ctx context.Context, service queries.Service) (serviceOut, error) {
	project, err := a.q.ProjectFirstByID(ctx, service.ProjectID)
	if err != nil {
		return serviceOut{}, err
	}
	org, err := a.q.OrganizationByID(ctx, project.OrgID)
	if err != nil {
		return serviceOut{}, err
	}
	network, err := a.q.OrgNetworkByOrganizationID(ctx, org.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return serviceOut{}, err
	}
	endpoints, err := a.q.ServiceEndpointListByServiceID(ctx, service.ID)
	if err != nil {
		return serviceOut{}, err
	}
	out := serviceOut{
		ID:                     pguuid.ToString(service.ID),
		ProjectID:              pguuid.ToString(service.ProjectID),
		Name:                   service.Name,
		Slug:                   service.Slug,
		RootDirectory:          service.RootDirectory,
		DockerfilePath:         service.DockerfilePath,
		DesiredInstanceCount:   service.DesiredInstanceCount,
		BuildOnlyOnRootChanges: service.BuildOnlyOnRootChanges,
		PublicDefault:          service.PublicDefault,
		IsPrimary:              service.IsPrimary,
		CreatedAt:              formatTS(service.CreatedAt),
		UpdatedAt:              formatTS(service.UpdatedAt),
	}
	if network.OrganizationID.Valid {
		out.OrgNetworkCIDR = network.Cidr.String()
	}
	out.Endpoints = make([]serviceEndpointOut, 0, len(endpoints))
	for _, endpoint := range endpoints {
		out.Endpoints = append(out.Endpoints, serviceEndpointToOut(endpoint, service, project, org))
	}
	return out, nil
}

func (a *API) listProjectServices(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	projectID, _, ok := a.projectForRequest(w, r, p.OrganizationID)
	if !ok {
		return
	}
	services, err := a.q.ServiceListByProjectID(r.Context(), projectID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_project_services", err)
		return
	}
	out := make([]serviceOut, 0, len(services))
	for _, service := range services {
		item, err := a.serviceToOut(r.Context(), service)
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_project_services", err)
			return
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) createProjectService(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	projectID, project, ok := a.projectForRequest(w, r, p.OrganizationID)
	if !ok {
		return
	}
	var req struct {
		Name                   string `json:"name"`
		Slug                   string `json:"slug"`
		RootDirectory          string `json:"root_directory"`
		DockerfilePath         string `json:"dockerfile_path"`
		DesiredInstanceCount   *int32 `json:"desired_instance_count"`
		BuildOnlyOnRootChanges *bool  `json:"build_only_on_root_changes"`
		PublicDefault          *bool  `json:"public_default"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "name is required")
		return
	}
	slug := normalizeServiceSlug(req.Slug)
	if slug == "" {
		slug = normalizeServiceSlug(req.Name)
	}
	if slug == "" {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "slug is required")
		return
	}
	rootDirectory := strings.TrimSpace(req.RootDirectory)
	if rootDirectory == "" {
		rootDirectory = project.RootDirectory
	}
	dockerfilePath := strings.TrimSpace(req.DockerfilePath)
	if dockerfilePath == "" {
		dockerfilePath = project.DockerfilePath
	}
	desired := project.DesiredInstanceCount
	if req.DesiredInstanceCount != nil {
		desired = *req.DesiredInstanceCount
	}
	buildOnly := project.BuildOnlyOnRootChanges
	if req.BuildOnlyOnRootChanges != nil {
		buildOnly = *req.BuildOnlyOnRootChanges
	}
	publicDefault := false
	if req.PublicDefault != nil {
		publicDefault = *req.PublicDefault
	}
	service, err := a.q.ServiceCreate(r.Context(), queries.ServiceCreateParams{
		ID:                     pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ProjectID:              projectID,
		Name:                   req.Name,
		Slug:                   slug,
		RootDirectory:          rootDirectory,
		DockerfilePath:         dockerfilePath,
		DesiredInstanceCount:   desired,
		BuildOnlyOnRootChanges: buildOnly,
		PublicDefault:          publicDefault,
		IsPrimary:              false,
	})
	if err != nil {
		if isPgUniqueViolation(err) {
			writeAPIError(w, http.StatusConflict, "service_slug_taken", "that service slug is already in use for this project")
			return
		}
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "create_project_service", err)
		return
	}
	out, err := a.serviceToOut(r.Context(), serviceFromCreateRow(service))
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "create_project_service", err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (a *API) getService(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	serviceID, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid service id")
		return
	}
	service, err := a.q.ServiceFirstByIDAndOrg(r.Context(), queries.ServiceFirstByIDAndOrgParams{
		ID:    serviceID,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "service not found")
		return
	}
	out, err := a.serviceToOut(r.Context(), service)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "get_service", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) listServiceEndpoints(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	serviceID, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid service id")
		return
	}
	service, err := a.q.ServiceFirstByIDAndOrg(r.Context(), queries.ServiceFirstByIDAndOrgParams{
		ID:    serviceID,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "service not found")
		return
	}
	project, err := a.q.ProjectFirstByID(r.Context(), service.ProjectID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_service_endpoints", err)
		return
	}
	org, err := a.q.OrganizationByID(r.Context(), project.OrgID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_service_endpoints", err)
		return
	}
	endpoints, err := a.q.ServiceEndpointListByServiceID(r.Context(), service.ID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_service_endpoints", err)
		return
	}
	out := make([]serviceEndpointOut, 0, len(endpoints))
	for _, endpoint := range endpoints {
		out = append(out, serviceEndpointToOut(endpoint, service, project, org))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) createServiceEndpoint(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	serviceID, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid service id")
		return
	}
	service, err := a.q.ServiceFirstByIDAndOrg(r.Context(), queries.ServiceFirstByIDAndOrgParams{
		ID:    serviceID,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "service not found")
		return
	}
	var req struct {
		Name           string `json:"name"`
		Protocol       string `json:"protocol"`
		TargetPort     *int32 `json:"target_port"`
		Visibility     string `json:"visibility"`
		PublicHostname string `json:"public_hostname"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	name := normalizeServiceSlug(req.Name)
	if name == "" {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "name is required")
		return
	}
	protocol := strings.TrimSpace(strings.ToLower(req.Protocol))
	if protocol == "" {
		protocol = "http"
	}
	if protocol != "http" && protocol != "tcp" {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "protocol must be http or tcp")
		return
	}
	targetPort := int32(3000)
	if req.TargetPort != nil {
		targetPort = *req.TargetPort
	}
	if targetPort <= 0 || targetPort > 65535 {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "target_port must be between 1 and 65535")
		return
	}
	visibility := strings.TrimSpace(strings.ToLower(req.Visibility))
	if visibility == "" {
		visibility = "private"
	}
	if visibility != "private" && visibility != "public" {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "visibility must be private or public")
		return
	}
	endpoint, err := a.q.ServiceEndpointCreate(r.Context(), queries.ServiceEndpointCreateParams{
		ID:             service.ID,
		ID_2:           pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Name:           name,
		Protocol:       protocol,
		TargetPort:     targetPort,
		Visibility:     visibility,
		PublicHostname: strings.TrimSpace(req.PublicHostname),
	})
	if err != nil {
		if isPgUniqueViolation(err) {
			writeAPIError(w, http.StatusConflict, "endpoint_name_taken", "that endpoint name is already in use for this service")
			return
		}
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "create_service_endpoint", err)
		return
	}
	project, err := a.q.ProjectFirstByID(r.Context(), service.ProjectID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "create_service_endpoint", err)
		return
	}
	org, err := a.q.OrganizationByID(r.Context(), project.OrgID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "create_service_endpoint", err)
		return
	}
	writeJSON(w, http.StatusCreated, serviceEndpointToOut(serviceEndpointFromCreateRow(endpoint), service, project, org))
}
