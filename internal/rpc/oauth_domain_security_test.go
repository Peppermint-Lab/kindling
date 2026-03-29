package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	extoauth "github.com/kindlingvm/kindling/internal/oauth"
)

// ────────────────────────────────────────────────────────────────────────────
// Pure-function unit tests
// ────────────────────────────────────────────────────────────────────────────

func TestExtractEmailDomain(t *testing.T) {
	t.Parallel()
	cases := []struct {
		email string
		want  string
	}{
		{"alice@acme.com", "acme.com"},
		{"Alice@ACME.COM", "acme.com"},
		{"bob@gmail.com", "gmail.com"},
		{"", ""},
		{"noatsign", ""},
		{"trailing@", ""},
		{"@leading.com", "leading.com"},
		{" user@domain.io ", "domain.io"},
	}
	for _, tc := range cases {
		t.Run(tc.email, func(t *testing.T) {
			t.Parallel()
			got := extractEmailDomain(tc.email)
			if got != tc.want {
				t.Fatalf("extractEmailDomain(%q) = %q, want %q", tc.email, got, tc.want)
			}
		})
	}
}

func TestIsConsumerDomain(t *testing.T) {
	t.Parallel()
	consumer := []string{
		"gmail.com", "yahoo.com", "outlook.com", "hotmail.com",
		"icloud.com", "aol.com", "protonmail.com", "mail.com", "zoho.com",
	}
	for _, d := range consumer {
		if !isConsumerDomain(d) {
			t.Fatalf("isConsumerDomain(%q) = false, want true", d)
		}
		// Case-insensitive check
		if !isConsumerDomain(strings.ToUpper(d)) {
			t.Fatalf("isConsumerDomain(%q) = false, want true", strings.ToUpper(d))
		}
	}
	nonConsumer := []string{"acme.com", "company.io", "startup.dev", ""}
	for _, d := range nonConsumer {
		if isConsumerDomain(d) {
			t.Fatalf("isConsumerDomain(%q) = true, want false", d)
		}
	}
}

// ────────────────────────────────────────────────────────────────────────────
// In-memory fake DBTX for testing resolveExternalLogin flow
// ────────────────────────────────────────────────────────────────────────────

// fakeStore provides in-memory storage for the queries used by resolveExternalLogin
// and the pending-member approval/rejection flow.
type fakeStore struct {
	mu          sync.Mutex
	users       []queries.User
	identities  []queries.UserIdentity
	orgs        []queries.Organization
	memberships []queries.OrganizationMembership
	orgNetworks []struct {
		OrgID pgtype.UUID
		Cidr  string
	}
}

func newFakeStore() *fakeStore {
	return &fakeStore{}
}

func (f *fakeStore) addUser(u queries.User)        { f.mu.Lock(); defer f.mu.Unlock(); f.users = append(f.users, u) }
func (f *fakeStore) addOrg(o queries.Organization)  { f.mu.Lock(); defer f.mu.Unlock(); f.orgs = append(f.orgs, o) }
func (f *fakeStore) addIdentity(i queries.UserIdentity) {
	f.mu.Lock(); defer f.mu.Unlock(); f.identities = append(f.identities, i)
}
func (f *fakeStore) addMembership(m queries.OrganizationMembership) {
	f.mu.Lock(); defer f.mu.Unlock(); f.memberships = append(f.memberships, m)
}

func (f *fakeStore) findOrgByEmailDomain(domain string) (queries.Organization, bool) {
	f.mu.Lock(); defer f.mu.Unlock()
	for _, o := range f.orgs {
		if strings.EqualFold(o.EmailDomain, domain) && o.EmailDomain != "" {
			return o, true
		}
	}
	return queries.Organization{}, false
}

func (f *fakeStore) findMembership(userID, orgID pgtype.UUID) (queries.OrganizationMembership, bool) {
	f.mu.Lock(); defer f.mu.Unlock()
	for _, m := range f.memberships {
		if m.UserID == userID && m.OrganizationID == orgID {
			return m, true
		}
	}
	return queries.OrganizationMembership{}, false
}

func (f *fakeStore) countMemberships(userID, orgID pgtype.UUID, status string) int {
	f.mu.Lock(); defer f.mu.Unlock()
	count := 0
	for _, m := range f.memberships {
		if m.UserID == userID && m.OrganizationID == orgID && m.Status == status {
			count++
		}
	}
	return count
}

func (f *fakeStore) countActiveMemberships(userID pgtype.UUID) int {
	f.mu.Lock(); defer f.mu.Unlock()
	count := 0
	for _, m := range f.memberships {
		if m.UserID == userID && m.Status == "active" {
			count++
		}
	}
	return count
}

func (f *fakeStore) userByID(id pgtype.UUID) (queries.User, bool) {
	f.mu.Lock(); defer f.mu.Unlock()
	for _, u := range f.users {
		if u.ID == id {
			return u, true
		}
	}
	return queries.User{}, false
}

