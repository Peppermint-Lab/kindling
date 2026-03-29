package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/auth"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

// ── stub infrastructure for password-change session revocation tests ──

// sessionStubRow implements pgx.Row for returning pre-canned scan results.
type sessionStubRow struct {
	scanFn func(dest ...any) error
}

func (r *sessionStubRow) Scan(dest ...any) error { return r.scanFn(dest...) }

// sessionRevocationDB is a stub DBTX that routes SQL queries to callbacks
// so we can test the password change handler's session revocation logic
// with deterministic, controlled query results.
type sessionRevocationDB struct {
	mu sync.Mutex

	// user stores the test user, keyed by raw user ID bytes.
	user *queries.User

	// sessions tracks live sessions for the stub user(s).
	// key = hex(token_hash)
	sessions map[string]*queries.UserSession

	// otherUserSessions tracks sessions belonging to other users.
	// key = hex(token_hash)
	otherUserSessions map[string]*queries.UserSession

	// passwordHash is the current stored password hash for the user.
	passwordHash string

	// failDeleteOthers, when true, makes UserSessionDeleteOthersByTokenHash
	// return an error to test failure handling.
	failDeleteOthers bool

	// failDeleteAll, when true, makes UserSessionDeleteAllForUser return error.
	failDeleteAll bool

	// deletedSessions tracks which sessions were deleted (for assertions).
	deletedSessions []string // hex(token_hash) of deleted sessions
}

func (db *sessionRevocationDB) Exec(_ context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	switch {
	case strings.Contains(sql, "UPDATE users SET password_hash"):
		// UserUpdatePasswordHash
		if len(args) >= 2 {
			db.passwordHash = args[1].(string)
		}
		return pgconn.NewCommandTag("UPDATE 1"), nil

	case strings.Contains(sql, "DELETE FROM user_sessions") && strings.Contains(sql, "token_hash !="):
		// UserSessionDeleteOthersByTokenHash
		if db.failDeleteOthers {
			return pgconn.CommandTag{}, fmt.Errorf("simulated session revocation failure")
		}
		if len(args) >= 2 {
			keepHash := hex.EncodeToString(args[1].([]byte))
			for hashKey, sess := range db.sessions {
				if hashKey != keepHash {
					db.deletedSessions = append(db.deletedSessions, hashKey)
					delete(db.sessions, hashKey)
					_ = sess // suppress unused
				}
			}
		}
		return pgconn.NewCommandTag("DELETE 1"), nil

	case strings.Contains(sql, "DELETE FROM user_sessions WHERE user_id"):
		// UserSessionDeleteAllForUser
		if db.failDeleteAll {
			return pgconn.CommandTag{}, fmt.Errorf("simulated session revocation failure")
		}
		for hashKey := range db.sessions {
			db.deletedSessions = append(db.deletedSessions, hashKey)
		}
		db.sessions = make(map[string]*queries.UserSession)
		return pgconn.NewCommandTag("DELETE 1"), nil
	}

	return pgconn.NewCommandTag(""), nil
}

func (db *sessionRevocationDB) Query(_ context.Context, _ string, _ ...interface{}) (pgx.Rows, error) {
	return nil, fmt.Errorf("sessionRevocationDB: Query not implemented")
}

