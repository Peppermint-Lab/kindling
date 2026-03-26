package builder

import "context"

// BuildRun carries inputs for a single OCI image build and optional registry push.
type BuildRun struct {
	BuildDir         string
	ImageRef         string
	DockerfilePath   string
	RegistryUsername string
	RegistryPassword string
	// LogLine is called for each line of build/push output (stdout/stderr).
	LogLine func(line string)
}

// BuildRunner executes BuildRun (build + optional push). Linux uses host buildah;
// macOS uses a Kindling-managed Apple VZ builder VM.
type BuildRunner interface {
	BuildAndPush(ctx context.Context, run BuildRun) error
}