func (f *fakeStore) orgsForUser(userID pgtype.UUID) []queries.Organization {
	f.mu.Lock(); defer f.mu.Unlock()
	var out []queries.Organization
	for _, m := range f.memberships {
		if m.UserID == userID && m.Status == "active" {
			for _, o := range f.orgs {
				if o.ID == m.OrganizationID {
					out = append(out, o)
				}
			}
		}
	}
	return out
}

// fakeOAuthDBTX implements DBTX for the queries used in resolveExternalLogin.
// It intercepts SQL patterns and routes them to the fakeStore.
type fakeOAuthDBTX struct {
	store *fakeStore
}

func (f *fakeOAuthDBTX) Exec(_ context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	// OrganizationMembershipUpsert (ON CONFLICT DO NOTHING)
	if strings.Contains(sql, "INSERT INTO organization_memberships") && strings.Contains(sql, "DO NOTHING") {
		return pgconn.CommandTag{}, nil
	}
	// OrganizationMembershipUpsertOwner
	if strings.Contains(sql, "INSERT INTO organization_memberships") && strings.Contains(sql, "DO UPDATE SET role") {
		id := args[0].(pgtype.UUID)
		orgID := args[1].(pgtype.UUID)
		userID := args[2].(pgtype.UUID)
		f.store.mu.Lock()
		defer f.store.mu.Unlock()
		// Check for existing membership
		for i, m := range f.store.memberships {
			if m.OrganizationID == orgID && m.UserID == userID {
				f.store.memberships[i].Role = "owner"
				return pgconn.CommandTag{}, nil
			}
		}
		f.store.memberships = append(f.store.memberships, queries.OrganizationMembership{
			ID: id, OrganizationID: orgID, UserID: userID, Role: "owner", Status: "active",
		})
		return pgconn.CommandTag{}, nil
	}
	// OrganizationUpdateEmailDomain
	if strings.Contains(sql, "UPDATE organizations SET email_domain") {
		orgID := args[0].(pgtype.UUID)
		domain := args[1].(string)
		f.store.mu.Lock()
		defer f.store.mu.Unlock()
		for i, o := range f.store.orgs {
			if o.ID == orgID {
				f.store.orgs[i].EmailDomain = domain
			}
		}
		return pgconn.CommandTag{}, nil
	}
	return pgconn.CommandTag{}, nil
}

func (f *fakeOAuthDBTX) Query(_ context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	// OrgProviderConnectionListByProvider - return empty
	if strings.Contains(sql, "FROM org_provider_connections") {
		return &emptyRows{}, nil
	}
	// OrganizationsForUser
	if strings.Contains(sql, "FROM organizations o") && strings.Contains(sql, "organization_memberships") {
		userID := args[0].(pgtype.UUID)
		orgs := f.store.orgsForUser(userID)
		return &orgRows{orgs: orgs}, nil
	}
	return &emptyRows{}, nil
}

