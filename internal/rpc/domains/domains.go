// Package domains provides custom domain management API handlers.
package domains

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/rpc/rpcutil"
)

const dnsLookupTimeout = 15 * time.Second // timeout for DNS TXT challenge verification

const kindlingChallengePrefix = "_kindling-challenge."

// Handler provides custom domain API handlers.
type Handler struct {
	Q *queries.Queries
}

// RegisterRoutes mounts custom domain routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects/{id}/domains", h.listProjectDomains)
	mux.HandleFunc("POST /api/projects/{id}/domains", h.createProjectDomain)
	mux.HandleFunc("DELETE /api/projects/{id}/domains/{domain_id}", h.deleteProjectDomain)
	mux.HandleFunc("POST /api/projects/{id}/domains/{domain_id}/verify", h.verifyProjectDomain)
}

type dnsChallengeOut struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

type projectDomainOut struct {
	ID           string           `json:"id"`
	DomainName   string           `json:"domain_name"`
	VerifiedAt   *string          `json:"verified_at,omitempty"`
	DeploymentID *string          `json:"deployment_id,omitempty"`
	DNSChallenge *dnsChallengeOut `json:"dns_challenge,omitempty"`
	Instructions string           `json:"instructions,omitempty"`
}

func domainChallengeRecordName(domain string) string {
	return kindlingChallengePrefix + domain
}

// NormalizeDomainName validates and normalises a domain name.
func NormalizeDomainName(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return "", errors.New("domain name is required")
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return "", errors.New("use a fully qualified hostname (e.g. app.example.com)")
	}
	for _, p := range parts {
		if p == "" || len(p) > 63 {
			return "", errors.New("invalid domain label")
		}
		for _, c := range p {
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
				continue
			}
			return "", errors.New("domain must be ASCII letters, digits, and hyphens per label")
		}
		if strings.HasPrefix(p, "-") || strings.HasSuffix(p, "-") {
			return "", errors.New("domain labels cannot start or end with a hyphen")
		}
	}
	return s, nil
}

func newVerificationToken() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func challengeTXTFound(ctx context.Context, recordName, want string) (bool, error) {
	resolver := net.DefaultResolver
	ctx, cancel := context.WithTimeout(ctx, dnsLookupTimeout)
	defer cancel()
	txts, err := resolver.LookupTXT(ctx, recordName)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return false, nil
		}
		return false, err
	}
	want = strings.TrimSpace(want)
	for _, t := range txts {
		if strings.TrimSpace(t) == want {
			return true, nil
		}
	}
	return false, nil
}

func domainToOut(d queries.Domain) projectDomainOut {
	out := projectDomainOut{
		ID:           uuid.UUID(d.ID.Bytes).String(),
		DomainName:   d.DomainName,
		DeploymentID: nil,
	}
	if d.DeploymentID.Valid {
		s := uuid.UUID(d.DeploymentID.Bytes).String()
		out.DeploymentID = &s
	}
	if d.VerifiedAt.Valid {
		ts := d.VerifiedAt.Time.UTC().Format(time.RFC3339Nano)
		out.VerifiedAt = &ts
		return out
	}
	if d.VerificationToken != "" {
		name := domainChallengeRecordName(d.DomainName)
		out.DNSChallenge = &dnsChallengeOut{
			Type:  "TXT",
			Name:  name,
			Value: d.VerificationToken,
		}
	}
	return out
}