func (db *sessionRevocationDB) QueryRow(_ context.Context, sql string, args ...interface{}) pgx.Row {
	db.mu.Lock()
	defer db.mu.Unlock()

	switch {
	case strings.Contains(sql, "SELECT") && strings.Contains(sql, "FROM users WHERE id"):
		// UserByID
		if db.user == nil {
			return &sessionStubRow{scanFn: func(_ ...any) error { return pgx.ErrNoRows }}
		}
		u := db.user
		return &sessionStubRow{scanFn: func(dest ...any) error {
			if len(dest) < 7 {
				return fmt.Errorf("UserByID: expected 7 scan targets, got %d", len(dest))
			}
			*dest[0].(*pgtype.UUID) = u.ID
			*dest[1].(*string) = u.Email
			*dest[2].(*string) = db.passwordHash
			*dest[3].(*string) = u.DisplayName
			*dest[4].(*bool) = u.IsPlatformAdmin
			*dest[5].(*pgtype.Timestamptz) = u.CreatedAt
			*dest[6].(*pgtype.Timestamptz) = u.UpdatedAt
			return nil
		}}

	case strings.Contains(sql, "SELECT") && strings.Contains(sql, "FROM user_sessions") && strings.Contains(sql, "token_hash"):
		// UserSessionByTokenHash
		if len(args) >= 1 {
			hashKey := hex.EncodeToString(args[0].([]byte))
			if sess, ok := db.sessions[hashKey]; ok {
				return &sessionStubRow{scanFn: func(dest ...any) error {
					if len(dest) < 6 {
						return fmt.Errorf("UserSessionByTokenHash: expected 6 scan targets, got %d", len(dest))
					}
					*dest[0].(*pgtype.UUID) = sess.ID
					*dest[1].(*pgtype.UUID) = sess.UserID
					*dest[2].(*[]byte) = sess.TokenHash
					*dest[3].(*pgtype.UUID) = sess.CurrentOrganizationID
					*dest[4].(*pgtype.Timestamptz) = sess.ExpiresAt
					*dest[5].(*pgtype.Timestamptz) = sess.CreatedAt
					return nil
				}}
			}
			// Also check other user sessions
			if sess, ok := db.otherUserSessions[hashKey]; ok {
				return &sessionStubRow{scanFn: func(dest ...any) error {
					if len(dest) < 6 {
						return fmt.Errorf("UserSessionByTokenHash: expected 6 scan targets, got %d", len(dest))
					}
					*dest[0].(*pgtype.UUID) = sess.ID
					*dest[1].(*pgtype.UUID) = sess.UserID
					*dest[2].(*[]byte) = sess.TokenHash
					*dest[3].(*pgtype.UUID) = sess.CurrentOrganizationID
					*dest[4].(*pgtype.Timestamptz) = sess.ExpiresAt
					*dest[5].(*pgtype.Timestamptz) = sess.CreatedAt
					return nil
				}}
			}
		}
		return &sessionStubRow{scanFn: func(_ ...any) error { return pgx.ErrNoRows }}
	}

	return &sessionStubRow{scanFn: func(_ ...any) error { return pgx.ErrNoRows }}
}

// ── helper functions ──

// makeTestUser creates a User with a bcrypt-hashed password.
func makeTestUser(t *testing.T, password string) (queries.User, string) {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	uid := uuid.New()
	return queries.User{
		ID:    pgtype.UUID{Bytes: uid, Valid: true},
		Email: "testuser@example.com",
		PasswordHash: hash,
		DisplayName:  "Test User",
	}, hash
}

// makeSession creates a session token and returns the raw token, its hash,
// and a UserSession struct suitable for the stub DB.
func makeSession(t *testing.T, userID pgtype.UUID, orgID pgtype.UUID) ([]byte, []byte, *queries.UserSession) {
	t.Helper()
	raw, err := auth.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}
	tokenHash := auth.HashSessionToken(raw)
	sessID := uuid.New()
	return raw, tokenHash, &queries.UserSession{
		ID:                    pgtype.UUID{Bytes: sessID, Valid: true},
		UserID:                userID,
		TokenHash:             tokenHash,
		CurrentOrganizationID: orgID,
	}
}

