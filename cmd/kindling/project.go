package main

import (
	"context"
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kindlingvm/kindling/internal/database/queries"
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
		name   string
		repo   string
		dbURL  string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new project",
		RunE: func(cmd *cobra.Command, args []string) error {
			dbURL = resolveDBURL(dbURL)
			pool, err := pgxpool.New(cmd.Context(), dbURL)
			if err != nil {
				return err
			}
			defer pool.Close()

			q := queries.New(pool)
			webhookSecret := ""
			if repo != "" {
				var err error
				webhookSecret, err = rpc.GenerateWebhookSecret()
				if err != nil {
					return err
				}
			}
			project, err := q.ProjectCreate(cmd.Context(), queries.ProjectCreateParams{
				ID:                   pgtype.UUID{Bytes: uuid.New(), Valid: true},
				Name:                 name,
				GithubRepository:     repo,
				GithubInstallationID: 0,
				GithubWebhookSecret:  webhookSecret,
				RootDirectory:        "/",
				DockerfilePath:       "Dockerfile",
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
			dbURL = resolveDBURL(dbURL)
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
			dbURL = resolveDBURL(dbURL)
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
		dbURL     string
	)

	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Trigger a deployment",
		RunE: func(cmd *cobra.Command, args []string) error {
			dbURL = resolveDBURL(dbURL)
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
			dep, err := q.DeploymentCreate(cmd.Context(), queries.DeploymentCreateParams{
				ID:           pgtype.UUID{Bytes: uuid.New(), Valid: true},
				ProjectID:    pgtype.UUID{Bytes: id, Valid: true},
				GithubCommit: commit,
			})
			if err != nil {
				return fmt.Errorf("create deployment: %w", err)
			}

			fmt.Printf("Deployment created: %x (commit: %s)\n", dep.ID.Bytes, commit)
			return nil
		},
	}

	cmd.Flags().StringVar(&projectID, "project", "", "Project UUID (required)")
	cmd.Flags().StringVar(&commit, "commit", "", "Git commit SHA (required)")
	cmd.Flags().StringVar(&dbURL, "database-url", "", "PostgreSQL connection string")
	cmd.MarkFlagRequired("project")
	cmd.MarkFlagRequired("commit")

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
			dbURL = resolveDBURL(dbURL)
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

func resolveDBURL(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://kindling:kindling@localhost:5432/kindling?sslmode=disable"
}
