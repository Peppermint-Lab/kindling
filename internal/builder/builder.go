// Package builder handles build orchestration. Each build runs in an isolated
// dind (Docker-in-Docker) microVM. Source is downloaded as a GitHub tarball,
// framework is auto-detected if no Dockerfile is present, and the image is
// built with docker buildx and pushed to a registry.
package builder

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

// Config holds builder configuration.
type Config struct {
	// GitHubToken is used to download source tarballs.
	// TODO: replace with GitHub App installation tokens.
	GitHubToken string

	// RegistryURL is the OCI registry to push built images to.
	RegistryURL string

	// RegistryUsername for registry auth.
	RegistryUsername string

	// RegistryPassword for registry auth.
	RegistryPassword string
}

// Builder orchestrates builds via reconciliation.
type Builder struct {
	cfg      Config
	q        *queries.Queries
	serverID uuid.UUID
}

// New creates a new builder.
func New(cfg Config, q *queries.Queries, serverID uuid.UUID) *Builder {
	return &Builder{cfg: cfg, q: q, serverID: serverID}
}

// ReconcileBuild is the reconcile function for builds.
func (b *Builder) ReconcileBuild(ctx context.Context, buildID uuid.UUID) error {
	build, err := b.q.BuildFirstByID(ctx, uuidToPgtype(buildID))
	if err != nil {
		return fmt.Errorf("fetch build: %w", err)
	}

	// Skip terminal states.
	if build.Status == "successful" || build.Status == "failed" {
		return nil
	}

	// Stuck build detection (15 min timeout).
	if build.Status == "building" && build.BuildingAt.Valid {
		if time.Since(build.BuildingAt.Time) > 15*time.Minute {
			slog.Warn("build stuck, marking failed", "build_id", buildID)
			return b.q.BuildMarkFailed(ctx, build.ID)
		}
	}

	// Claim the build lease.
	build, err = b.q.BuildClaimLease(ctx, queries.BuildClaimLeaseParams{
		ID:           build.ID,
		ProcessingBy: uuidToPgtype(b.serverID),
	})
	if err != nil {
		slog.Debug("build lease not available", "build_id", buildID)
		return nil // another server has it
	}
	defer b.releaseLease(buildID)

	// Mark as building.
	if build.Status == "pending" {
		if err := b.q.BuildMarkBuilding(ctx, queries.BuildMarkBuildingParams{
			ID:   build.ID,
			VmID: pgtype.UUID{}, // no VM yet — builds run on the host for now
		}); err != nil {
			return fmt.Errorf("mark building: %w", err)
		}
	}

	// Get project info.
	project, err := b.q.ProjectFirstByID(ctx, build.ProjectID)
	if err != nil {
		return fmt.Errorf("fetch project: %w", err)
	}

	// Log build start.
	b.log(ctx, build.ID, "info", fmt.Sprintf("Building %s@%s", project.GithubRepository, build.GithubCommit))

	// Download source tarball.
	tarball, err := b.downloadSource(ctx, project.GithubRepository, build.GithubCommit)
	if err != nil {
		b.log(ctx, build.ID, "error", fmt.Sprintf("Failed to download source: %v", err))
		return b.q.BuildMarkFailed(ctx, build.ID)
	}
	defer tarball.Close()

	// Prepare build context (extract tar, detect framework, inject Dockerfile if needed).
	buildContext, dockerfile, err := b.prepareBuildContext(ctx, build, tarball, project.RootDirectory, project.DockerfilePath)
	if err != nil {
		b.log(ctx, build.ID, "error", fmt.Sprintf("Failed to prepare build context: %v", err))
		return b.q.BuildMarkFailed(ctx, build.ID)
	}

	if dockerfile != "" {
		b.log(ctx, build.ID, "info", fmt.Sprintf("No Dockerfile found. Detected framework: %s", dockerfile))
	}

	// Build the Docker image on the host.
	imageTag := build.GithubCommit
	if len(imageTag) > 12 {
		imageTag = imageTag[:12]
	}
	imageRef := fmt.Sprintf("%s/%s:%s", b.cfg.RegistryURL, project.GithubRepository, imageTag)

	b.log(ctx, build.ID, "info", fmt.Sprintf("Building image: %s", imageRef))

	if err := b.runDockerBuild(ctx, build, buildContext, imageRef, project.DockerfilePath); err != nil {
		b.log(ctx, build.ID, "error", fmt.Sprintf("Docker build failed: %v", err))
		return b.q.BuildMarkFailed(ctx, build.ID)
	}

	b.log(ctx, build.ID, "info", "Docker build completed")

	// Push to registry if credentials are configured.
	if b.cfg.RegistryUsername != "" {
		b.log(ctx, build.ID, "info", "Pushing image to registry...")
		if err := b.runDockerPush(ctx, build, imageRef); err != nil {
			b.log(ctx, build.ID, "error", fmt.Sprintf("Push failed: %v", err))
			// Don't fail the build — image is still available locally.
		} else {
			b.log(ctx, build.ID, "info", "Image pushed successfully")
		}
	}

	// Create image record.
	image, err := b.q.ImageFindOrCreate(ctx, queries.ImageFindOrCreateParams{
		ID:         uuidToPgtype(uuid.New()),
		Registry:   b.cfg.RegistryURL,
		Repository: project.GithubRepository,
		Tag:        imageTag,
	})
	if err != nil {
		return fmt.Errorf("create image record: %w", err)
	}

	// Mark successful.
	if err := b.q.BuildMarkSuccessful(ctx, queries.BuildMarkSuccessfulParams{
		ID:      build.ID,
		ImageID: image.ID,
	}); err != nil {
		return fmt.Errorf("mark successful: %w", err)
	}

	b.log(ctx, build.ID, "info", "Build completed successfully")
	slog.Info("build completed", "build_id", buildID, "image_id", image.ID)

	return nil
}