// IsPgUniqueViolation checks if an error is a Postgres unique constraint violation.
func IsPgUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (h *Handler) listProjectDomains(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	projectID, err := rpcutil.ParseUUID(r.PathValue("id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	if _, err := h.Q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    projectID,
		OrgID: p.OrganizationID,
	}); err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	rows, err := h.Q.DomainListByProjectID(r.Context(), projectID)
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "list_domains", err)
		return
	}
	out := make([]projectDomainOut, 0, len(rows))
	for _, d := range rows {
		out = append(out, domainToOut(d))
	}
	rpcutil.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) createProjectDomain(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	if !rpcutil.RequireOrgAdmin(w, p) {
		return
	}
	projectID, err := rpcutil.ParseUUID(r.PathValue("id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	if _, err := h.Q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    projectID,
		OrgID: p.OrganizationID,
	}); err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	var req struct {
		DomainName string `json:"domain_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	domainName, err := NormalizeDomainName(req.DomainName)
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_domain", err.Error())
		return
	}
	token, err := newVerificationToken()
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "token_generation", err)
		return
	}
	d, err := h.Q.DomainCreate(r.Context(), queries.DomainCreateParams{
		ID:                pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ProjectID:         projectID,
		DomainName:        domainName,
		VerificationToken: token,
	})
	if err != nil {
		if IsPgUniqueViolation(err) {
			rpcutil.WriteAPIError(w, http.StatusConflict, "domain_taken", "that domain is already registered")
			return
		}
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "create_domain", err)
		return
	}
	rpcutil.WriteJSON(w, http.StatusCreated, domainToOut(d))
}

func (h *Handler) deleteProjectDomain(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	if !rpcutil.RequireOrgAdmin(w, p) {
		return
	}
	projectID, err := rpcutil.ParseUUID(r.PathValue("id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	if _, err := h.Q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    projectID,
		OrgID: p.OrganizationID,
	}); err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	domainID, err := rpcutil.ParseUUID(r.PathValue("domain_id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_domain_id", "invalid domain id")
		return
	}
	if _, err := h.Q.DomainFirstByIDAndProject(r.Context(), queries.DomainFirstByIDAndProjectParams{
		ID:        domainID,
		ProjectID: projectID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "domain not found")
			return
		}
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "domain_lookup", err)
		return
	}
	if err := h.Q.DomainDelete(r.Context(), queries.DomainDeleteParams{
		ID:        domainID,
		ProjectID: projectID,
	}); err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "delete_domain", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) verifyProjectDomain(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	if !rpcutil.RequireOrgAdmin(w, p) {
		return
	}
	projectID, err := rpcutil.ParseUUID(r.PathValue("id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	if _, err := h.Q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    projectID,
		OrgID: p.OrganizationID,
	}); err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	domainID, err := rpcutil.ParseUUID(r.PathValue("domain_id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_domain_id", "invalid domain id")
		return
	}
	d, err := h.Q.DomainFirstByIDAndProject(r.Context(), queries.DomainFirstByIDAndProjectParams{
		ID:        domainID,
		ProjectID: projectID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "domain not found")
			return
		}
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "domain_lookup", err)
		return
	}
	if d.VerifiedAt.Valid {
		rpcutil.WriteJSON(w, http.StatusOK, domainToOut(d))
		return
	}
	if d.VerificationToken == "" {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "not_pending", "domain has no pending verification")
		return
	}
	recordName := domainChallengeRecordName(d.DomainName)
	txtOk, err := challengeTXTFound(r.Context(), recordName, d.VerificationToken)
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "dns_lookup_failed", "could not resolve DNS: "+err.Error())
		return
	}
	if !txtOk {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "verification_failed", "TXT record "+recordName+" not found or value does not match")
		return
	}
	d, err = h.Q.DomainSetVerified(r.Context(), queries.DomainSetVerifiedParams{
		ID:        domainID,
		ProjectID: projectID,
	})
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "verify_domain", err)
		return
	}
	// Point all project domains at the current running deployment so the edge can route.
	if dep, err := h.Q.DeploymentLatestRunningByProjectID(r.Context(), projectID); err == nil {
		if dep.RunningAt.Valid {
			_ = h.Q.DomainUpdateDeploymentForProject(r.Context(), queries.DomainUpdateDeploymentForProjectParams{
				DeploymentID: dep.ID,
				ProjectID:    projectID,
			})
			if dFresh, err := h.Q.DomainFirstByIDAndProject(r.Context(), queries.DomainFirstByIDAndProjectParams{
				ID:        domainID,
				ProjectID: projectID,
			}); err == nil {
				d = dFresh
			}
		}
	}
	rpcutil.WriteJSON(w, http.StatusOK, domainToOut(d))
}