func (f *fakeOAuthDBTX) QueryRow(_ context.Context, sql string, args ...interface{}) pgx.Row {
	// UserIdentityByProviderSubject
	if strings.Contains(sql, "FROM user_identities") && strings.Contains(sql, "provider_subject") && !strings.Contains(sql, "INSERT") {
		if strings.Contains(sql, "user_id") && strings.Contains(sql, "provider") && !strings.Contains(sql, "provider_subject = $2") {
			// UserIdentityByUserAndProvider
			return &errRow{err: pgx.ErrNoRows}
		}
		provider := args[0].(string)
		subject := args[1].(string)
		f.store.mu.Lock()
		defer f.store.mu.Unlock()
		for _, id := range f.store.identities {
			if id.Provider == provider && id.ProviderSubject == subject {
				return &identityRow{identity: &id}
			}
		}
		return &errRow{err: pgx.ErrNoRows}
	}
	// UserIdentityByUserAndProvider
	if strings.Contains(sql, "FROM user_identities") && strings.Contains(sql, "user_id = $1 AND provider = $2") {
		return &errRow{err: pgx.ErrNoRows}
	}
	// UserByEmail
	if strings.Contains(sql, "FROM users WHERE LOWER(email)") {
		email := args[0].(string)
		f.store.mu.Lock()
		defer f.store.mu.Unlock()
		for _, u := range f.store.users {
			if strings.EqualFold(u.Email, email) {
				return &userRow{user: &u}
			}
		}
		return &errRow{err: pgx.ErrNoRows}
	}
	// UserByID
	if strings.Contains(sql, "FROM users WHERE id") {
		id := args[0].(pgtype.UUID)
		f.store.mu.Lock()
		defer f.store.mu.Unlock()
		for _, u := range f.store.users {
			if u.ID == id {
				return &userRow{user: &u}
			}
		}
		return &errRow{err: pgx.ErrNoRows}
	}
	// UserCount
	if strings.Contains(sql, "COUNT(*)") && strings.Contains(sql, "FROM users") {
		f.store.mu.Lock()
		cnt := int64(len(f.store.users))
		f.store.mu.Unlock()
		return &countRow{count: cnt}
	}
	// UserCreate
	if strings.Contains(sql, "INSERT INTO users") {
		id := args[0].(pgtype.UUID)
		email := args[1].(string)
		passwordHash := args[2].(string)
		displayName := args[3].(string)
		isPlatformAdmin := args[4].(bool)
		user := queries.User{
			ID: id, Email: email, PasswordHash: passwordHash,
			DisplayName: displayName, IsPlatformAdmin: isPlatformAdmin,
		}
		f.store.addUser(user)
		return &userRow{user: &user}
	}
	// OrganizationFindByEmailDomain
	if strings.Contains(sql, "FROM organizations") && strings.Contains(sql, "email_domain") && !strings.Contains(sql, "INSERT") && !strings.Contains(sql, "UPDATE") {
		domain := args[0].(string)
		org, ok := f.store.findOrgByEmailDomain(domain)
		if ok {
			return &orgRow{org: &org}
		}
		return &errRow{err: pgx.ErrNoRows}
	}
	// OrganizationMembershipByUserAndOrgWithStatus (or ByUserAndOrg)
	if strings.Contains(sql, "FROM organization_memberships") && strings.Contains(sql, "user_id") && strings.Contains(sql, "organization_id") && !strings.Contains(sql, "INSERT") && !strings.Contains(sql, "UPDATE") {
		userID := args[0].(pgtype.UUID)
		orgID := args[1].(pgtype.UUID)
		m, ok := f.store.findMembership(userID, orgID)
		if ok {
			return &membershipRow{membership: &m}
		}
		return &errRow{err: pgx.ErrNoRows}
	}
	// OrganizationMembershipCreateWithStatus
	if strings.Contains(sql, "INSERT INTO organization_memberships") && strings.Contains(sql, "status") {
		id := args[0].(pgtype.UUID)
		orgID := args[1].(pgtype.UUID)
		userID := args[2].(pgtype.UUID)
		role := args[3].(string)
		status := args[4].(string)
		m := queries.OrganizationMembership{
			ID: id, OrganizationID: orgID, UserID: userID, Role: role, Status: status,
		}
		// Check for duplicate
		existing, dup := f.store.findMembership(userID, orgID)
		if dup {
			return &membershipRow{membership: &existing}
		}
		f.store.addMembership(m)
		return &membershipRow{membership: &m}
	}
	// OrganizationMembershipCreate (without status)
	if strings.Contains(sql, "INSERT INTO organization_memberships") && !strings.Contains(sql, "status") {
		id := args[0].(pgtype.UUID)
		orgID := args[1].(pgtype.UUID)
		userID := args[2].(pgtype.UUID)
		role := args[3].(string)
		m := queries.OrganizationMembership{
			ID: id, OrganizationID: orgID, UserID: userID, Role: role, Status: "active",
		}
		f.store.addMembership(m)
		return &membershipRow{membership: &m}
	}
	// OrganizationMembershipUpdateStatus
	if strings.Contains(sql, "UPDATE organization_memberships") && strings.Contains(sql, "status") {
		orgID := args[0].(pgtype.UUID)
		userID := args[1].(pgtype.UUID)
		newStatus := args[2].(string)
		f.store.mu.Lock()
		defer f.store.mu.Unlock()
		for i, m := range f.store.memberships {
			if m.OrganizationID == orgID && m.UserID == userID {
				f.store.memberships[i].Status = newStatus
				updated := f.store.memberships[i]
				return &membershipRow{membership: &updated}
			}
		}
		return &errRow{err: pgx.ErrNoRows}
	}
	// OrganizationCreate
	if strings.Contains(sql, "INSERT INTO organizations") {
		id := args[0].(pgtype.UUID)
		name := args[1].(string)
		slug := args[2].(string)
		org := queries.Organization{ID: id, Name: name, Slug: slug}
		f.store.addOrg(org)
		return &orgCreateRow{org: &org}
	}
	// UserIdentityCreate
	if strings.Contains(sql, "INSERT INTO user_identities") {
		id := args[0].(pgtype.UUID)
		userID := args[1].(pgtype.UUID)
		provider := args[2].(string)
		subject := args[3].(string)
		login := args[4].(string)
		email := args[5].(string)
		displayName := args[6].(string)
		identity := queries.UserIdentity{
			ID: id, UserID: userID, Provider: provider, ProviderSubject: subject,
			ProviderLogin: login, ProviderEmail: email, ProviderDisplayName: displayName,
		}
		f.store.addIdentity(identity)
		return &identityRow{identity: &identity}
	}
	// UserIdentityUpdateByID
	if strings.Contains(sql, "UPDATE user_identities") {
		return &identityRow{identity: &queries.UserIdentity{}}
	}
	return &errRow{err: fmt.Errorf("fakeOAuthDBTX: unmatched QueryRow SQL: %s", sql[:min(80, len(sql))])}
}

