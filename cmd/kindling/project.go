package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kindlingvm/kindling/internal/bootstrap"
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/githubapi"
	"github.com/kindlingvm/kindling/internal/rpc"
	"github.com/spf13/cobra"
)

func projectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage projects",
	}

	cmd.AddCommand(projectCreateCmd())
	cmd.AddCommand(projectListCmd())
	cmd.AddCommand(projectDeleteCmd())

	return cmd
}

func projectCreateCmd() *cobra.Command {
	var (
		name           string
		repo           string
		rootDirectory  string
		dockerfilePath string
		dbURL          string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new project",
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			dbURL, err = resolveDBURL(dbURL)
			if err != nil {
				return err
			}
			pool, err := pgxpool.New(cmd.Context(), dbURL)
			if err != nil {
				return err
			}
			defer pool.Close()

			q := queries.New(pool)
			webhookSecret := ""
			if repo != "" {
				var genErr error
				webhookSecret, genErr = rpc.GenerateWebhookSecret()
				if genErr != nil {
					return genErr
				}
			}
			rd := strings.TrimSpace(rootDirectory)
			if rd == "" {
				rd = "/"
			} else if !strings.HasPrefix(rd, "/") {
				rd = "/" + rd
			}
			dfp := strings.TrimSpace(dockerfilePath)
			if dfp == "" {
				dfp = "Dockerfile"
			}
			project, err := q.ProjectCreate(cmd.Context(), queries.ProjectCreateParams{
				ID:                   pgtype.UUID{Bytes: uuid.New(), Valid: true},
				Name:                 name,
				GithubRepository:     repo,
				GithubInstallationID: 0,
				GithubWebhookSecret:  webhookSecret,
				RootDirectory:        rd,
				DockerfilePath:       dfp,
				DesiredInstanceCount: 1,
			})
			if err != nil {
				return fmt.Errorf("create project: %w", err)
			}

			fmt.Printf("Project created: %s (id: %x)\n", project.Name, project.ID.Bytes)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Project name (required)")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repository (owner/repo)")
	cmd.Flags().StringVar(&rootDirectory, "root-directory", "/", "Subdirectory in the repo used as build context (e.g. /web/landing)")
	cmd.Flags().StringVar(&dockerfilePath, "dockerfile", "Dockerfile", "Path to Dockerfile relative to the build context root")
	cmd.Flags().StringVar(&dbURL, "database-url", "", "PostgreSQL connection string")
	cmd.MarkFlagRequired("name")

	return cmd
}

func projectListCmd() *cobra.Command {
	var dbURL string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			dbURL, err = resolveDBURL(dbURL)
			if err != nil {
				return err
			}
			pool, err := pgxpool.New(cmd.Context(), dbURL)
			if err != nil {
				return err
			}
			defer pool.Close()

			q := queries.New(pool)
			projects, err := q.ProjectFindAll(cmd.Context())
			if err != nil {
				return err
			}

			if len(projects) == 0 {
				fmt.Println("No projects found.")
				return nil
			}

			for _, p := range projects {
				fmt.Printf("%-20s %-40s %x\n", p.Name, p.GithubRepository, p.ID.Bytes)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&dbURL, "database-url", "", "PostgreSQL connection string")
	return cmd
}

func projectDeleteCmd() *cobra.Command {
	var (
		projectID string
		dbURL     string
	)

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a project",
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			dbURL, err = resolveDBURL(dbURL)
			if err != nil {
				return err
			}
			pool, err := pgxpool.New(cmd.Context(), dbURL)
			if err != nil {
				return err
			}
			defer pool.Close()

			id, err := uuid.Parse(projectID)
			if err != nil {
				return fmt.Errorf("invalid project ID: %w", err)
			}

			q := queries.New(pool)
			if err := q.ProjectDelete(cmd.Context(), pgtype.UUID{Bytes: id, Valid: true}); err != nil {
				return fmt.Errorf("delete project: %w", err)
			}

			fmt.Println("Project deleted.")
			return nil
		},
	}

	cmd.Flags().StringVar(&projectID, "id", "", "Project UUID (required)")
	cmd.Flags().StringVar(&dbURL, "database-url", "", "PostgreSQL connection string")
	cmd.MarkFlagRequired("id")

	return cmd
}

