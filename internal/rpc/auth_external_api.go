package rpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/auth"
	"github.com/kindlingvm/kindling/internal/database/queries"
	extoauth "github.com/kindlingvm/kindling/internal/oauth"
)

const authStateTTL = 10 * time.Minute // auth state cookie + expiry lifetime

const externalAuthStateCookieName = "kindling_auth_flow"

var externalAuthHTTPClient = &http.Client{Timeout: 15 * time.Second}

type externalAuthState struct {
	Provider     string `json:"provider"`
	Mode         string `json:"mode"`
	State        string `json:"state"`
	CodeVerifier string `json:"code_verifier"`
	Nonce        string `json:"nonce,omitempty"`
	LinkUserID   string `json:"link_user_id,omitempty"`
	ReturnTo     string `json:"return_to,omitempty"`
	ExpiresAt    int64  `json:"expires_at"`
}

func (a *API) registerExternalAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/providers", a.listPublicAuthProviders)
	mux.HandleFunc("GET /api/auth/admin/providers", a.listAdminAuthProviders)
	mux.HandleFunc("PUT /api/auth/admin/providers/{provider}", a.putAdminAuthProvider)
	mux.HandleFunc("GET /api/auth/identities", a.listAuthIdentities)
	mux.HandleFunc("GET /api/auth/providers/{provider}/start", a.startExternalAuth)
	mux.HandleFunc("GET /api/auth/providers/{provider}/link", a.linkExternalAuth)
	mux.HandleFunc("GET /api/auth/providers/{provider}/callback", a.externalAuthCallback)
}