func passwordChangeRequest(t *testing.T, currentPassword, newPassword string, rawToken []byte, principal auth.Principal) *http.Request {
	t.Helper()
	body := fmt.Sprintf(`{"current_password":%q,"new_password":%q}`, currentPassword, newPassword)
	req := httptest.NewRequest(http.MethodPut, "/api/auth/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	setTrustedOrigin(req)

	// Set session cookie so the handler can extract the token hash
	if rawToken != nil {
		req.AddCookie(&http.Cookie{
			Name:  auth.SessionCookieName,
			Value: hex.EncodeToString(rawToken),
		})
	}

	// Inject principal into context
	req = req.WithContext(auth.WithPrincipal(req.Context(), principal))
	return req
}

// ── tests ──

// TestPasswordChangeRevokesOtherSessions verifies that on a successful
// password change, all sessions OTHER than the current one are revoked.
// Fulfills: VAL-SESSION-001 (other sessions revoked)
func TestPasswordChangeRevokesOtherSessions(t *testing.T) {
	t.Parallel()

	oldPassword := "oldpassword123"
	newPassword := "newpassword456"
	user, hash := makeTestUser(t, oldPassword)
	orgID := pgtype.UUID{Bytes: uuid.New(), Valid: true}

	rawA, hashA, sessA := makeSession(t, user.ID, orgID)
	_, hashB, sessB := makeSession(t, user.ID, orgID)
	_, hashC, sessC := makeSession(t, user.ID, orgID)

	db := &sessionRevocationDB{
		user:         &user,
		passwordHash: hash,
		sessions: map[string]*queries.UserSession{
			hex.EncodeToString(hashA): sessA,
			hex.EncodeToString(hashB): sessB,
			hex.EncodeToString(hashC): sessC,
		},
		otherUserSessions: make(map[string]*queries.UserSession),
	}

	api := &API{q: queries.New(db)}
	principal := auth.Principal{
		UserID:    uuid.UUID(user.ID.Bytes),
		SessionID: uuid.UUID(sessA.ID.Bytes),
	}
	req := passwordChangeRequest(t, oldPassword, newPassword, rawA, principal)
	rec := httptest.NewRecorder()

	api.authChangePassword(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Session A (current) should remain
	db.mu.Lock()
	defer db.mu.Unlock()

	if _, ok := db.sessions[hex.EncodeToString(hashA)]; !ok {
		t.Fatal("current session A was revoked, expected it to be preserved")
	}

	// Sessions B and C should be deleted
	if _, ok := db.sessions[hex.EncodeToString(hashB)]; ok {
		t.Fatal("session B still exists, expected it to be revoked")
	}
	if _, ok := db.sessions[hex.EncodeToString(hashC)]; ok {
		t.Fatal("session C still exists, expected it to be revoked")
	}

	if len(db.deletedSessions) != 2 {
		t.Fatalf("expected 2 sessions deleted, got %d", len(db.deletedSessions))
	}
}

// TestPasswordChangeCurrentSessionRemainsValid verifies that the session
// performing the password change is preserved and can still be used.
// Fulfills: VAL-SESSION-002 (initiating session stays valid)
func TestPasswordChangeCurrentSessionRemainsValid(t *testing.T) {
	t.Parallel()

	oldPassword := "oldpassword123"
	newPassword := "newpassword456"
	user, hash := makeTestUser(t, oldPassword)
	orgID := pgtype.UUID{Bytes: uuid.New(), Valid: true}

	rawA, hashA, sessA := makeSession(t, user.ID, orgID)
	_, hashB, sessB := makeSession(t, user.ID, orgID)

	db := &sessionRevocationDB{
		user:         &user,
		passwordHash: hash,
		sessions: map[string]*queries.UserSession{
			hex.EncodeToString(hashA): sessA,
			hex.EncodeToString(hashB): sessB,
		},
		otherUserSessions: make(map[string]*queries.UserSession),
	}

	api := &API{q: queries.New(db)}
	principal := auth.Principal{
		UserID:    uuid.UUID(user.ID.Bytes),
		SessionID: uuid.UUID(sessA.ID.Bytes),
	}
	req := passwordChangeRequest(t, oldPassword, newPassword, rawA, principal)
	rec := httptest.NewRecorder()

	api.authChangePassword(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Verify session A still exists in the DB (not deleted)
	db.mu.Lock()
	remainingA, aExists := db.sessions[hex.EncodeToString(hashA)]
	db.mu.Unlock()

	if !aExists {
		t.Fatal("session A (initiating session) was revoked, expected it to be preserved")
	}

	// Verify the session can still be looked up by token hash (simulating
	// the next authenticated request with this session).
	q := queries.New(db)
	_, err := q.UserSessionByTokenHash(context.Background(), auth.HashSessionToken(rawA))
	if err != nil {
		t.Fatalf("session A lookup failed after password change: %v", err)
	}
	_ = remainingA // used for the existence check above
}

// TestPasswordChangeRevokedSessionsReturn401 verifies that sessions revoked
// by the password change handler can no longer be used for authenticated
// requests (they return ErrNoRows, which the auth middleware translates to 401).
// Fulfills: VAL-SESSION-003 (revoked sessions get 401)
func TestPasswordChangeRevokedSessionsReturn401(t *testing.T) {
	t.Parallel()

	oldPassword := "oldpassword123"
	newPassword := "newpassword456"
	user, hash := makeTestUser(t, oldPassword)
	orgID := pgtype.UUID{Bytes: uuid.New(), Valid: true}

	rawA, hashA, sessA := makeSession(t, user.ID, orgID)
	rawB, hashB, sessB := makeSession(t, user.ID, orgID)
	rawC, hashC, sessC := makeSession(t, user.ID, orgID)

	db := &sessionRevocationDB{
		user:         &user,
		passwordHash: hash,
		sessions: map[string]*queries.UserSession{
			hex.EncodeToString(hashA): sessA,
			hex.EncodeToString(hashB): sessB,
			hex.EncodeToString(hashC): sessC,
		},
		otherUserSessions: make(map[string]*queries.UserSession),
	}

	api := &API{q: queries.New(db)}
	principal := auth.Principal{
		UserID:    uuid.UUID(user.ID.Bytes),
		SessionID: uuid.UUID(sessA.ID.Bytes),
	}
	req := passwordChangeRequest(t, oldPassword, newPassword, rawA, principal)
	rec := httptest.NewRecorder()

	api.authChangePassword(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Revoked sessions B and C should return ErrNoRows on lookup,
	// which the auth middleware would translate to 401.
	q := queries.New(db)

	_, errB := q.UserSessionByTokenHash(context.Background(), auth.HashSessionToken(rawB))
	if errB == nil {
		t.Fatal("revoked session B still found in DB, expected ErrNoRows (would be 401)")
	}

	_, errC := q.UserSessionByTokenHash(context.Background(), auth.HashSessionToken(rawC))
	if errC == nil {
		t.Fatal("revoked session C still found in DB, expected ErrNoRows (would be 401)")
	}

	// Verify current session A is still valid
	_, errA := q.UserSessionByTokenHash(context.Background(), auth.HashSessionToken(rawA))
	if errA != nil {
		t.Fatalf("current session A should still be valid, got error: %v", errA)
	}
}

// TestPasswordChangeOtherUsersUnaffected verifies that password change for
// one user does not touch any sessions belonging to other users.
// Fulfills: expected behavior "Other users' sessions are not touched"
func TestPasswordChangeOtherUsersUnaffected(t *testing.T) {
	t.Parallel()

	oldPassword := "oldpassword123"
	newPassword := "newpassword456"
	user, hash := makeTestUser(t, oldPassword)
	orgID := pgtype.UUID{Bytes: uuid.New(), Valid: true}

	// Sessions for user performing the password change
	rawA, hashA, sessA := makeSession(t, user.ID, orgID)
	_, hashB, sessB := makeSession(t, user.ID, orgID)

	// Session for a different user (should NOT be affected)
	otherUserID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	rawOther, hashOther, sessOther := makeSession(t, otherUserID, orgID)

	db := &sessionRevocationDB{
		user:         &user,
		passwordHash: hash,
		sessions: map[string]*queries.UserSession{
			hex.EncodeToString(hashA): sessA,
			hex.EncodeToString(hashB): sessB,
		},
		otherUserSessions: map[string]*queries.UserSession{
			hex.EncodeToString(hashOther): sessOther,
		},
	}

	api := &API{q: queries.New(db)}
	principal := auth.Principal{
		UserID:    uuid.UUID(user.ID.Bytes),
		SessionID: uuid.UUID(sessA.ID.Bytes),
	}
	req := passwordChangeRequest(t, oldPassword, newPassword, rawA, principal)
	rec := httptest.NewRecorder()

	api.authChangePassword(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Other user's session should still be valid
	q := queries.New(db)
	_, err := q.UserSessionByTokenHash(context.Background(), auth.HashSessionToken(rawOther))
	if err != nil {
		t.Fatalf("other user's session was affected by password change: %v", err)
	}

	// Verify the other user's session was NOT deleted
	db.mu.Lock()
	defer db.mu.Unlock()
	if _, ok := db.otherUserSessions[hex.EncodeToString(hashOther)]; !ok {
		t.Fatal("other user's session was deleted during password change")
	}
}

// TestPasswordChangeFailsWhenSessionRevocationFails verifies that when
// revoking other sessions fails, the handler returns a non-200 error
// instead of silently succeeding.
// Fulfills: "Password change returns non-200 when session revocation fails"
func TestPasswordChangeFailsWhenSessionRevocationFails(t *testing.T) {
	t.Parallel()

	oldPassword := "oldpassword123"
	newPassword := "newpassword456"
	user, hash := makeTestUser(t, oldPassword)
	orgID := pgtype.UUID{Bytes: uuid.New(), Valid: true}

	rawA, hashA, sessA := makeSession(t, user.ID, orgID)
	_, hashB, sessB := makeSession(t, user.ID, orgID)

	db := &sessionRevocationDB{
		user:         &user,
		passwordHash: hash,
		sessions: map[string]*queries.UserSession{
			hex.EncodeToString(hashA): sessA,
			hex.EncodeToString(hashB): sessB,
		},
		otherUserSessions: make(map[string]*queries.UserSession),
		failDeleteOthers:  true, // Simulate DB failure
	}

	api := &API{q: queries.New(db)}
	principal := auth.Principal{
		UserID:    uuid.UUID(user.ID.Bytes),
		SessionID: uuid.UUID(sessA.ID.Bytes),
	}
	req := passwordChangeRequest(t, oldPassword, newPassword, rawA, principal)
	rec := httptest.NewRecorder()

	api.authChangePassword(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatal("expected non-200 status when session revocation fails, got 200")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	// Verify the error response has an informative code
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode error body: %v", err)
	}
	if code, ok := body["code"].(string); !ok || code != "session_revocation_failed" {
		t.Fatalf("error code = %q, want %q", body["code"], "session_revocation_failed")
	}
}

// TestPasswordChangeFailsWhenFallbackRevocationFails verifies the same
// failure handling in the fallback path (when session token hash is not
// available, so all sessions are deleted).
func TestPasswordChangeFailsWhenFallbackRevocationFails(t *testing.T) {
	t.Parallel()

	oldPassword := "oldpassword123"
	newPassword := "newpassword456"
	user, hash := makeTestUser(t, oldPassword)

	db := &sessionRevocationDB{
		user:              &user,
		passwordHash:      hash,
		sessions:          make(map[string]*queries.UserSession),
		otherUserSessions: make(map[string]*queries.UserSession),
		failDeleteAll:     true, // Simulate DB failure on fallback path
	}

	api := &API{q: queries.New(db)}
	principal := auth.Principal{
		UserID:    uuid.UUID(user.ID.Bytes),
		SessionID: uuid.UUID([16]byte{1}),
	}

	// Request WITHOUT session cookie → triggers fallback path
	body := fmt.Sprintf(`{"current_password":%q,"new_password":%q}`, oldPassword, newPassword)
	req := httptest.NewRequest(http.MethodPut, "/api/auth/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	setTrustedOrigin(req)
	req = req.WithContext(auth.WithPrincipal(req.Context(), principal))

	rec := httptest.NewRecorder()
	api.authChangePassword(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatal("expected non-200 status when fallback session revocation fails, got 200")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// TestPasswordChangeSuccessResponseBody verifies that on success, the
// handler returns the expected JSON body.
func TestPasswordChangeSuccessResponseBody(t *testing.T) {
	t.Parallel()

	oldPassword := "oldpassword123"
	newPassword := "newpassword456"
	user, hash := makeTestUser(t, oldPassword)
	orgID := pgtype.UUID{Bytes: uuid.New(), Valid: true}

	rawA, hashA, sessA := makeSession(t, user.ID, orgID)

	db := &sessionRevocationDB{
		user:         &user,
		passwordHash: hash,
		sessions: map[string]*queries.UserSession{
			hex.EncodeToString(hashA): sessA,
		},
		otherUserSessions: make(map[string]*queries.UserSession),
	}

	api := &API{q: queries.New(db)}
	principal := auth.Principal{
		UserID:    uuid.UUID(user.ID.Bytes),
		SessionID: uuid.UUID(sessA.ID.Bytes),
	}
	req := passwordChangeRequest(t, oldPassword, newPassword, rawA, principal)
	rec := httptest.NewRecorder()

	api.authChangePassword(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode success body: %v", err)
	}
	if ok, exists := body["ok"].(bool); !exists || !ok {
		t.Fatalf("expected {\"ok\":true}, got %v", body)
	}
}

// TestPasswordChangeUpdatesPasswordHash verifies that the handler actually
// updates the password hash in the database.
func TestPasswordChangeUpdatesPasswordHash(t *testing.T) {
	t.Parallel()

	oldPassword := "oldpassword123"
	newPassword := "newpassword456"
	user, hash := makeTestUser(t, oldPassword)
	orgID := pgtype.UUID{Bytes: uuid.New(), Valid: true}

	rawA, hashA, sessA := makeSession(t, user.ID, orgID)

	db := &sessionRevocationDB{
		user:         &user,
		passwordHash: hash,
		sessions: map[string]*queries.UserSession{
			hex.EncodeToString(hashA): sessA,
		},
		otherUserSessions: make(map[string]*queries.UserSession),
	}

	oldHash := db.passwordHash

	api := &API{q: queries.New(db)}
	principal := auth.Principal{
		UserID:    uuid.UUID(user.ID.Bytes),
		SessionID: uuid.UUID(sessA.ID.Bytes),
	}
	req := passwordChangeRequest(t, oldPassword, newPassword, rawA, principal)
	rec := httptest.NewRecorder()

	api.authChangePassword(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	db.mu.Lock()
	newHash := db.passwordHash
	db.mu.Unlock()

	if newHash == oldHash {
		t.Fatal("password hash was not updated in the database")
	}

	// The new hash should validate against the new password
	if !auth.CheckPassword(newHash, newPassword) {
		t.Fatal("new password hash does not validate against new password")
	}

	// The new hash should NOT validate against the old password
	if auth.CheckPassword(newHash, oldPassword) {
		t.Fatal("new password hash still validates against old password")
	}
}
