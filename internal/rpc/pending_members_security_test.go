package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/auth"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

// ────────────────────────────────────────────────────────────────────────────
// Security: Approve/reject endpoints must only affect pending memberships.
// Active memberships must not be modifiable through these endpoints.
// ────────────────────────────────────────────────────────────────────────────

func TestOAuthPendingMember_ApproveOnlyWorksPending(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	api := newTestOAuthAPI(store)

	orgID := uuid.New()
	pendingUserID := uuid.New()

	store.addOrg(queries.Organization{
		ID:   pgUUID(orgID),
		Name: "TestOrg",
		Slug: "testorg",
	})
	store.addUser(queries.User{
		ID:    pgUUID(pendingUserID),
		Email: "pending@testorg.com",
	})
	store.addMembership(queries.OrganizationMembership{
		ID:             pgUUID(uuid.New()),
		OrganizationID: pgUUID(orgID),
		UserID:         pgUUID(pendingUserID),
		Role:           "member",
		Status:         "pending",
	})

	// Admin principal
	adminID := uuid.New()
	p := auth.Principal{
		UserID:         adminID,
		OrgID:          orgID,
		OrganizationID: pgtype.UUID{Bytes: orgID, Valid: true},
		OrgRole:        "admin",
	}

	req := httptest.NewRequest("POST", "/api/org/pending-members/"+pendingUserID.String()+"/approve", nil)
	req.SetPathValue("user_id", pendingUserID.String())
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rr := httptest.NewRecorder()

	api.approvePendingMember(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("approve pending member: status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify membership is now active
	m, found := store.findMembership(pgUUID(pendingUserID), pgUUID(orgID))
	if !found {
		t.Fatal("membership not found after approval")
	}
	if m.Status != "active" {
		t.Fatalf("membership status after approval = %q, want active", m.Status)
	}
}

func TestOAuthPendingMember_ApproveBlocksActiveMembership(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	api := newTestOAuthAPI(store)

	orgID := uuid.New()
	activeUserID := uuid.New()

	store.addOrg(queries.Organization{
		ID:   pgUUID(orgID),
		Name: "TestOrg",
		Slug: "testorg",
	})
	store.addUser(queries.User{
		ID:    pgUUID(activeUserID),
		Email: "active@testorg.com",
	})
	store.addMembership(queries.OrganizationMembership{
		ID:             pgUUID(uuid.New()),
		OrganizationID: pgUUID(orgID),
		UserID:         pgUUID(activeUserID),
		Role:           "member",
		Status:         "active",
	})

	adminID := uuid.New()
	p := auth.Principal{
		UserID:         adminID,
		OrgID:          orgID,
		OrganizationID: pgtype.UUID{Bytes: orgID, Valid: true},
		OrgRole:        "admin",
	}

	req := httptest.NewRequest("POST", "/api/org/pending-members/"+activeUserID.String()+"/approve", nil)
	req.SetPathValue("user_id", activeUserID.String())
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rr := httptest.NewRecorder()

	api.approvePendingMember(rr, req)

	// Should be rejected — active memberships cannot be modified through pending endpoints.
	if rr.Code == http.StatusOK {
		t.Fatalf("approve active member: status = %d, expected non-200 (should reject active membership)", rr.Code)
	}
	if rr.Code != http.StatusConflict {
		t.Fatalf("approve active member: status = %d, want %d", rr.Code, http.StatusConflict)
	}

	// Verify membership was NOT changed.
	m, found := store.findMembership(pgUUID(activeUserID), pgUUID(orgID))
	if !found {
		t.Fatal("membership not found after attempted approve on active member")
	}
	if m.Status != "active" {
		t.Fatalf("active membership status changed to %q after approve attempt, should remain active", m.Status)
	}
}

func TestOAuthPendingMember_RejectOnlyWorksPending(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	api := newTestOAuthAPI(store)

	orgID := uuid.New()
	pendingUserID := uuid.New()

	store.addOrg(queries.Organization{
		ID:   pgUUID(orgID),
		Name: "TestOrg",
		Slug: "testorg",
	})
	store.addUser(queries.User{
		ID:    pgUUID(pendingUserID),
		Email: "pending@testorg.com",
	})
	store.addMembership(queries.OrganizationMembership{
		ID:             pgUUID(uuid.New()),
		OrganizationID: pgUUID(orgID),
		UserID:         pgUUID(pendingUserID),
		Role:           "member",
		Status:         "pending",
	})

	adminID := uuid.New()
	p := auth.Principal{
		UserID:         adminID,
		OrgID:          orgID,
		OrganizationID: pgtype.UUID{Bytes: orgID, Valid: true},
		OrgRole:        "admin",
	}

	req := httptest.NewRequest("POST", "/api/org/pending-members/"+pendingUserID.String()+"/reject", nil)
	req.SetPathValue("user_id", pendingUserID.String())
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rr := httptest.NewRecorder()

	api.rejectPendingMember(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("reject pending member: status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	m, found := store.findMembership(pgUUID(pendingUserID), pgUUID(orgID))
	if !found {
		t.Fatal("membership not found after rejection")
	}
	if m.Status != "rejected" {
		t.Fatalf("membership status after rejection = %q, want rejected", m.Status)
	}
}

func TestOAuthPendingMember_RejectBlocksActiveMembership(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	api := newTestOAuthAPI(store)

	orgID := uuid.New()
	activeUserID := uuid.New()

	store.addOrg(queries.Organization{
		ID:   pgUUID(orgID),
		Name: "TestOrg",
		Slug: "testorg",
	})
	store.addUser(queries.User{
		ID:    pgUUID(activeUserID),
		Email: "active@testorg.com",
	})
	store.addMembership(queries.OrganizationMembership{
		ID:             pgUUID(uuid.New()),
		OrganizationID: pgUUID(orgID),
		UserID:         pgUUID(activeUserID),
		Role:           "member",
		Status:         "active",
	})

	adminID := uuid.New()
	p := auth.Principal{
		UserID:         adminID,
		OrgID:          orgID,
		OrganizationID: pgtype.UUID{Bytes: orgID, Valid: true},
		OrgRole:        "admin",
	}

	req := httptest.NewRequest("POST", "/api/org/pending-members/"+activeUserID.String()+"/reject", nil)
	req.SetPathValue("user_id", activeUserID.String())
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rr := httptest.NewRecorder()

	api.rejectPendingMember(rr, req)

	// Should be rejected — active memberships cannot be modified through pending endpoints.
	if rr.Code == http.StatusOK {
		t.Fatalf("reject active member: status = %d, expected non-200 (should reject active membership)", rr.Code)
	}
	if rr.Code != http.StatusConflict {
		t.Fatalf("reject active member: status = %d, want %d", rr.Code, http.StatusConflict)
	}

	// Verify membership was NOT changed.
	m, found := store.findMembership(pgUUID(activeUserID), pgUUID(orgID))
	if !found {
		t.Fatal("membership not found after attempted reject on active member")
	}
	if m.Status != "active" {
		t.Fatalf("active membership status changed to %q after reject attempt, should remain active", m.Status)
	}
}

func TestOAuthPendingMember_RejectBlocksRejectedMembership(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	api := newTestOAuthAPI(store)

	orgID := uuid.New()
	rejectedUserID := uuid.New()

	store.addOrg(queries.Organization{
		ID:   pgUUID(orgID),
		Name: "TestOrg",
		Slug: "testorg",
	})
	store.addUser(queries.User{
		ID:    pgUUID(rejectedUserID),
		Email: "rejected@testorg.com",
	})
	store.addMembership(queries.OrganizationMembership{
		ID:             pgUUID(uuid.New()),
		OrganizationID: pgUUID(orgID),
		UserID:         pgUUID(rejectedUserID),
		Role:           "member",
		Status:         "rejected",
	})

	adminID := uuid.New()
	p := auth.Principal{
		UserID:         adminID,
		OrgID:          orgID,
		OrganizationID: pgtype.UUID{Bytes: orgID, Valid: true},
		OrgRole:        "admin",
	}

	// Try to reject an already-rejected membership
	req := httptest.NewRequest("POST", "/api/org/pending-members/"+rejectedUserID.String()+"/reject", nil)
	req.SetPathValue("user_id", rejectedUserID.String())
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rr := httptest.NewRecorder()

	api.rejectPendingMember(rr, req)

	if rr.Code == http.StatusOK {
		t.Fatalf("reject already-rejected member: status = %d, expected non-200", rr.Code)
	}
}

func TestOAuthPendingMember_ApproveBlocksRejectedMembership(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	api := newTestOAuthAPI(store)

	orgID := uuid.New()
	rejectedUserID := uuid.New()

	store.addOrg(queries.Organization{
		ID:   pgUUID(orgID),
		Name: "TestOrg",
		Slug: "testorg",
	})
	store.addUser(queries.User{
		ID:    pgUUID(rejectedUserID),
		Email: "rejected@testorg.com",
	})
	store.addMembership(queries.OrganizationMembership{
		ID:             pgUUID(uuid.New()),
		OrganizationID: pgUUID(orgID),
		UserID:         pgUUID(rejectedUserID),
		Role:           "member",
		Status:         "rejected",
	})

	adminID := uuid.New()
	p := auth.Principal{
		UserID:         adminID,
		OrgID:          orgID,
		OrganizationID: pgtype.UUID{Bytes: orgID, Valid: true},
		OrgRole:        "admin",
	}

	// Try to approve a rejected membership
	req := httptest.NewRequest("POST", "/api/org/pending-members/"+rejectedUserID.String()+"/approve", nil)
	req.SetPathValue("user_id", rejectedUserID.String())
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rr := httptest.NewRecorder()

	api.approvePendingMember(rr, req)

	if rr.Code == http.StatusOK {
		t.Fatalf("approve rejected member: status = %d, expected non-200", rr.Code)
	}
}

func TestOAuthPendingMember_ApproveResponseContainsActiveStatus(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	api := newTestOAuthAPI(store)

	orgID := uuid.New()
	pendingUserID := uuid.New()

	store.addOrg(queries.Organization{
		ID:   pgUUID(orgID),
		Name: "TestOrg",
		Slug: "testorg",
	})
	store.addUser(queries.User{
		ID:    pgUUID(pendingUserID),
		Email: "pending@testorg.com",
	})
	store.addMembership(queries.OrganizationMembership{
		ID:             pgUUID(uuid.New()),
		OrganizationID: pgUUID(orgID),
		UserID:         pgUUID(pendingUserID),
		Role:           "member",
		Status:         "pending",
	})

	adminID := uuid.New()
	p := auth.Principal{
		UserID:         adminID,
		OrgID:          orgID,
		OrganizationID: pgtype.UUID{Bytes: orgID, Valid: true},
		OrgRole:        "admin",
	}

	req := httptest.NewRequest("POST", "/api/org/pending-members/"+pendingUserID.String()+"/approve", nil)
	req.SetPathValue("user_id", pendingUserID.String())
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rr := httptest.NewRecorder()

	api.approvePendingMember(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("approve pending: status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["status"] != "active" {
		t.Fatalf("response status = %v, want active", body["status"])
	}
}
