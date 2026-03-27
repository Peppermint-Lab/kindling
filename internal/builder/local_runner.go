package builder

import (
	"context"
	"fmt"

	"github.com/kindlingvm/kindling/internal/oci"
)

// LocalBuildRunner runs buildah on the local host (Linux).
type LocalBuildRunner struct{}

// NewLocalBuildRunner returns a runner that shells out to host buildah.
func NewLocalBuildRunner() *LocalBuildRunner {
	return &LocalBuildRunner{}
}

// BuildAndPush implements BuildRunner.
func (*LocalBuildRunner) BuildAndPush(ctx context.Context, run BuildRun) error {
	engine, err := oci.DetectBuildEngine()
	if err != nil {
		return fmt.Errorf("detect build engine: %w", err)
	}
	if run.LogLine != nil {
		run.LogLine(fmt.Sprintf("Building image with %s: %s", engine, run.ImageRef))
		run.LogLine("Using local OCI layer cache on this host")
	}
	if err := oci.BuildDockerfile(ctx, engine, run.BuildDir, run.ImageRef, run.DockerfilePath, run.LogLine); err != nil {
		return fmt.Errorf("build dockerfile: %w", err)
	}
	if run.LogLine != nil {
		run.LogLine("OCI build completed")
	}
	if run.RegistryUsername != "" {
		if run.LogLine != nil {
			run.LogLine("Pushing image to registry...")
		}
		auth := &oci.Auth{Username: run.RegistryUsername, Password: run.RegistryPassword}
		if err := oci.PushImage(ctx, engine, run.ImageRef, auth); err != nil {
			if run.LogLine != nil {
				run.LogLine(fmt.Sprintf("Push failed: %v", err))
			}
			// Same semantics as prior builder.go: push failure does not fail the build on Linux.
		} else if run.LogLine != nil {
			run.LogLine("Image pushed successfully")
		}
	}
	return nil
}