func (b *Builder) downloadSource(ctx context.Context, repo, commit string) (io.ReadCloser, error) {
	// Normalize repo: strip full URL to owner/repo.
	repo = strings.TrimPrefix(repo, "https://github.com/")
	repo = strings.TrimPrefix(repo, "http://github.com/")
	repo = strings.TrimPrefix(repo, "github.com/")
	repo = strings.TrimSuffix(repo, ".git")

	url := fmt.Sprintf("https://api.github.com/repos/%s/tarball/%s", repo, commit)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	if b.cfg.GitHubToken != "" {
		req.Header.Set("Authorization", "Bearer "+b.cfg.GitHubToken)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("GitHub returned %d: %s", resp.StatusCode, string(body))
	}

	return resp.Body, nil
}

// prepareBuildContext extracts the GitHub tarball, detects framework, and
// injects a Dockerfile if needed. Returns the prepared tar stream and
// the detected framework name (empty if user provided their own Dockerfile).
func (b *Builder) prepareBuildContext(ctx context.Context, build queries.Build, gzipReader io.Reader, rootDir, dockerfilePath string) (io.Reader, string, error) {
	pr, pw := io.Pipe()

	detectedFramework := make(chan string, 1)

	go func() {
		defer pw.Close()

		gzr, err := gzip.NewReader(gzipReader)
		if err != nil {
			pw.CloseWithError(err)
			detectedFramework <- ""
			return
		}
		defer gzr.Close()

		tr := tar.NewReader(gzr)
		tw := tar.NewWriter(pw)
		defer tw.Close()

		var githubPrefix string
		foundDockerfile := false
		var signals frameworkSignals

		rootDirPrefix := strings.TrimPrefix(rootDir, "/")
		if rootDirPrefix != "" && !strings.HasSuffix(rootDirPrefix, "/") {
			rootDirPrefix += "/"
		}

		if dockerfilePath == "" {
			dockerfilePath = "Dockerfile"
		}

		for {
			header, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				pw.CloseWithError(err)
				detectedFramework <- ""
				return
			}

			// Strip GitHub's prefix directory.
			if githubPrefix == "" {
				parts := strings.SplitN(header.Name, "/", 2)
				if len(parts) > 1 {
					githubPrefix = parts[0] + "/"
				}
			}

			path := strings.TrimPrefix(header.Name, githubPrefix)
			if path == "" {
				continue
			}

			// Apply root directory filter.
			if rootDirPrefix != "" {
				if !strings.HasPrefix(path, rootDirPrefix) {
					continue
				}
				path = strings.TrimPrefix(path, rootDirPrefix)
				if path == "" {
					continue
				}
			}

			// Check for Dockerfile.
			if path == dockerfilePath {
				foundDockerfile = true
			}

			// Collect framework signals from top-level files.
			if !strings.Contains(path, "/") {
				switch path {
				case "nuxt.config.ts", "nuxt.config.js":
					signals.hasNuxtConfig = true
				case "next.config.ts", "next.config.js", "next.config.mjs":
					signals.hasNextConfig = true
				case "Gemfile":
					signals.hasGemfile = true
				case "Rakefile":
					signals.hasRakefile = true
				case "artisan":
					signals.hasArtisan = true
				case "go.mod":
					signals.hasGoMod = true
				}
			} else if path == "config/routes.rb" {
				signals.hasRailsRoutes = true
			}

			// Write to output tar.
			newHeader := *header
			newHeader.Name = path
			if err := tw.WriteHeader(&newHeader); err != nil {
				pw.CloseWithError(err)
				detectedFramework <- ""
				return
			}

			if header.Typeflag == tar.TypeReg {
				// Buffer certain files for framework detection.
				var buf *[]byte
				if !strings.Contains(path, "/") {
					switch path {
					case "package.json":
						buf = &signals.packageJSON
					case "Gemfile":
						buf = &signals.gemfileBytes
					case "composer.json":
						buf = &signals.composerJSON
					}
				}

				if buf != nil {
					content, err := io.ReadAll(tr)
					if err != nil {
						pw.CloseWithError(err)
						detectedFramework <- ""
						return
					}
					tw.Write(content)
					*buf = content
				} else {
					io.Copy(tw, tr)
				}
			}
		}

		// Inject Dockerfile if not found.
		if !foundDockerfile {
			framework := DetectFramework(signals)
			if framework == "" {
				pw.CloseWithError(fmt.Errorf("no Dockerfile found and could not detect framework"))
				detectedFramework <- ""
				return
			}

			content := GetDockerfile(framework)
			tw.WriteHeader(&tar.Header{
				Name:    dockerfilePath,
				Mode:    0o644,
				Size:    int64(len(content)),
				ModTime: time.Now(),
			})
			tw.Write([]byte(content))
			detectedFramework <- framework
		} else {
			detectedFramework <- ""
		}
	}()

	framework := <-detectedFramework
	return pr, framework, nil
}