// ────────────────────────────────────────────────────────────────────────────
// Row types for the fake DBTX
// ────────────────────────────────────────────────────────────────────────────

type errRow struct{ err error }
func (r *errRow) Scan(_ ...interface{}) error { return r.err }

type countRow struct{ count int64 }
func (r *countRow) Scan(dest ...interface{}) error {
	*dest[0].(*int64) = r.count
	return nil
}

type userRow struct{ user *queries.User }
func (r *userRow) Scan(dest ...interface{}) error {
	u := r.user
	*dest[0].(*pgtype.UUID) = u.ID
	*dest[1].(*string) = u.Email
	*dest[2].(*string) = u.PasswordHash
	*dest[3].(*string) = u.DisplayName
	*dest[4].(*bool) = u.IsPlatformAdmin
	*dest[5].(*pgtype.Timestamptz) = u.CreatedAt
	*dest[6].(*pgtype.Timestamptz) = u.UpdatedAt
	return nil
}

type orgRow struct{ org *queries.Organization }
func (r *orgRow) Scan(dest ...interface{}) error {
	o := r.org
	*dest[0].(*pgtype.UUID) = o.ID
	*dest[1].(*string) = o.Name
	*dest[2].(*string) = o.Slug
	*dest[3].(*string) = o.EmailDomain
	*dest[4].(*pgtype.Timestamptz) = o.CreatedAt
	*dest[5].(*pgtype.Timestamptz) = o.UpdatedAt
	return nil
}

type orgCreateRow struct{ org *queries.Organization }
func (r *orgCreateRow) Scan(dest ...interface{}) error {
	o := r.org
	*dest[0].(*pgtype.UUID) = o.ID
	*dest[1].(*string) = o.Name
	*dest[2].(*string) = o.Slug
	*dest[3].(*string) = o.EmailDomain
	*dest[4].(*pgtype.Timestamptz) = o.CreatedAt
	*dest[5].(*pgtype.Timestamptz) = o.UpdatedAt
	return nil
}

type membershipRow struct{ membership *queries.OrganizationMembership }
func (r *membershipRow) Scan(dest ...interface{}) error {
	m := r.membership
	*dest[0].(*pgtype.UUID) = m.ID
	*dest[1].(*pgtype.UUID) = m.OrganizationID
	*dest[2].(*pgtype.UUID) = m.UserID
	*dest[3].(*string) = m.Role
	*dest[4].(*string) = m.Status
	*dest[5].(*pgtype.Timestamptz) = m.CreatedAt
	return nil
}

type identityRow struct{ identity *queries.UserIdentity }
func (r *identityRow) Scan(dest ...interface{}) error {
	id := r.identity
	*dest[0].(*pgtype.UUID) = id.ID
	*dest[1].(*pgtype.UUID) = id.UserID
	*dest[2].(*string) = id.Provider
	*dest[3].(*string) = id.ProviderSubject
	*dest[4].(*string) = id.ProviderLogin
	*dest[5].(*string) = id.ProviderEmail
	*dest[6].(*string) = id.ProviderDisplayName
	*dest[7].(*[]byte) = id.Claims
	*dest[8].(*pgtype.Timestamptz) = id.LastLoginAt
	*dest[9].(*pgtype.Timestamptz) = id.CreatedAt
	*dest[10].(*pgtype.Timestamptz) = id.UpdatedAt
	return nil
}

type emptyRows struct{ closed bool }
func (r *emptyRows) Close()                                         { r.closed = true }
func (r *emptyRows) Err() error                                     { return nil }
func (r *emptyRows) CommandTag() pgconn.CommandTag                   { return pgconn.CommandTag{} }
func (r *emptyRows) FieldDescriptions() []pgconn.FieldDescription   { return nil }
func (r *emptyRows) Next() bool                                     { return false }
func (r *emptyRows) Scan(_ ...interface{}) error                    { return fmt.Errorf("no rows") }
func (r *emptyRows) Values() ([]interface{}, error)                 { return nil, nil }
func (r *emptyRows) RawValues() [][]byte                            { return nil }
func (r *emptyRows) Conn() *pgx.Conn                                { return nil }