func (a *API) listPublicAuthProviders(w http.ResponseWriter, r *http.Request) {
	rows, err := a.q.AuthProviderListEnabled(r.Context())
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_auth_providers", err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if !authProviderReady(row) {
			continue
		}
		out = append(out, map[string]any{
			"provider":     row.Provider,
			"display_name": authProviderDisplayName(row),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) listAdminAuthProviders(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requirePlatformAdmin(w, p) {
		return
	}
	rows, err := a.q.AuthProviderListAll(r.Context())
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_auth_provider_admin", err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, a.authProviderAdminJSON(r.Context(), r, row))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) putAdminAuthProvider(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requirePlatformAdmin(w, p) {
		return
	}
	provider := strings.TrimSpace(strings.ToLower(r.PathValue("provider")))
	if provider != "github" && provider != "oidc" {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "provider must be github or oidc")
		return
	}
	var req struct {
		DisplayName       string `json:"display_name"`
		Enabled           bool   `json:"enabled"`
		ClientID          string `json:"client_id"`
		ClientSecret      string `json:"client_secret"`
		ClearClientSecret bool   `json:"clear_client_secret"`
		IssuerURL         string `json:"issuer_url"`
		Scopes            string `json:"scopes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	existing, err := a.q.AuthProviderByProvider(r.Context(), provider)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "load_auth_provider", err)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		existing = queries.AuthProvider{Provider: provider}
	}

	clientSecretCiphertext := existing.ClientSecretCiphertext
	if req.ClearClientSecret {
		clientSecretCiphertext = nil
	}
	if secret := strings.TrimSpace(req.ClientSecret); secret != "" {
		if a.cfg == nil {
			writeAPIError(w, http.StatusInternalServerError, "auth_provider_secret", "config manager unavailable")
			return
		}
		cipher, err := a.cfg.EncryptBytes([]byte(secret))
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "auth_provider_secret", err)
			return
		}
		clientSecretCiphertext = cipher
	}

	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = authProviderDefaultDisplayName(provider)
	}
	clientID := strings.TrimSpace(req.ClientID)
	issuerURL := strings.TrimSpace(req.IssuerURL)
	scopes := strings.Join(extoauth.SplitScopes(req.Scopes, authProviderDefaultScopes(provider)), " ")
	if provider == "github" {
		issuerURL = ""
	}
	if req.Enabled {
		if clientID == "" {
			writeAPIError(w, http.StatusBadRequest, "validation_error", "client_id is required")
			return
		}
		if len(clientSecretCiphertext) == 0 {
			writeAPIError(w, http.StatusBadRequest, "validation_error", "client_secret is required")
			return
		}
		if provider == "oidc" && issuerURL == "" {
			writeAPIError(w, http.StatusBadRequest, "validation_error", "issuer_url is required for oidc")
			return
		}
	}
	row, err := a.q.AuthProviderUpsert(r.Context(), queries.AuthProviderUpsertParams{
		Provider:               provider,
		DisplayName:            displayName,
		Enabled:                req.Enabled,
		ClientID:               clientID,
		ClientSecretCiphertext: clientSecretCiphertext,
		IssuerUrl:              issuerURL,
		AuthUrl:                "",
		TokenUrl:               "",
		UserinfoUrl:            "",
		Scopes:                 scopes,
		Metadata:               authProviderMetadata(existing.Metadata),
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "save_auth_provider", err)
		return
	}
	writeJSON(w, http.StatusOK, a.authProviderAdminJSON(r.Context(), r, row))
}

func (a *API) listAuthIdentities(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	rows, err := a.q.UserIdentityListByUser(r.Context(), auth.PgUUID(p.UserID))
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_auth_identities", err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"provider":              row.Provider,
			"provider_login":        row.ProviderLogin,
			"provider_email":        row.ProviderEmail,
			"provider_display_name": row.ProviderDisplayName,
			"created_at":            timestampString(row.CreatedAt),
			"updated_at":            timestampString(row.UpdatedAt),
			"last_login_at":         nullableTimestampString(row.LastLoginAt),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) startExternalAuth(w http.ResponseWriter, r *http.Request) {
	provider := strings.TrimSpace(strings.ToLower(r.PathValue("provider")))
	if err := a.beginExternalAuth(w, r, provider, "login", "", sanitizeReturnTo(r.URL.Query().Get("return_to"), "/")); err != nil {
		writeAPIErrorFromErr(w, http.StatusBadRequest, "start_auth_provider", err)
	}
}

func (a *API) linkExternalAuth(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	provider := strings.TrimSpace(strings.ToLower(r.PathValue("provider")))
	if err := a.beginExternalAuth(w, r, provider, "link", p.UserID.String(), sanitizeReturnTo(r.URL.Query().Get("return_to"), "/settings?tab=authentication")); err != nil {
		writeAPIErrorFromErr(w, http.StatusBadRequest, "link_auth_provider", err)
	}
}

func (a *API) externalAuthCallback(w http.ResponseWriter, r *http.Request) {
	state, err := a.readExternalAuthState(r)
	a.clearExternalAuthState(w, r)
	if err != nil {
		a.redirectExternalAuthError(w, r, "/login", err)
		return
	}
	provider := strings.TrimSpace(strings.ToLower(r.PathValue("provider")))
	if state.Provider != provider {
		a.redirectExternalAuthError(w, r, externalAuthFailurePath(state), fmt.Errorf("authentication state provider mismatch"))
		return
	}
	if remoteError := strings.TrimSpace(r.URL.Query().Get("error")); remoteError != "" {
		message := strings.TrimSpace(r.URL.Query().Get("error_description"))
		if message == "" {
			message = remoteError
		}
		a.redirectExternalAuthError(w, r, externalAuthFailurePath(state), fmt.Errorf("%s", message))
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("state")) != state.State {
		a.redirectExternalAuthError(w, r, externalAuthFailurePath(state), fmt.Errorf("authentication state mismatch"))
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		a.redirectExternalAuthError(w, r, externalAuthFailurePath(state), fmt.Errorf("authentication code missing"))
		return
	}
	providerRow, err := a.q.AuthProviderByProvider(r.Context(), provider)
	if err != nil {
		a.redirectExternalAuthError(w, r, externalAuthFailurePath(state), err)
		return
	}
	if !authProviderReady(providerRow) {
		a.redirectExternalAuthError(w, r, externalAuthFailurePath(state), fmt.Errorf("authentication provider is not configured"))
		return
	}
	identity, orgLogins, err := a.exchangeExternalIdentity(r.Context(), r, providerRow, code, state)
	if err != nil {
		a.redirectExternalAuthError(w, r, externalAuthFailurePath(state), err)
		return
	}
	if state.Mode == "link" {
		userID, err := uuid.Parse(strings.TrimSpace(state.LinkUserID))
		if err != nil {
			a.redirectExternalAuthError(w, r, "/settings?tab=authentication", fmt.Errorf("linking session expired"))
			return
		}
		if _, err := a.q.UserByID(r.Context(), auth.PgUUID(userID)); err != nil {
			a.redirectExternalAuthError(w, r, "/settings?tab=authentication", err)
			return
		}
		if err := a.linkExternalIdentity(r.Context(), auth.PgUUID(userID), provider, identity); err != nil {
			a.redirectExternalAuthError(w, r, "/settings?tab=authentication", err)
			return
		}
		a.redirectExternalAuthSuccess(w, r, "/settings?tab=authentication", map[string]string{"auth_linked": provider})
		return
	}
	user, err := a.resolveExternalLogin(r.Context(), provider, identity, orgLogins)
	if err != nil {
		a.redirectExternalAuthError(w, r, "/login", err)
		return
	}
	rawToken, _, _, _, err := a.issueSessionForUser(r.Context(), user)
	if err != nil {
		a.redirectExternalAuthError(w, r, "/login", err)
		return
	}
	auth.SetSessionCookie(w, r, rawToken, auth.RequestUsesHTTPS(r))
	redirectPath := sanitizeReturnTo(state.ReturnTo, "/")
	a.redirectExternalAuthSuccess(w, r, redirectPath, nil)
}

func (a *API) beginExternalAuth(w http.ResponseWriter, r *http.Request, provider, mode, linkUserID, returnTo string) error {
	if provider != "github" && provider != "oidc" {
		return fmt.Errorf("provider must be github or oidc")
	}
	providerRow, err := a.q.AuthProviderByProvider(r.Context(), provider)
	if err != nil {
		return fmt.Errorf("load auth provider: %w", err)
	}
	if !authProviderReady(providerRow) {
		return fmt.Errorf("authentication provider is not configured")
	}
	stateToken, err := extoauth.RandomToken(32)
	if err != nil {
		return fmt.Errorf("generate state token: %w", err)
	}
	codeVerifier, err := extoauth.RandomToken(48)
	if err != nil {
		return fmt.Errorf("generate code verifier: %w", err)
	}
	codeChallenge := extoauth.PKCEChallenge(codeVerifier)
	callbackURL, err := a.authProviderCallbackURL(r.Context(), r, provider)
	if err != nil {
		return fmt.Errorf("resolve callback URL: %w", err)
	}
	authURL := ""
	nonce := ""
	scopes := extoauth.SplitScopes(providerRow.Scopes, authProviderDefaultScopes(provider))
	clientSecret, err := a.authProviderClientSecret(providerRow)
	if err != nil {
		return fmt.Errorf("decrypt client secret: %w", err)
	}
	switch provider {
	case "github":
		authURL = extoauth.GitHubAuthorizeURL(extoauth.GitHubConfig{
			ClientID:     strings.TrimSpace(providerRow.ClientID),
			ClientSecret: clientSecret,
			RedirectURL:  callbackURL,
			Scopes:       scopes,
		}, stateToken, codeChallenge)
	case "oidc":
		nonce, err = extoauth.RandomToken(32)
		if err != nil {
			return fmt.Errorf("generate nonce: %w", err)
		}
		authURL, err = extoauth.OIDCAuthorizeURL(r.Context(), externalAuthHTTPClient, extoauth.OIDCConfig{
			ClientID:     strings.TrimSpace(providerRow.ClientID),
			ClientSecret: clientSecret,
			IssuerURL:    strings.TrimSpace(providerRow.IssuerUrl),
			RedirectURL:  callbackURL,
			Scopes:       scopes,
		}, stateToken, nonce, codeChallenge)
		if err != nil {
			return fmt.Errorf("build OIDC authorize URL: %w", err)
		}
	}
	if err := a.setExternalAuthState(w, r, externalAuthState{
		Provider:     provider,
		Mode:         mode,
		State:        stateToken,
		CodeVerifier: codeVerifier,
		Nonce:        nonce,
		LinkUserID:   strings.TrimSpace(linkUserID),
		ReturnTo:     sanitizeReturnTo(returnTo, "/"),
		ExpiresAt:    time.Now().Add(authStateTTL).Unix(),
	}); err != nil {
		return fmt.Errorf("set auth state cookie: %w", err)
	}
	http.Redirect(w, r, authURL, http.StatusFound)
	return nil
}

func (a *API) exchangeExternalIdentity(ctx context.Context, r *http.Request, providerRow queries.AuthProvider, code string, state externalAuthState) (extoauth.Identity, []string, error) {
	clientSecret, err := a.authProviderClientSecret(providerRow)
	if err != nil {
		return extoauth.Identity{}, nil, fmt.Errorf("decrypt client secret: %w", err)
	}
	callbackURL, err := a.authProviderCallbackURL(ctx, r, providerRow.Provider)
	if err != nil {
		return extoauth.Identity{}, nil, fmt.Errorf("resolve callback URL: %w", err)
	}
	switch providerRow.Provider {
	case "github":
		token, err := extoauth.ExchangeGitHubCode(ctx, externalAuthHTTPClient, extoauth.GitHubConfig{
			ClientID:     strings.TrimSpace(providerRow.ClientID),
			ClientSecret: clientSecret,
			RedirectURL:  callbackURL,
			Scopes:       extoauth.SplitScopes(providerRow.Scopes, authProviderDefaultScopes(providerRow.Provider)),
		}, code, state.CodeVerifier)
		if err != nil {
			return extoauth.Identity{}, nil, fmt.Errorf("exchange GitHub code: %w", err)
		}
		return extoauth.GitHubIdentity(ctx, externalAuthHTTPClient, token)
	case "oidc":
		identity, err := extoauth.ExchangeOIDCCode(ctx, externalAuthHTTPClient, extoauth.OIDCConfig{
			ClientID:     strings.TrimSpace(providerRow.ClientID),
			ClientSecret: clientSecret,
			IssuerURL:    strings.TrimSpace(providerRow.IssuerUrl),
			RedirectURL:  callbackURL,
			Scopes:       extoauth.SplitScopes(providerRow.Scopes, authProviderDefaultScopes(providerRow.Provider)),
		}, code, state.CodeVerifier, state.Nonce)
		if err != nil {
			return extoauth.Identity{}, nil, fmt.Errorf("exchange OIDC code: %w", err)
		}
		return identity, nil, nil
	default:
		return extoauth.Identity{}, nil, fmt.Errorf("unsupported auth provider")
	}
}

func (a *API) resolveExternalLogin(ctx context.Context, provider string, identity extoauth.Identity, orgLogins []string) (queries.User, error) {
	identityRow, err := a.q.UserIdentityByProviderSubject(ctx, queries.UserIdentityByProviderSubjectParams{
		Provider:        provider,
		ProviderSubject: strings.TrimSpace(identity.Subject),
	})
	if err == nil {
		if err := a.linkExternalIdentity(ctx, identityRow.UserID, provider, identity); err != nil {
			return queries.User{}, fmt.Errorf("link existing identity: %w", err)
		}
		return a.q.UserByID(ctx, identityRow.UserID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return queries.User{}, fmt.Errorf("lookup identity by subject: %w", err)
	}
	if strings.TrimSpace(identity.Email) == "" {
		return queries.User{}, fmt.Errorf("this identity does not expose a verified email address")
	}
	if _, err := a.q.UserByEmail(ctx, identity.Email); err == nil {
		return queries.User{}, fmt.Errorf("an account with that email already exists; sign in locally and link %s from settings", provider)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return queries.User{}, fmt.Errorf("lookup user by email: %w", err)
	}
	userCount, err := a.q.UserCount(ctx)
	if err != nil {
		return queries.User{}, fmt.Errorf("count users: %w", err)
	}
	userID := uuid.New()
	user, err := a.q.UserCreate(ctx, queries.UserCreateParams{
		ID:              pgtype.UUID{Bytes: userID, Valid: true},
		Email:           strings.TrimSpace(strings.ToLower(identity.Email)),
		PasswordHash:    disabledPasswordHash,
		DisplayName:     externalIdentityDisplayName(identity),
		IsPlatformAdmin: userCount == 0,
	})
	if err != nil {
		return queries.User{}, fmt.Errorf("create user: %w", err)
	}

	// Domain-aware org matching for OAuth users.
	emailDomain := extractEmailDomain(identity.Email)
	domainMatched := false

	if emailDomain != "" && !isConsumerDomain(emailDomain) {
		// Business domain: check if any org claims this domain.
		matchedOrg, lookupErr := a.q.OrganizationFindByEmailDomain(ctx, emailDomain)
		if lookupErr == nil {
			// Found a matching org — check if user is already an active member.
			existingMembership, memErr := a.q.OrganizationMembershipByUserAndOrgWithStatus(ctx, queries.OrganizationMembershipByUserAndOrgWithStatusParams{
				UserID:         user.ID,
				OrganizationID: matchedOrg.ID,
			})
			if errors.Is(memErr, pgx.ErrNoRows) {
				// No membership yet — create pending membership (idempotent).
				_, createErr := a.q.OrganizationMembershipCreateWithStatus(ctx, queries.OrganizationMembershipCreateWithStatusParams{
					ID:             pgtype.UUID{Bytes: uuid.New(), Valid: true},
					OrganizationID: matchedOrg.ID,
					UserID:         user.ID,
					Role:           "member",
					Status:         "pending",
				})
				if createErr != nil && !isPgUniqueViolation(createErr) {
					return queries.User{}, fmt.Errorf("create pending membership: %w", createErr)
				}
				domainMatched = true
			} else if memErr == nil {
				// Already has a membership — if active, they'll login normally.
				// If pending or rejected, don't re-create.
				if existingMembership.Status == "active" {
					domainMatched = false // let normal flow handle it
				} else {
					// pending or rejected — domain matched, no new org
					domainMatched = true
				}
			} else {
				return queries.User{}, fmt.Errorf("check membership: %w", memErr)
			}
		} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return queries.User{}, fmt.Errorf("lookup org by domain: %w", lookupErr)
		}
		// If no matching org found (ErrNoRows), fall through to create new org.
	}

	if !domainMatched {
		// No domain match (or consumer domain or no match found) — create personal org.
		personalName := externalIdentityDisplayName(identity)
		if personalName == "" {
			personalName = user.Email
		}
		org, err := a.createOwnedOrganizationForUser(ctx, user, personalName+"'s Workspace", identity.Login)
		if err != nil {
			return queries.User{}, fmt.Errorf("create personal organization: %w", err)
		}
		// Set the email domain on the new org so future users with the same domain can match.
		if emailDomain != "" && !isConsumerDomain(emailDomain) {
			if err := a.q.OrganizationUpdateEmailDomain(ctx, queries.OrganizationUpdateEmailDomainParams{
				ID:          org.ID,
				EmailDomain: emailDomain,
			}); err != nil {
				return queries.User{}, fmt.Errorf("set org email domain: %w", err)
			}
		}
	}

	if provider == "github" {
		orgIDs, err := a.resolveGitHubOrganizationIDs(ctx, orgLogins)
		if err != nil {
			return queries.User{}, fmt.Errorf("resolve GitHub org IDs: %w", err)
		}
		for _, orgID := range orgIDs {
			if err := a.q.OrganizationMembershipUpsert(ctx, queries.OrganizationMembershipUpsertParams{
				ID:             pgtype.UUID{Bytes: uuid.New(), Valid: true},
				OrganizationID: orgID,
				UserID:         user.ID,
				Role:           "member",
			}); err != nil {
				return queries.User{}, fmt.Errorf("upsert org membership: %w", err)
			}
		}
	}
	if err := a.linkExternalIdentity(ctx, user.ID, provider, identity); err != nil {
		return queries.User{}, fmt.Errorf("link identity for new user: %w", err)
	}
	return user, nil
}

func (a *API) linkExternalIdentity(ctx context.Context, userID pgtype.UUID, provider string, identity extoauth.Identity) error {
	if strings.TrimSpace(identity.Subject) == "" {
		return fmt.Errorf("external identity subject is missing")
	}
	claims, err := json.Marshal(identity.Claims)
	if err != nil {
		return fmt.Errorf("marshal identity claims: %w", err)
	}
	if len(claims) == 0 {
		claims = []byte(`{}`)
	}
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	bySubject, err := a.q.UserIdentityByProviderSubject(ctx, queries.UserIdentityByProviderSubjectParams{
		Provider:        provider,
		ProviderSubject: strings.TrimSpace(identity.Subject),
	})
	if err == nil {
		if bySubject.UserID != userID {
			return fmt.Errorf("that %s identity is already linked to another Kindling account", provider)
		}
		_, err = a.q.UserIdentityUpdateByID(ctx, queries.UserIdentityUpdateByIDParams{
			ID:                  bySubject.ID,
			ProviderSubject:     strings.TrimSpace(identity.Subject),
			ProviderLogin:       strings.TrimSpace(identity.Login),
			ProviderEmail:       strings.TrimSpace(identity.Email),
			ProviderDisplayName: externalIdentityDisplayName(identity),
			Claims:              claims,
			LastLoginAt:         now,
		})
		if err != nil {
			return fmt.Errorf("update identity by subject: %w", err)
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lookup identity by subject: %w", err)
	}
	byUser, err := a.q.UserIdentityByUserAndProvider(ctx, queries.UserIdentityByUserAndProviderParams{
		UserID:   userID,
		Provider: provider,
	})
	if err == nil {
		_, err = a.q.UserIdentityUpdateByID(ctx, queries.UserIdentityUpdateByIDParams{
			ID:                  byUser.ID,
			ProviderSubject:     strings.TrimSpace(identity.Subject),
			ProviderLogin:       strings.TrimSpace(identity.Login),
			ProviderEmail:       strings.TrimSpace(identity.Email),
			ProviderDisplayName: externalIdentityDisplayName(identity),
			Claims:              claims,
			LastLoginAt:         now,
		})
		if err != nil {
			return fmt.Errorf("update identity by user: %w", err)
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lookup identity by user and provider: %w", err)
	}
	_, err = a.q.UserIdentityCreate(ctx, queries.UserIdentityCreateParams{
		ID:                  pgtype.UUID{Bytes: uuid.New(), Valid: true},
		UserID:              userID,
		Provider:            provider,
		ProviderSubject:     strings.TrimSpace(identity.Subject),
		ProviderLogin:       strings.TrimSpace(identity.Login),
		ProviderEmail:       strings.TrimSpace(identity.Email),
		ProviderDisplayName: externalIdentityDisplayName(identity),
		Claims:              claims,
		LastLoginAt:         now,
	})
	if err != nil {
		return fmt.Errorf("create identity: %w", err)
	}
	return nil
}

func (a *API) resolveGitHubOrganizationIDs(ctx context.Context, orgLogins []string) ([]pgtype.UUID, error) {
	rows, err := a.q.OrgProviderConnectionListByProvider(ctx, "github")
	if err != nil {
		return nil, fmt.Errorf("list GitHub org provider connections: %w", err)
	}
	allowed := make(map[string][]pgtype.UUID, len(rows))
	for _, row := range rows {
		slug := strings.TrimSpace(strings.ToLower(row.ExternalSlug))
		if slug == "" {
			continue
		}
		allowed[slug] = append(allowed[slug], row.OrganizationID)
	}
	seen := make(map[pgtype.UUID]struct{})
	out := make([]pgtype.UUID, 0, len(orgLogins))
	for _, login := range orgLogins {
		for _, orgID := range allowed[strings.TrimSpace(strings.ToLower(login))] {
			if _, ok := seen[orgID]; ok {
				continue
			}
			seen[orgID] = struct{}{}
			out = append(out, orgID)
		}
	}
	return out, nil
}

func (a *API) authProviderAdminJSON(ctx context.Context, r *http.Request, row queries.AuthProvider) map[string]any {
	callbackURL, _ := a.authProviderCallbackURL(ctx, r, row.Provider)
	return map[string]any{
		"provider":          row.Provider,
		"display_name":      authProviderDisplayName(row),
		"enabled":           row.Enabled,
		"configured":        authProviderReady(row),
		"client_id":         row.ClientID,
		"has_client_secret": len(row.ClientSecretCiphertext) > 0,
		"issuer_url":        row.IssuerUrl,
		"scopes":            strings.Join(extoauth.SplitScopes(row.Scopes, authProviderDefaultScopes(row.Provider)), " "),
		"callback_url":      callbackURL,
		"created_at":        timestampString(row.CreatedAt),
		"updated_at":        timestampString(row.UpdatedAt),
	}
}

func authProviderReady(row queries.AuthProvider) bool {
	if !row.Enabled || strings.TrimSpace(row.ClientID) == "" || len(row.ClientSecretCiphertext) == 0 {
		return false
	}
	if row.Provider == "oidc" && strings.TrimSpace(row.IssuerUrl) == "" {
		return false
	}
	return true
}

func authProviderDefaultDisplayName(provider string) string {
	switch provider {
	case "github":
		return "GitHub"
	case "oidc":
		return "OpenID Connect"
	default:
		return strings.ToUpper(provider)
	}
}

func authProviderDisplayName(row queries.AuthProvider) string {
	if strings.TrimSpace(row.DisplayName) != "" {
		return strings.TrimSpace(row.DisplayName)
	}
	return authProviderDefaultDisplayName(row.Provider)
}

func authProviderDefaultScopes(provider string) []string {
	switch provider {
	case "github":
		return []string{"read:user", "user:email", "read:org"}
	case "oidc":
		return []string{"openid", "profile", "email"}
	default:
		return nil
	}
}

func authProviderMetadata(raw []byte) []byte {
	if len(raw) > 0 {
		return raw
	}
	return []byte(`{}`)
}

func externalIdentityDisplayName(identity extoauth.Identity) string {
	if strings.TrimSpace(identity.DisplayName) != "" {
		return strings.TrimSpace(identity.DisplayName)
	}
	if strings.TrimSpace(identity.Login) != "" {
		return strings.TrimSpace(identity.Login)
	}
	return strings.TrimSpace(identity.Email)
}

func sanitizeReturnTo(raw, fallback string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return fallback
	}
	return raw
}

func externalAuthFailurePath(state externalAuthState) string {
	if state.Mode == "link" {
		return "/settings?tab=authentication"
	}
	return "/login"
}

func timestampString(ts pgtype.Timestamptz) string {
	if !ts.Valid {
		return ""
	}
	return ts.Time.UTC().Format(time.RFC3339)
}

func nullableTimestampString(ts pgtype.Timestamptz) *string {
	if !ts.Valid {
		return nil
	}
	v := ts.Time.UTC().Format(time.RFC3339)
	return &v
}

func (a *API) authProviderClientSecret(row queries.AuthProvider) (string, error) {
	if a.cfg == nil {
		return "", fmt.Errorf("config manager unavailable")
	}
	plain, err := a.cfg.DecryptBytes(row.ClientSecretCiphertext)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(plain)), nil
}

func (a *API) setExternalAuthState(w http.ResponseWriter, r *http.Request, state externalAuthState) error {
	if a.cfg == nil {
		return fmt.Errorf("config manager unavailable")
	}
	b, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal auth state: %w", err)
	}
	cipher, err := a.cfg.EncryptBytes(b)
	if err != nil {
		return fmt.Errorf("encrypt auth state: %w", err)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     externalAuthStateCookieName,
		Value:    base64.RawURLEncoding.EncodeToString(cipher),
		Path:     "/api/auth/providers",
		MaxAge:   int(authStateTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   auth.RequestUsesHTTPS(r),
	})
	return nil
}

func (a *API) readExternalAuthState(r *http.Request) (externalAuthState, error) {
	cookie, err := r.Cookie(externalAuthStateCookieName)
	if err != nil {
		return externalAuthState{}, err
	}
	if a.cfg == nil {
		return externalAuthState{}, fmt.Errorf("config manager unavailable")
	}
	cipher, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(cookie.Value))
	if err != nil {
		return externalAuthState{}, err
	}
	plain, err := a.cfg.DecryptBytes(cipher)
	if err != nil {
		return externalAuthState{}, err
	}
	var state externalAuthState
	if err := json.Unmarshal(plain, &state); err != nil {
		return externalAuthState{}, err
	}
	if state.ExpiresAt > 0 && time.Now().Unix() > state.ExpiresAt {
		return externalAuthState{}, fmt.Errorf("authentication state expired")
	}
	return state, nil
}

func (a *API) clearExternalAuthState(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     externalAuthStateCookieName,
		Value:    "",
		Path:     "/api/auth/providers",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   auth.RequestUsesHTTPS(r),
	})
}

func (a *API) authProviderCallbackURL(ctx context.Context, r *http.Request, provider string) (string, error) {
	base, err := a.publicBaseURL(ctx)
	if err != nil {
		return "", err
	}
	if base == "" {
		base = requestOrigin(r)
	}
	return strings.TrimRight(base, "/") + "/api/auth/providers/" + provider + "/callback", nil
}

func (a *API) dashboardRedirectURL(ctx context.Context, r *http.Request, path string, values map[string]string) string {
	base := requestOrigin(r)
	if publicBase, err := a.publicBaseURL(ctx); err == nil && publicBase != "" {
		base = publicBase
	}
	if dashHost, err := a.dashboardPublicHost(ctx); err == nil && dashHost != "" {
		scheme := "https"
		if publicBase, err := a.publicBaseURL(ctx); err == nil && publicBase != "" {
			if u, err := url.Parse(publicBase); err == nil && u.Scheme != "" {
				scheme = u.Scheme
			}
		}
		base = scheme + "://" + dashHost
	}
	u, err := url.Parse(strings.TrimRight(base, "/") + sanitizeReturnTo(path, "/"))
	if err != nil {
		return strings.TrimRight(base, "/") + sanitizeReturnTo(path, "/")
	}
	query := u.Query()
	for key, value := range values {
		if strings.TrimSpace(value) != "" {
			query.Set(key, value)
		}
	}
	u.RawQuery = query.Encode()
	return u.String()
}

func (a *API) redirectExternalAuthError(w http.ResponseWriter, r *http.Request, path string, err error) {
	http.Redirect(w, r, a.dashboardRedirectURL(r.Context(), r, path, map[string]string{"auth_error": err.Error()}), http.StatusFound)
}

func (a *API) redirectExternalAuthSuccess(w http.ResponseWriter, r *http.Request, path string, params map[string]string) {
	http.Redirect(w, r, a.dashboardRedirectURL(r.Context(), r, path, params), http.StatusFound)
}

func requestOrigin(r *http.Request) string {
	scheme := "http"
	if auth.RequestUsesHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
