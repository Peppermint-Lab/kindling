package workerdns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/shared/netnames"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
	"github.com/miekg/dns"
)

const (
	privateDomainSuffix = "kindling.internal."
	answerTTLSeconds    = 5
)

type Resolution struct {
	Handled bool
	Rcode   int
	Answers []dns.RR
}

type callerIdentity struct {
	OrganizationID        uuid.UUID
	ProjectID             uuid.UUID
	EnvSlug               string
	PreviewEnvironmentID  uuid.UUID
	HasPreviewEnvironment bool
}

type endpointCandidate struct {
	ServiceID      uuid.UUID
	ProjectID      uuid.UUID
	OrganizationID uuid.UUID
	ProjectName    string
}

type previewEnvironmentRef struct {
	ID uuid.UUID
}

type lookupStore interface {
	LookupCallerByIP(ctx context.Context, ip netip.Addr) (callerIdentity, bool, error)
	LookupEndpointCandidates(ctx context.Context, endpoint, service, org string) ([]endpointCandidate, error)
	LookupPreviewEnvironmentsByProjectAndPR(ctx context.Context, projectID uuid.UUID, prNumber int32) ([]previewEnvironmentRef, error)
	LookupLatestRunningProductionDeployment(ctx context.Context, serviceID uuid.UUID) (uuid.UUID, bool, error)
	LookupLatestRunningPreviewDeployment(ctx context.Context, serviceID, previewEnvironmentID uuid.UUID) (uuid.UUID, bool, error)
	LookupRunningBackendIPs(ctx context.Context, deploymentID uuid.UUID) ([]netip.Addr, error)
}

type Resolver struct {
	store lookupStore
}

func NewResolver(q *queries.Queries) *Resolver {
	return &Resolver{store: &queryStore{q: q}}
}

func NewResolverWithStore(store lookupStore) *Resolver {
	return &Resolver{store: store}
}

func (r *Resolver) Resolve(ctx context.Context, sourceIP netip.Addr, qname string, qtype uint16) (Resolution, error) {
	target, ok := parseInternalName(qname)
	if !ok {
		return Resolution{}, nil
	}

	caller, ok, err := r.store.LookupCallerByIP(ctx, sourceIP)
	if err != nil {
		return Resolution{}, fmt.Errorf("lookup caller: %w", err)
	}
	if !ok {
		return Resolution{Handled: true, Rcode: dns.RcodeNameError}, nil
	}

	candidates, err := r.store.LookupEndpointCandidates(ctx, target.Endpoint, target.Service, target.Organization)
	if err != nil {
		return Resolution{}, fmt.Errorf("lookup endpoint candidates: %w", err)
	}
	candidate, ok := selectEndpointCandidate(candidates, target.Project)
	if !ok {
		return Resolution{Handled: true, Rcode: dns.RcodeNameError}, nil
	}

	if caller.OrganizationID != candidate.OrganizationID {
		return Resolution{Handled: true, Rcode: dns.RcodeNameError}, nil
	}
	if caller.EnvSlug != target.Environment {
		return Resolution{Handled: true, Rcode: dns.RcodeNameError}, nil
	}

	deploymentID, ok, err := r.resolveDeployment(ctx, caller, candidate, target.Environment)
	if err != nil {
		return Resolution{}, err
	}
	if !ok {
		return Resolution{Handled: true, Rcode: dns.RcodeServerFailure}, nil
	}

	ips, err := r.store.LookupRunningBackendIPs(ctx, deploymentID)
	if err != nil {
		return Resolution{}, fmt.Errorf("lookup backend ips: %w", err)
	}
	if len(ips) == 0 {
		return Resolution{Handled: true, Rcode: dns.RcodeServerFailure}, nil
	}

	switch qtype {
	case dns.TypeA, dns.TypeANY:
		answers := answersForIPv4(qname, ips)
		if len(answers) == 0 {
			return Resolution{Handled: true, Rcode: dns.RcodeServerFailure}, nil
		}
		return Resolution{Handled: true, Rcode: dns.RcodeSuccess, Answers: answers}, nil
	case dns.TypeAAAA:
		return Resolution{Handled: true, Rcode: dns.RcodeSuccess}, nil
	default:
		return Resolution{Handled: true, Rcode: dns.RcodeSuccess}, nil
	}
}