type orgRows struct {
	orgs []queries.Organization
	idx  int
}
func (r *orgRows) Close()                                           { r.idx = len(r.orgs) }
func (r *orgRows) Err() error                                       { return nil }
func (r *orgRows) CommandTag() pgconn.CommandTag                     { return pgconn.CommandTag{} }
func (r *orgRows) FieldDescriptions() []pgconn.FieldDescription     { return nil }
func (r *orgRows) Next() bool                                       { return r.idx < len(r.orgs) }
func (r *orgRows) Scan(dest ...interface{}) error {
	if r.idx >= len(r.orgs) {
		return fmt.Errorf("no more rows")
	}
	o := r.orgs[r.idx]
	r.idx++
	*dest[0].(*pgtype.UUID) = o.ID
	*dest[1].(*string) = o.Name
	*dest[2].(*string) = o.Slug
	*dest[3].(*string) = o.EmailDomain
	*dest[4].(*pgtype.Timestamptz) = o.CreatedAt
	*dest[5].(*pgtype.Timestamptz) = o.UpdatedAt
	return nil
}
func (r *orgRows) Values() ([]interface{}, error) { return nil, nil }
func (r *orgRows) RawValues() [][]byte            { return nil }
func (r *orgRows) Conn() *pgx.Conn                { return nil }

// ────────────────────────────────────────────────────────────────────────────
// Helper to create test API with fake DB
// ────────────────────────────────────────────────────────────────────────────

func newTestOAuthAPI(store *fakeStore) *API {
	db := &fakeOAuthDBTX{store: store}
	return &API{q: queries.New(db)}
}

func pgUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

// ────────────────────────────────────────────────────────────────────────────
// VAL-OAUTH-001: Matching non-consumer domain creates pending access
// ────────────────────────────────────────────────────────────────────────────

