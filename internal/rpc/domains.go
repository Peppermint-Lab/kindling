package rpc

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
)

const kindlingChallengePrefix = "_kindling-challenge."

type dnsChallengeOut struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

type projectDomainOut struct {
	ID            string            `json:"id"`
	DomainName    string            `json:"domain_name"`
	VerifiedAt    *string           `json:"verified_at,omitempty"`
	DeploymentID  *string           `json:"deployment_id,omitempty"`
	DNSChallenge  *dnsChallengeOut  `json:"dns_challenge,omitempty"`
	Instructions  string            `json:"instructions,omitempty"`
}

func domainChallengeRecordName(domain string) string {
	return kindlingChallengePrefix + domain
}

func normalizeDomainName(raw string) (string, error) {
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
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
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

func isPgUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (a *API) listProjectDomains(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	if _, err := a.q.ProjectFirstByID(r.Context(), projectID); err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	rows, err := a.q.DomainListByProjectID(r.Context(), projectID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_domains", err)
		return
	}
	out := make([]projectDomainOut, 0, len(rows))
	for _, d := range rows {
		out = append(out, domainToOut(d))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) createProjectDomain(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	if _, err := a.q.ProjectFirstByID(r.Context(), projectID); err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	var req struct {
		DomainName string `json:"domain_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	domainName, err := normalizeDomainName(req.DomainName)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_domain", err.Error())
		return
	}
	token, err := newVerificationToken()
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "token_generation", err)
		return
	}
	d, err := a.q.DomainCreate(r.Context(), queries.DomainCreateParams{
		ID:                pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ProjectID:         projectID,
		DomainName:        domainName,
		VerificationToken: token,
	})
	if err != nil {
		if isPgUniqueViolation(err) {
			writeAPIError(w, http.StatusConflict, "domain_taken", "that domain is already registered")
			return
		}
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "create_domain", err)
		return
	}
	writeJSON(w, http.StatusCreated, domainToOut(d))
}

func (a *API) deleteProjectDomain(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	domainID, err := parseUUID(r.PathValue("domain_id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_domain_id", "invalid domain id")
		return
	}
	if _, err := a.q.DomainFirstByIDAndProject(r.Context(), queries.DomainFirstByIDAndProjectParams{
		ID:        domainID,
		ProjectID: projectID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "domain not found")
			return
		}
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "domain_lookup", err)
		return
	}
	if err := a.q.DomainDelete(r.Context(), queries.DomainDeleteParams{
		ID:        domainID,
		ProjectID: projectID,
	}); err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "delete_domain", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) verifyProjectDomain(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	domainID, err := parseUUID(r.PathValue("domain_id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_domain_id", "invalid domain id")
		return
	}
	d, err := a.q.DomainFirstByIDAndProject(r.Context(), queries.DomainFirstByIDAndProjectParams{
		ID:        domainID,
		ProjectID: projectID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "domain not found")
			return
		}
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "domain_lookup", err)
		return
	}
	if d.VerifiedAt.Valid {
		writeJSON(w, http.StatusOK, domainToOut(d))
		return
	}
	if d.VerificationToken == "" {
		writeAPIError(w, http.StatusBadRequest, "not_pending", "domain has no pending verification")
		return
	}
	recordName := domainChallengeRecordName(d.DomainName)
	ok, err := challengeTXTFound(r.Context(), recordName, d.VerificationToken)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "dns_lookup_failed", "could not resolve DNS: "+err.Error())
		return
	}
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "verification_failed", "TXT record "+recordName+" not found or value does not match")
		return
	}
	d, err = a.q.DomainSetVerified(r.Context(), queries.DomainSetVerifiedParams{
		ID:        domainID,
		ProjectID: projectID,
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "verify_domain", err)
		return
	}
	// Point all project domains at the current running deployment so the edge can route.
	if dep, err := a.q.DeploymentLatestRunningByProjectID(r.Context(), projectID); err == nil {
		if dep.RunningAt.Valid {
			_ = a.q.DomainUpdateDeploymentForProject(r.Context(), queries.DomainUpdateDeploymentForProjectParams{
				DeploymentID: dep.ID,
				ProjectID:    projectID,
			})
			if dFresh, err := a.q.DomainFirstByIDAndProject(r.Context(), queries.DomainFirstByIDAndProjectParams{
				ID:        domainID,
				ProjectID: projectID,
			}); err == nil {
				d = dFresh
			}
		}
	}
	writeJSON(w, http.StatusOK, domainToOut(d))
}
