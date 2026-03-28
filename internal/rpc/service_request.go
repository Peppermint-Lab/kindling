package rpc

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

type serviceRequestContext struct {
	ID      pgtype.UUID
	Service queries.Service
	Project queries.Project
}

func (a *API) serviceForRequest(w http.ResponseWriter, r *http.Request, orgID pgtype.UUID) (serviceRequestContext, bool) {
	serviceID, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid service id")
		return serviceRequestContext{}, false
	}
	service, err := a.q.ServiceFirstByIDAndOrg(r.Context(), queries.ServiceFirstByIDAndOrgParams{
		ID:    serviceID,
		OrgID: orgID,
	})
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "service not found")
		return serviceRequestContext{}, false
	}
	project, err := a.q.ProjectFirstByID(r.Context(), service.ProjectID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "service_project", err)
		return serviceRequestContext{}, false
	}
	return serviceRequestContext{
		ID:      serviceID,
		Service: service,
		Project: project,
	}, true
}
