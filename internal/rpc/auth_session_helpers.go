package rpc

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/auth"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

const disabledPasswordHash = "!"

func (a *API) issueSessionForUser(ctx context.Context, user queries.User) ([]byte, queries.Organization, string, []queries.Organization, error) {
	orgs, err := a.q.OrganizationsForUser(ctx, user.ID)
	if err != nil {
		return nil, queries.Organization{}, "", nil, err
	}
	if len(orgs) == 0 {
		return nil, queries.Organization{}, "", nil, fmt.Errorf("user has no organization memberships")
	}
	org := orgs[0]
	role := ""
	for _, candidate := range orgs {
		mem, err := a.q.OrganizationMembershipByUserAndOrg(ctx, queries.OrganizationMembershipByUserAndOrgParams{
			UserID:         user.ID,
			OrganizationID: candidate.ID,
		})
		if err != nil {
			return nil, queries.Organization{}, "", nil, err
		}
		if role == "" {
			org = candidate
			role = mem.Role
		}
		if orgRoleCanManage(mem.Role) {
			org = candidate
			role = mem.Role
			break
		}
	}
	if role == "" {
		return nil, queries.Organization{}, "", nil, fmt.Errorf("user has no organization memberships")
	}
	rawTok, err := auth.NewSessionToken()
	if err != nil {
		return nil, queries.Organization{}, "", nil, err
	}
	_, err = a.q.UserSessionCreate(ctx, queries.UserSessionCreateParams{
		ID:                    pgtype.UUID{Bytes: uuid.New(), Valid: true},
		UserID:                user.ID,
		TokenHash:             auth.HashSessionToken(rawTok),
		CurrentOrganizationID: org.ID,
		ExpiresAt:             pgtype.Timestamptz{Time: auth.SessionDBExpiry(), Valid: true},
	})
	if err != nil {
		return nil, queries.Organization{}, "", nil, err
	}
	return rawTok, org, role, orgs, nil
}

func (a *API) createOwnedOrganizationForUser(ctx context.Context, user queries.User, preferredName, preferredSlug string) (queries.Organization, error) {
	name := strings.TrimSpace(preferredName)
	if name == "" {
		name = user.DisplayName
	}
	if strings.TrimSpace(name) == "" {
		name = user.Email
	}
	if name == "" {
		name = "Workspace"
	}
	slugBase := normalizeOrgSlug(preferredSlug)
	if slugBase == "" {
		slugBase = normalizeOrgSlug(name)
	}
	if slugBase == "" {
		slugBase = "workspace"
	}
	for i := 0; i < 20; i++ {
		slug := slugBase
		if i > 0 {
			slug = fmt.Sprintf("%s-%d", slugBase, i+1)
		}
		org, err := a.q.OrganizationCreate(ctx, queries.OrganizationCreateParams{
			ID:   auth.PgUUID(uuid.New()),
			Name: name,
			Slug: slug,
		})
		if err != nil {
			if isPgUniqueViolation(err) {
				continue
			}
			return queries.Organization{}, err
		}
		if err := a.q.OrganizationMembershipUpsertOwner(ctx, queries.OrganizationMembershipUpsertOwnerParams{
			ID:             auth.PgUUID(uuid.New()),
			OrganizationID: org.ID,
			UserID:         user.ID,
		}); err != nil {
			return queries.Organization{}, err
		}
		return org, nil
	}
	return queries.Organization{}, fmt.Errorf("could not allocate unique workspace slug")
}

func normalizeOrgSlug(raw string) string {
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