func (r *Resolver) resolveDeployment(
	ctx context.Context,
	caller callerIdentity,
	candidate endpointCandidate,
	envSlug string,
) (uuid.UUID, bool, error) {
	if envSlug == "prod" {
		return r.store.LookupLatestRunningProductionDeployment(ctx, candidate.ServiceID)
	}

	prNumber, ok := parsePreviewEnvSlug(envSlug)
	if !ok || !caller.HasPreviewEnvironment {
		return uuid.Nil, false, nil
	}

	envs, err := r.store.LookupPreviewEnvironmentsByProjectAndPR(ctx, candidate.ProjectID, prNumber)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("lookup preview environments: %w", err)
	}
	var previewEnvironmentID uuid.UUID
	for _, env := range envs {
		if env.ID == caller.PreviewEnvironmentID {
			previewEnvironmentID = env.ID
			break
		}
	}
	if previewEnvironmentID == uuid.Nil {
		return uuid.Nil, false, nil
	}

	return r.store.LookupLatestRunningPreviewDeployment(ctx, candidate.ServiceID, previewEnvironmentID)
}

type internalName struct {
	Endpoint     string
	Service      string
	Project      string
	Environment  string
	Organization string
}

func parseInternalName(qname string) (internalName, bool) {
	fqdn := dns.Fqdn(strings.TrimSpace(strings.ToLower(qname)))
	if !strings.HasSuffix(fqdn, privateDomainSuffix) {
		return internalName{}, false
	}
	trimmed := strings.TrimSuffix(fqdn, ".")
	parts := strings.Split(trimmed, ".")
	if len(parts) != 7 {
		return internalName{}, true
	}
	for _, part := range parts[:5] {
		if strings.TrimSpace(part) == "" {
			return internalName{}, true
		}
	}
	return internalName{
		Endpoint:     parts[0],
		Service:      parts[1],
		Project:      parts[2],
		Environment:  parts[3],
		Organization: parts[4],
	}, true
}

func selectEndpointCandidate(candidates []endpointCandidate, normalizedProject string) (endpointCandidate, bool) {
	var matched endpointCandidate
	matchCount := 0
	for _, candidate := range candidates {
		if netnames.NormalizeLabel(candidate.ProjectName) != normalizedProject {
			continue
		}
		matched = candidate
		matchCount++
	}
	return matched, matchCount == 1
}

func parsePreviewEnvSlug(envSlug string) (int32, bool) {
	if !strings.HasPrefix(envSlug, "pr-") {
		return 0, false
	}
	raw := strings.TrimPrefix(envSlug, "pr-")
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, false
	}
	return int32(n), true
}

func answersForIPv4(qname string, ips []netip.Addr) []dns.RR {
	name := dns.Fqdn(strings.TrimSpace(qname))
	answers := make([]dns.RR, 0, len(ips))
	for _, ip := range ips {
		if !ip.Is4() {
			continue
		}
		answers = append(answers, &dns.A{
			Hdr: dns.RR_Header{
				Name:   name,
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    answerTTLSeconds,
			},
			A: net.IP(ip.AsSlice()),
		})
	}
	return answers
}

type queryStore struct {
	q *queries.Queries
}