func (b *Builder) log(ctx context.Context, buildID pgtype.UUID, level, message string) {
	b.q.BuildLogCreate(ctx, queries.BuildLogCreateParams{
		ID:      uuidToPgtype(uuid.New()),
		BuildID: buildID,
		Message: message,
		Level:   level,
	})
}

func (b *Builder) releaseLease(buildID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b.q.BuildReleaseLease(ctx, queries.BuildReleaseLeaseParams{
		ID:           uuidToPgtype(buildID),
		ProcessingBy: uuidToPgtype(b.serverID),
	})
}

// runDockerBuild runs `docker build` with the prepared tar context piped to stdin.
func (b *Builder) runDockerBuild(ctx context.Context, build queries.Build, buildContext io.Reader, imageRef, dockerfilePath string) error {
	if dockerfilePath == "" {
		dockerfilePath = "Dockerfile"
	}

	args := []string{"build", "-t", imageRef, "-f", dockerfilePath, "-"}
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = buildContext

	// Stream stdout/stderr to build logs.
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start docker build: %w", err)
	}

	// Stream output to build logs in background.
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.streamOutput(ctx, build.ID, stdout, "info")
		b.streamOutput(ctx, build.ID, stderr, "error")
	}()

	err := cmd.Wait()
	<-done

	if err != nil {
		return fmt.Errorf("docker build: %w", err)
	}
	return nil
}

// runDockerPush pushes the image to the registry.
func (b *Builder) runDockerPush(ctx context.Context, build queries.Build, imageRef string) error {
	cmd := exec.CommandContext(ctx, "docker", "push", imageRef)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker push: %s: %w", string(out), err)
	}
	return nil
}

// streamOutput reads lines from r and writes them as build logs.
func (b *Builder) streamOutput(ctx context.Context, buildID pgtype.UUID, r io.Reader, level string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			b.log(ctx, buildID, level, line)
		}
	}
}

func uuidToPgtype(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}
