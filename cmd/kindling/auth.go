package main

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kindlingvm/kindling/internal/auth"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/spf13/cobra"
)

func adminAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "User administration (requires database access; break-glass)",
	}
	cmd.AddCommand(authCreateSuperuserCmd())
	return cmd
}

func authCreateSuperuserCmd() *cobra.Command {
	var (
		email        string
		password     string
		displayName  string
		dbURL        string
		revokeOthers bool
	)

	cmd := &cobra.Command{
		Use:   "create-superuser",
		Short: "Create or update a platform admin and grant owner on every organization",
		Long: `Creates a dashboard user (or updates password if the email exists) and sets their
membership role to owner for all Kindling organizations, while marking them as a platform admin.
Intended for recovery or
initial privileged access; requires a PostgreSQL DSN (same as other kindling commands).

Use --revoke-other-sessions to invalidate existing browser sessions for this user.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			email = strings.TrimSpace(strings.ToLower(email))
			if email == "" || !strings.Contains(email, "@") {
				return fmt.Errorf("valid --email is required")
			}
			if len(password) < 8 {
				return fmt.Errorf("password must be at least 8 characters")
			}
			url, err := resolveDBURL(dbURL)
			if err != nil {
				return fmt.Errorf("resolve database URL: %w", err)
			}
			pool, err := pgxpool.New(ctx, url)
			if err != nil {
				return fmt.Errorf("db connect: %w", err)
			}
			defer pool.Close()

			q := queries.New(pool)
			hash, err := auth.HashPassword(password)
			if err != nil {
				return fmt.Errorf("hash password: %w", err)
			}

			displayName = strings.TrimSpace(displayName)

			var userID uuid.UUID
			u, err := q.UserByEmail(ctx, email)
			if err != nil {
				if err != pgx.ErrNoRows {
					return fmt.Errorf("lookup user by email: %w", err)
				}
				userID = uuid.New()
				dn := displayName
				_, err = q.UserCreate(ctx, queries.UserCreateParams{
					ID:              pgtype.UUID{Bytes: userID, Valid: true},
					Email:           email,
					PasswordHash:    hash,
					DisplayName:     dn,
					IsPlatformAdmin: true,
				})
				if err != nil {
					return fmt.Errorf("create user: %w", err)
				}
				fmt.Printf("Created user %s (%s)\n", email, userID)
			} else {
				userID = uuid.UUID(u.ID.Bytes)
				if err := q.UserUpdatePasswordHash(ctx, queries.UserUpdatePasswordHashParams{
					ID:           u.ID,
					PasswordHash: hash,
				}); err != nil {
					return fmt.Errorf("update password: %w", err)
				}
				if displayName != "" {
					if err := q.UserUpdateDisplayName(ctx, queries.UserUpdateDisplayNameParams{
						ID:          u.ID,
						DisplayName: displayName,
					}); err != nil {
						return fmt.Errorf("update display name: %w", err)
					}
				}
				fmt.Printf("Updated password for existing user %s (%s)\n", email, userID)
			}
			if err := q.UserSetPlatformAdmin(ctx, queries.UserSetPlatformAdminParams{
				ID:              pgtype.UUID{Bytes: userID, Valid: true},
				IsPlatformAdmin: true,
			}); err != nil {
				return fmt.Errorf("grant platform admin: %w", err)
			}

			orgs, err := q.OrganizationsListAll(ctx)
			if err != nil {
				return fmt.Errorf("list organizations: %w", err)
			}
			if len(orgs) == 0 {
				return fmt.Errorf("no organizations in database — run kindling serve once to migrate schema")
			}

			for _, o := range orgs {
				if err := q.OrganizationMembershipUpsertOwner(ctx, queries.OrganizationMembershipUpsertOwnerParams{
					ID:             pgtype.UUID{Bytes: uuid.New(), Valid: true},
					OrganizationID: o.ID,
					UserID:         pgtype.UUID{Bytes: userID, Valid: true},
				}); err != nil {
					return fmt.Errorf("grant owner on %s: %w", o.Slug, err)
				}
				fmt.Printf("  owner @ %s (%s)\n", o.Name, o.Slug)
			}

			if revokeOthers {
				if err := q.UserSessionDeleteAllForUser(ctx, pgtype.UUID{Bytes: userID, Valid: true}); err != nil {
					return fmt.Errorf("revoke sessions: %w", err)
				}
				fmt.Println("Revoked all sessions for this user (sign in again in the browser).")
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&email, "email", "", "Login email (required)")
	cmd.Flags().StringVar(&password, "password", "", "Password (required, min 8 characters)")
	cmd.Flags().StringVar(&displayName, "display-name", "", "Display name (optional; set on create or overwrite when updating)")
	cmd.Flags().StringVar(&dbURL, "database-url", "", "PostgreSQL connection string (optional; uses same discovery as serve)")
	cmd.Flags().BoolVar(&revokeOthers, "revoke-other-sessions", true, "Delete DB sessions for this user so old cookies stop working")
	cmd.MarkFlagRequired("email")
	cmd.MarkFlagRequired("password")

	return cmd
}