func (s *queryStore) LookupCallerByIP(ctx context.Context, ip netip.Addr) (callerIdentity, bool, error) {
	vm, err := s.q.VMFindByIPAddress(ctx, ip)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return callerIdentity{}, false, nil
		}
		return callerIdentity{}, false, err
	}
	dep, err := s.q.DeploymentFindByVMID(ctx, pguuid.ToPgtype(pguuid.FromPgtype(vm.ID)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return callerIdentity{}, false, nil
		}
		return callerIdentity{}, false, err
	}
	project, err := s.q.ProjectFirstByID(ctx, dep.ProjectID)
	if err != nil {
		return callerIdentity{}, false, err
	}
	org, err := s.q.OrganizationByID(ctx, project.OrgID)
	if err != nil {
		return callerIdentity{}, false, err
	}

	identity := callerIdentity{
		OrganizationID: pguuid.FromPgtype(org.ID),
		ProjectID:      pguuid.FromPgtype(project.ID),
		EnvSlug:        "prod",
	}
	if strings.EqualFold(dep.DeploymentKind, "preview") {
		if !dep.PreviewEnvironmentID.Valid {
			return callerIdentity{}, false, nil
		}
		previewEnvironment, err := s.q.PreviewEnvironmentByID(ctx, dep.PreviewEnvironmentID)
		if err != nil {
			return callerIdentity{}, false, err
		}
		identity.HasPreviewEnvironment = true
		identity.PreviewEnvironmentID = pguuid.FromPgtype(previewEnvironment.ID)
		if previewEnvironment.PrNumber > 0 {
			identity.EnvSlug = fmt.Sprintf("pr-%d", previewEnvironment.PrNumber)
		} else {
			identity.EnvSlug = "preview"
		}
	}
	return identity, true, nil
}

func (s *queryStore) LookupEndpointCandidates(ctx context.Context, endpoint, service, org string) ([]endpointCandidate, error) {
	rows, err := s.q.ServiceEndpointDNSLookupCandidates(ctx, queries.ServiceEndpointDNSLookupCandidatesParams{
		Lower:  endpoint,
		Slug:   service,
		Slug_2: org,
	})
	if err != nil {
		return nil, err
	}
	out := make([]endpointCandidate, 0, len(rows))
	for _, row := range rows {
		out = append(out, endpointCandidate{
			ServiceID:      pguuid.FromPgtype(row.ServiceID),
			ProjectID:      pguuid.FromPgtype(row.ProjectID),
			OrganizationID: pguuid.FromPgtype(row.OrganizationID),
			ProjectName:    row.ProjectName,
		})
	}
	return out, nil
}

func (s *queryStore) LookupPreviewEnvironmentsByProjectAndPR(ctx context.Context, projectID uuid.UUID, prNumber int32) ([]previewEnvironmentRef, error) {
	rows, err := s.q.PreviewEnvironmentsByProjectAndPRNumber(ctx, queries.PreviewEnvironmentsByProjectAndPRNumberParams{
		ProjectID: pguuid.ToPgtype(projectID),
		PrNumber:  prNumber,
	})
	if err != nil {
		return nil, err
	}
	out := make([]previewEnvironmentRef, 0, len(rows))
	for _, row := range rows {
		out = append(out, previewEnvironmentRef{ID: pguuid.FromPgtype(row.ID)})
	}
	return out, nil
}

func (s *queryStore) LookupLatestRunningProductionDeployment(ctx context.Context, serviceID uuid.UUID) (uuid.UUID, bool, error) {
	dep, err := s.q.DeploymentLatestRunningByServiceID(ctx, pguuid.ToPgtype(serviceID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, false, nil
		}
		return uuid.Nil, false, err
	}
	return pguuid.FromPgtype(dep.ID), true, nil
}

func (s *queryStore) LookupLatestRunningPreviewDeployment(ctx context.Context, serviceID, previewEnvironmentID uuid.UUID) (uuid.UUID, bool, error) {
	dep, err := s.q.DeploymentLatestRunningPreviewByServiceAndPreviewEnvironmentID(ctx, queries.DeploymentLatestRunningPreviewByServiceAndPreviewEnvironmentIDParams{
		ServiceID:            pguuid.ToPgtype(serviceID),
		PreviewEnvironmentID: pguuid.ToPgtype(previewEnvironmentID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, false, nil
		}
		return uuid.Nil, false, err
	}
	return pguuid.FromPgtype(dep.ID), true, nil
}

func (s *queryStore) LookupRunningBackendIPs(ctx context.Context, deploymentID uuid.UUID) ([]netip.Addr, error) {
	return s.q.DeploymentRunningBackendIPs(ctx, pguuid.ToPgtype(deploymentID))
}