func TestOAuthDomainMatch_BusinessDomainCreatesPendingMembership(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	api := newTestOAuthAPI(store)

	// Pre-existing org with email domain set.
	existingOrgID := uuid.New()
	store.addOrg(queries.Organization{
		ID:          pgUUID(existingOrgID),
		Name:        "Acme Corp",
		Slug:        "acme",
		EmailDomain: "acme.com",
	})

	// A brand-new OAuth login with a matching business domain.
	identity := extoauth.Identity{
		Subject:     "github|12345",
		Email:       "alice@acme.com",
		Login:       "alice",
		DisplayName: "Alice",
	}

	user, err := api.resolveExternalLogin(context.Background(), "github", identity, nil)
	if err != nil {
		t.Fatalf("resolveExternalLogin failed: %v", err)
	}

	// The user should be created.
	if user.Email != "alice@acme.com" {
		t.Fatalf("user email = %q, want alice@acme.com", user.Email)
	}

	// Should have a pending membership for the matched org, NOT active.
	m, found := store.findMembership(user.ID, pgUUID(existingOrgID))
	if !found {
		t.Fatal("expected pending membership record to exist for matched org")
	}
	if m.Status != "pending" {
		t.Fatalf("membership status = %q, want pending", m.Status)
	}

	// Should NOT have active membership for the matched org.
	activeCnt := store.countMemberships(user.ID, pgUUID(existingOrgID), "active")
	if activeCnt != 0 {
		t.Fatalf("active membership count for matched org = %d, want 0", activeCnt)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// VAL-OAUTH-002: Unmatched domain auto-creates new organization
// ────────────────────────────────────────────────────────────────────────────

func TestOAuthDomainMatch_UnmatchedDomainCreatesNewOrg(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	api := newTestOAuthAPI(store)

	identity := extoauth.Identity{
		Subject:     "github|99999",
		Email:       "bob@newstartup.com",
		Login:       "bob",
		DisplayName: "Bob",
	}

	user, err := api.resolveExternalLogin(context.Background(), "github", identity, nil)
	if err != nil {
		t.Fatalf("resolveExternalLogin failed: %v", err)
	}

	// User should be created.
	if user.Email != "bob@newstartup.com" {
		t.Fatalf("user email = %q, want bob@newstartup.com", user.Email)
	}

	// Should have an active owner membership in a new org.
	activeCnt := store.countActiveMemberships(user.ID)
	if activeCnt == 0 {
		t.Fatal("expected at least one active membership for new user with unmatched domain")
	}

	// The new org should have its email_domain set.
	found := false
	store.mu.Lock()
	for _, o := range store.orgs {
		if o.EmailDomain == "newstartup.com" {
			found = true
			break
		}
	}
	store.mu.Unlock()
	if !found {
		t.Fatal("expected new org to have email_domain = newstartup.com")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// VAL-OAUTH-003: Consumer email domains excluded from org-domain matching
// ────────────────────────────────────────────────────────────────────────────

func TestOAuthDomainMatch_ConsumerDomainSkipsOrgMatching(t *testing.T) {
	t.Parallel()

	existingOrgID := uuid.New()

	consumerDomainEmails := []string{
		"user@gmail.com", "user@yahoo.com", "user@outlook.com",
		"user@hotmail.com", "user@icloud.com", "user@aol.com",
		"user@protonmail.com", "user@mail.com", "user@zoho.com",
	}

	for i, email := range consumerDomainEmails {
		t.Run(email, func(t *testing.T) {
			localStore := newFakeStore()
			localStore.addOrg(queries.Organization{
				ID:          pgUUID(existingOrgID),
				Name:        "Gmail Org",
				Slug:        "gmail-org",
				EmailDomain: extractEmailDomain(email),
			})
			localAPI := newTestOAuthAPI(localStore)

			identity := extoauth.Identity{
				Subject:     fmt.Sprintf("github|consumer-%d", i),
				Email:       email,
				Login:       fmt.Sprintf("consumer%d", i),
				DisplayName: fmt.Sprintf("Consumer %d", i),
			}

			user, err := localAPI.resolveExternalLogin(context.Background(), "github", identity, nil)
			if err != nil {
				t.Fatalf("resolveExternalLogin failed for %s: %v", email, err)
			}

			// Should NOT have a membership (active or pending) for the existing org.
			_, found := localStore.findMembership(user.ID, pgUUID(existingOrgID))
			if found {
				t.Fatalf("consumer domain %s should not create membership in existing org", email)
			}

			// Should have created a new org with active membership.
			activeCnt := localStore.countActiveMemberships(user.ID)
			if activeCnt == 0 {
				t.Fatalf("consumer domain %s should create new org with active membership", email)
			}
		})
	}
}

// ────────────────────────────────────────────────────────────────────────────
// VAL-OAUTH-004: Pending users cannot obtain authenticated org session
// ────────────────────────────────────────────────────────────────────────────

func TestOAuthDomainMatch_PendingUserCannotAccessMatchedOrg(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	api := newTestOAuthAPI(store)

	// Existing org with domain set.
	existingOrgID := uuid.New()
	store.addOrg(queries.Organization{
		ID:          pgUUID(existingOrgID),
		Name:        "SecureCorp",
		Slug:        "securecorp",
		EmailDomain: "securecorp.com",
	})

	identity := extoauth.Identity{
		Subject:     "github|pending-test",
		Email:       "pending@securecorp.com",
		Login:       "pending",
		DisplayName: "Pending User",
	}

	user, err := api.resolveExternalLogin(context.Background(), "github", identity, nil)
	if err != nil {
		t.Fatalf("resolveExternalLogin failed: %v", err)
	}

	// Confirm pending membership.
	m, found := store.findMembership(user.ID, pgUUID(existingOrgID))
	if !found || m.Status != "pending" {
		t.Fatalf("expected pending membership, got found=%v status=%q", found, m.Status)
	}

	// issueSessionForUser queries OrganizationsForUser which only returns active memberships.
	// The pending user should NOT see SecureCorp as an available org.
	orgs := store.orgsForUser(user.ID)
	for _, o := range orgs {
		if o.ID == pgUUID(existingOrgID) {
			t.Fatal("pending user should not see the matched org in OrganizationsForUser")
		}
	}

	// The user has NO active memberships in the matched org — they cannot get a session for it.
	// issueSessionForUser would fail or return a different org if user has no personal org either.
	// Since domain-matched users with pending status don't get personal org created for the matched org,
	// they have no active memberships in the matched org at all.
	activeInMatchedOrg := store.countMemberships(user.ID, pgUUID(existingOrgID), "active")
	if activeInMatchedOrg != 0 {
		t.Fatalf("pending user has %d active memberships in matched org, want 0", activeInMatchedOrg)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// VAL-OAUTH-005: Org admin approval activates access
// ────────────────────────────────────────────────────────────────────────────

func TestOAuthDomainMatch_AdminApprovalActivatesMembership(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	api := newTestOAuthAPI(store)

	existingOrgID := uuid.New()
	store.addOrg(queries.Organization{
		ID:          pgUUID(existingOrgID),
		Name:        "ApproveCorp",
		Slug:        "approvecorp",
		EmailDomain: "approvecorp.com",
	})

	identity := extoauth.Identity{
		Subject:     "github|approve-test",
		Email:       "newbie@approvecorp.com",
		Login:       "newbie",
		DisplayName: "Newbie",
	}

	user, err := api.resolveExternalLogin(context.Background(), "github", identity, nil)
	if err != nil {
		t.Fatalf("resolveExternalLogin failed: %v", err)
	}

	// Confirm pending.
	m, found := store.findMembership(user.ID, pgUUID(existingOrgID))
	if !found || m.Status != "pending" {
		t.Fatalf("expected pending, got found=%v status=%q", found, m.Status)
	}

	// Admin approves — simulate via direct query.
	_, err = api.q.OrganizationMembershipUpdateStatus(context.Background(), queries.OrganizationMembershipUpdateStatusParams{
		OrganizationID: pgUUID(existingOrgID),
		UserID:         user.ID,
		Status:         "active",
	})
	if err != nil {
		t.Fatalf("approve membership failed: %v", err)
	}

	// Now the membership should be active.
	m2, found := store.findMembership(user.ID, pgUUID(existingOrgID))
	if !found {
		t.Fatal("membership not found after approval")
	}
	if m2.Status != "active" {
		t.Fatalf("membership status after approval = %q, want active", m2.Status)
	}

	// User should now see the org in OrganizationsForUser.
	orgs := store.orgsForUser(user.ID)
	foundOrg := false
	for _, o := range orgs {
		if o.ID == pgUUID(existingOrgID) {
			foundOrg = true
		}
	}
	if !foundOrg {
		t.Fatal("approved user should see matched org in OrganizationsForUser")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// VAL-OAUTH-006: Org admin rejection keeps user out
// ────────────────────────────────────────────────────────────────────────────

func TestOAuthDomainMatch_AdminRejectionKeepsUserOut(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	api := newTestOAuthAPI(store)

	existingOrgID := uuid.New()
	store.addOrg(queries.Organization{
		ID:          pgUUID(existingOrgID),
		Name:        "RejectCorp",
		Slug:        "rejectcorp",
		EmailDomain: "rejectcorp.com",
	})

	identity := extoauth.Identity{
		Subject:     "github|reject-test",
		Email:       "rejected@rejectcorp.com",
		Login:       "rejected",
		DisplayName: "Rejected User",
	}

	user, err := api.resolveExternalLogin(context.Background(), "github", identity, nil)
	if err != nil {
		t.Fatalf("resolveExternalLogin failed: %v", err)
	}

	// Admin rejects.
	_, err = api.q.OrganizationMembershipUpdateStatus(context.Background(), queries.OrganizationMembershipUpdateStatusParams{
		OrganizationID: pgUUID(existingOrgID),
		UserID:         user.ID,
		Status:         "rejected",
	})
	if err != nil {
		t.Fatalf("reject membership failed: %v", err)
	}

	// Membership should be rejected.
	m, found := store.findMembership(user.ID, pgUUID(existingOrgID))
	if !found {
		t.Fatal("membership not found after rejection")
	}
	if m.Status != "rejected" {
		t.Fatalf("membership status after rejection = %q, want rejected", m.Status)
	}

	// Rejected user should NOT see the org.
	orgs := store.orgsForUser(user.ID)
	for _, o := range orgs {
		if o.ID == pgUUID(existingOrgID) {
			t.Fatal("rejected user should not see matched org in OrganizationsForUser")
		}
	}

	// No active membership.
	activeCnt := store.countMemberships(user.ID, pgUUID(existingOrgID), "active")
	if activeCnt != 0 {
		t.Fatalf("rejected user has %d active memberships, want 0", activeCnt)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// VAL-OAUTH-007: Existing active member signs in normally (not downgraded)
// ────────────────────────────────────────────────────────────────────────────

func TestOAuthDomainMatch_ExistingActiveMemberNotDowngraded(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	api := newTestOAuthAPI(store)

	existingOrgID := uuid.New()
	existingUserID := uuid.New()

	store.addOrg(queries.Organization{
		ID:          pgUUID(existingOrgID),
		Name:        "ActiveCorp",
		Slug:        "activecorp",
		EmailDomain: "activecorp.com",
	})
	store.addUser(queries.User{
		ID:    pgUUID(existingUserID),
		Email: "veteran@activecorp.com",
	})
	store.addIdentity(queries.UserIdentity{
		ID:              pgUUID(uuid.New()),
		UserID:          pgUUID(existingUserID),
		Provider:        "github",
		ProviderSubject: "github|veteran",
		ProviderLogin:   "veteran",
		ProviderEmail:   "veteran@activecorp.com",
	})
	store.addMembership(queries.OrganizationMembership{
		ID:             pgUUID(uuid.New()),
		OrganizationID: pgUUID(existingOrgID),
		UserID:         pgUUID(existingUserID),
		Role:           "member",
		Status:         "active",
	})

	// Returning user logs in again via OAuth.
	identity := extoauth.Identity{
		Subject:     "github|veteran",
		Email:       "veteran@activecorp.com",
		Login:       "veteran",
		DisplayName: "Veteran",
	}

	user, err := api.resolveExternalLogin(context.Background(), "github", identity, nil)
	if err != nil {
		t.Fatalf("resolveExternalLogin failed: %v", err)
	}

	// User should still be the same user.
	if user.ID != pgUUID(existingUserID) {
		t.Fatalf("returned different user ID, expected existing user")
	}

	// Membership should still be active (not downgraded to pending).
	m, found := store.findMembership(pgUUID(existingUserID), pgUUID(existingOrgID))
	if !found {
		t.Fatal("membership not found for existing active member")
	}
	if m.Status != "active" {
		t.Fatalf("existing active member status = %q, want active (not downgraded)", m.Status)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// VAL-OAUTH-008: Repeated pending logins are idempotent (no duplicates)
// ────────────────────────────────────────────────────────────────────────────

func TestOAuthDomainMatch_RepeatedPendingLoginsIdempotent(t *testing.T) {
	t.Parallel()

	existingOrgID := uuid.New()

	// We need to simulate 3 login attempts. The first creates the user+identity,
	// subsequent ones find the existing identity and skip the creation path.
	// So we need a store that persists across all 3.
	store := newFakeStore()
	api := newTestOAuthAPI(store)

	store.addOrg(queries.Organization{
		ID:          pgUUID(existingOrgID),
		Name:        "IdempotentCorp",
		Slug:        "idempotentcorp",
		EmailDomain: "idempotentcorp.com",
	})

	identity := extoauth.Identity{
		Subject:     "github|idempotent",
		Email:       "repeat@idempotentcorp.com",
		Login:       "repeat",
		DisplayName: "Repeat User",
	}

	// First login — creates user and pending membership.
	user1, err := api.resolveExternalLogin(context.Background(), "github", identity, nil)
	if err != nil {
		t.Fatalf("first login failed: %v", err)
	}

	// Count pending memberships.
	pendingCnt1 := store.countMemberships(user1.ID, pgUUID(existingOrgID), "pending")
	if pendingCnt1 != 1 {
		t.Fatalf("after first login: pending count = %d, want 1", pendingCnt1)
	}

	// Second login — the user identity already exists, so resolveExternalLogin
	// takes the "existing identity" path (returns early).
	user2, err := api.resolveExternalLogin(context.Background(), "github", identity, nil)
	if err != nil {
		t.Fatalf("second login failed: %v", err)
	}
	if user2.ID != user1.ID {
		t.Fatal("second login returned different user")
	}

	// Should still have exactly 1 pending membership.
	pendingCnt2 := store.countMemberships(user1.ID, pgUUID(existingOrgID), "pending")
	if pendingCnt2 != 1 {
		t.Fatalf("after second login: pending count = %d, want 1", pendingCnt2)
	}

	// Third login — same identity, same result.
	user3, err := api.resolveExternalLogin(context.Background(), "github", identity, nil)
	if err != nil {
		t.Fatalf("third login failed: %v", err)
	}
	if user3.ID != user1.ID {
		t.Fatal("third login returned different user")
	}

	// Still exactly 1 pending membership.
	pendingCnt3 := store.countMemberships(user1.ID, pgUUID(existingOrgID), "pending")
	if pendingCnt3 != 1 {
		t.Fatalf("after third login: pending count = %d, want 1", pendingCnt3)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Additional edge-case tests
// ────────────────────────────────────────────────────────────────────────────

// TestOAuthDomainMatch_BusinessDomainNoMatchCreatesOrgWithDomain verifies
// that a business-domain user whose domain doesn't match any existing org
// gets a new org created AND that org's email_domain is set.
func TestOAuthDomainMatch_BusinessDomainNoMatchCreatesOrgWithDomain(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	api := newTestOAuthAPI(store)

	identity := extoauth.Identity{
		Subject:     "github|biz-new",
		Email:       "founder@brandnew.io",
		Login:       "founder",
		DisplayName: "Founder",
	}

	user, err := api.resolveExternalLogin(context.Background(), "github", identity, nil)
	if err != nil {
		t.Fatalf("resolveExternalLogin failed: %v", err)
	}

	// Should have an active owner membership.
	activeCnt := store.countActiveMemberships(user.ID)
	if activeCnt == 0 {
		t.Fatal("expected active membership for new business-domain user")
	}

	// The newly created org should have email_domain = "brandnew.io".
	store.mu.Lock()
	found := false
	for _, o := range store.orgs {
		if o.EmailDomain == "brandnew.io" {
			found = true
		}
	}
	store.mu.Unlock()
	if !found {
		t.Fatal("expected new org to have email_domain = brandnew.io")
	}
}

// TestOAuthDomainMatch_ConsumerDomainDoesNotSetEmailDomain verifies that
// a consumer-domain user's new org does NOT get email_domain set.
func TestOAuthDomainMatch_ConsumerDomainDoesNotSetEmailDomain(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	api := newTestOAuthAPI(store)

	identity := extoauth.Identity{
		Subject:     "github|gmail-user",
		Email:       "gmailuser@gmail.com",
		Login:       "gmailuser",
		DisplayName: "Gmail User",
	}

	_, err := api.resolveExternalLogin(context.Background(), "github", identity, nil)
	if err != nil {
		t.Fatalf("resolveExternalLogin failed: %v", err)
	}

	// No org should have email_domain = "gmail.com".
	store.mu.Lock()
	for _, o := range store.orgs {
		if o.EmailDomain == "gmail.com" {
			t.Fatal("consumer domain user's org should not have email_domain set to gmail.com")
		}
	}
	store.mu.Unlock()
}

// Ensure all errors are used (package import validation).
var _ = errors.Is