func deployCmd() *cobra.Command {
	var (
		projectID string
		commit    string
		ref       string
		dbURL     string
	)

	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Trigger a deployment",
		Long: `Trigger a deployment for a project.

Pass --commit with a SHA/branch the builder can fetch, or omit --commit to resolve
the tip of a branch via the GitHub API (default branch, or the branch from --ref).
Private repositories need github_token stored encrypted in cluster_secrets (see kindling config import-env).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			dbURL, err = resolveDBURL(dbURL)
			if err != nil {
				return err
			}
			pool, err := pgxpool.New(cmd.Context(), dbURL)
			if err != nil {
				return err
			}
			defer pool.Close()

			id, err := uuid.Parse(projectID)
			if err != nil {
				return fmt.Errorf("invalid project ID: %w", err)
			}

			q := queries.New(pool)
			resolved := commit
			if strings.TrimSpace(resolved) == "" {
				p, err := q.ProjectFirstByID(cmd.Context(), pgtype.UUID{Bytes: id, Valid: true})
				if err != nil {
					return fmt.Errorf("project not found: %w", err)
				}
				repo := strings.TrimSpace(p.GithubRepository)
				if repo == "" {
					return fmt.Errorf("project has no github_repository; pass --commit")
				}
				ghTok, err := config.GitHubTokenFromPool(cmd.Context(), pool)
				if err != nil {
					return fmt.Errorf("github token from db: %w", err)
				}
				sha, usedRef, err := githubapi.ResolveCommit(cmd.Context(), nil, strings.TrimSpace(ghTok), repo, ref)
				if err != nil {
					return fmt.Errorf("resolve GitHub ref: %w", err)
				}
				resolved = sha
				short := resolved
				if len(short) > 7 {
					short = short[:7]
				}
				fmt.Fprintf(os.Stderr, "Resolved %s (%s) -> %s\n", githubapi.NormalizeRepo(repo), usedRef, short)
			}

			dep, err := q.DeploymentCreate(cmd.Context(), queries.DeploymentCreateParams{
				ID:           pgtype.UUID{Bytes: uuid.New(), Valid: true},
				ProjectID:    pgtype.UUID{Bytes: id, Valid: true},
				GithubCommit: resolved,
			})
			if err != nil {
				return fmt.Errorf("create deployment: %w", err)
			}

			fmt.Printf("Deployment created: %x (commit: %s)\n", dep.ID.Bytes, resolved)
			return nil
		},
	}

	cmd.Flags().StringVar(&projectID, "project", "", "Project UUID (required)")
	cmd.Flags().StringVar(&commit, "commit", "", "Git commit SHA or ref for the builder tarball (optional; uses GitHub API when omitted)")
	cmd.Flags().StringVar(&ref, "ref", "", "Branch or tag when resolving via GitHub API without --commit (default: repo default branch)")
	cmd.Flags().StringVar(&dbURL, "database-url", "", "PostgreSQL connection string")
	cmd.MarkFlagRequired("project")

	return cmd
}

func logsCmd() *cobra.Command {
	var (
		deploymentID string
		dbURL        string
	)

	cmd := &cobra.Command{
		Use:   "logs",
		Short: "View build logs for a deployment",
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			dbURL, err = resolveDBURL(dbURL)
			if err != nil {
				return err
			}
			pool, err := pgxpool.New(cmd.Context(), dbURL)
			if err != nil {
				return err
			}
			defer pool.Close()

			id, err := uuid.Parse(deploymentID)
			if err != nil {
				return fmt.Errorf("invalid deployment ID: %w", err)
			}

			q := queries.New(pool)

			// Get deployment to find build ID.
			dep, err := q.DeploymentFirstByID(context.Background(), pgtype.UUID{Bytes: id, Valid: true})
			if err != nil {
				return fmt.Errorf("deployment not found: %w", err)
			}

			if !dep.BuildID.Valid {
				fmt.Println("No build associated with this deployment.")
				return nil
			}

			logs, err := q.BuildLogsByBuildID(cmd.Context(), dep.BuildID)
			if err != nil {
				return err
			}

			if len(logs) == 0 {
				fmt.Println("No build logs yet.")
				return nil
			}

			for _, l := range logs {
				fmt.Printf("[%s] %s %s\n", l.CreatedAt.Time.Format("15:04:05"), l.Level, l.Message)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&deploymentID, "deployment", "", "Deployment UUID (required)")
	cmd.Flags().StringVar(&dbURL, "database-url", "", "PostgreSQL connection string")
	cmd.MarkFlagRequired("deployment")

	return cmd
}

func resolveDBURL(flagValue string) (string, error) {
	return bootstrap.ResolvePostgresDSN(flagValue)
}
