// Package oci wraps OCI image operations without the Docker CLI: build (buildah),
// registry push, and exporting image rootfs via skopeo + umoci for microVM runtimes.
package oci

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Auth holds optional registry credentials for skopeo/buildah.
type Auth struct {
	Username string
	Password string
}

// ExportImageRootfs pulls an image by ref and unpacks its root filesystem into destDir.
// It tries containers-storage first (local buildah images), then docker:// for
// registry-backed refs. destDir is removed and recreated before unpack.
func ExportImageRootfs(ctx context.Context, imageRef, destDir string, auth *Auth) error {
	if err := os.RemoveAll(destDir); err != nil {
		return fmt.Errorf("remove dest dir: %w", err)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("mkdir dest: %w", err)
	}

	tmp, err := os.MkdirTemp("", "kindling-oci-export-*")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	ociLayout := filepath.Join(tmp, "oci")
	if err := PullToOCILayout(ctx, imageRef, ociLayout, auth); err != nil {
		return fmt.Errorf("pull OCI layout: %w", err)
	}

	bundleDir := filepath.Join(tmp, "bundle")
	if err := UmociUnpack(ctx, ociLayout+":latest", bundleDir); err != nil {
		return fmt.Errorf("umoci unpack: %w", err)
	}

	rootfs := filepath.Join(bundleDir, "rootfs")
	if st, err := os.Stat(rootfs); err != nil || !st.IsDir() {
		return fmt.Errorf("umoci unpack missing rootfs at %s", rootfs)
	}

	if err := copyRootfsPreservingMetadata(rootfs, destDir); err != nil {
		return fmt.Errorf("copy rootfs: %w", err)
	}
	return nil
}

// PullToOCILayout copies imageRef into an OCI layout directory at ociLayoutDir (tag :latest).
// Tries containers-storage first, then docker:// (with auth for registry pulls).
// Each attempt starts from an empty layout directory so a failed first pull cannot break the second.
func PullToOCILayout(ctx context.Context, imageRef, ociLayoutDir string, auth *Auth) error {
	ociTagged := "oci:" + ociLayoutDir + ":latest"
	var lastErr error
	// buildah tags images as localhost/<ref> when given a short name (see `buildah images`).
	candidates := []string{
		"containers-storage:" + imageRef,
		"containers-storage:localhost/" + imageRef,
		"docker://" + imageRef,
	}
	for _, src := range candidates {
		// A failed containers-storage pull can leave a partial layout; always reset before each try.
		if err := os.RemoveAll(ociLayoutDir); err != nil {
			return fmt.Errorf("reset oci layout dir: %w", err)
		}
		if err := os.MkdirAll(ociLayoutDir, 0o755); err != nil {
			return fmt.Errorf("mkdir oci layout: %w", err)
		}
		isDocker := strings.HasPrefix(src, "docker://")
		lastErr = skopeoCopy(ctx, src, ociTagged, isDocker, auth)
		if lastErr == nil {
			return nil
		}
	}
	return fmt.Errorf("skopeo copy image %q (tried containers-storage and docker): %w", imageRef, lastErr)
}

// UmociUnpack runs `umoci unpack` for imageRef (e.g. oci:/path:latest) into bundleDir.
// When the process is not UID 0, it passes --rootless so unpack works on macOS and
// unprivileged Linux (avoids lchown errors on the rootfs).
func UmociUnpack(ctx context.Context, imageRef, bundleDir string) error {
	args := []string{"unpack"}
	if umociRootless() {
		args = append(args, "--rootless")
	}
	args = append(args, "--image", imageRef, bundleDir)
	if out, err := exec.CommandContext(ctx, "umoci", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("umoci unpack: %s: %w", string(out), err)
	}
	return nil
}

func umociRootless() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	return os.Geteuid() != 0
}

func skopeoCopy(ctx context.Context, src, dest string, useSrcAuth bool, auth *Auth) error {
	args := []string{"copy"}
	// macOS skopeo otherwise selects a darwin variant from multi-arch indexes (none exist for Linux images).
	if runtime.GOOS == "darwin" && strings.HasPrefix(src, "docker://") {
		args = append(args, "--override-os", "linux")
		switch runtime.GOARCH {
		case "arm64":
			args = append(args, "--override-arch", "arm64")
		case "amd64", "386":
			args = append(args, "--override-arch", "amd64")
		}
	}

	// Pass registry credentials via a temporary authfile (mode 0600) instead of
	// --src-creds so they are never visible in /proc/<pid>/cmdline.
	var authFilePath string
	if useSrcAuth && auth != nil && auth.Username != "" {
		af, err := WriteAuthFile(src, auth)
		if err != nil {
			return fmt.Errorf("create auth file: %w", err)
		}
		defer os.Remove(af)
		authFilePath = af
		args = append(args, "--authfile", authFilePath)
	}

	args = append(args, src, dest)
	// Rootless skopeo needs the same user namespace as buildah (see `buildah unshare`); otherwise
	// `containers-storage:` reads fail with "Error during unshare(...): Operation not permitted".
	var cmd *exec.Cmd
	if runtime.GOOS == "linux" && os.Geteuid() != 0 && strings.HasPrefix(src, "containers-storage:") {
		if _, err := exec.LookPath("buildah"); err == nil {
			wrapped := append([]string{"unshare", "skopeo"}, args...)
			cmd = exec.CommandContext(ctx, "buildah", wrapped...)
		}
	}
	if cmd == nil {
		cmd = exec.CommandContext(ctx, "skopeo", args...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", redactCredentials(string(out), auth), err)
	}
	return nil
}

// copyRootfsPreservingMetadata copies unpacked OCI rootfs into destDir.
// Uses os.CopyFS so behavior does not depend on which cp (GNU vs busybox) appears first on PATH when invoked from Go.
func copyRootfsPreservingMetadata(srcRoot, dstRoot string) error {
	return os.CopyFS(dstRoot, os.DirFS(srcRoot))
}

// BuildEngine names the CLI used for image builds.
type BuildEngine string

const (
	EngineBuildah BuildEngine = "buildah"
)

// DetectBuildEngine returns buildah when available in PATH.
func DetectBuildEngine() (BuildEngine, error) {
	if _, err := exec.LookPath("buildah"); err == nil {
		return EngineBuildah, nil
	}
	if runtime.GOOS == "darwin" {
		return "", fmt.Errorf("buildah not on PATH: OCI image builds need Linux + buildah (not available from Homebrew on macOS); install on a Kindling Linux host via sudo ./contrib/install-host-deps.sh or use make remote-provision")
	}
	return "", fmt.Errorf("buildah not on PATH: install with sudo ./contrib/install-host-deps.sh or your distro package manager (e.g. apt install buildah)")
}

// BuildDockerfile runs an OCI image build in buildDir, tagging the result as imageRef.
// dockerfilePath is relative to buildDir. logLine is called for each stdout/stderr line.
// It enables host-local layer caching for buildah (--layers) so repeated builds on the same host can reuse layers.
func BuildDockerfile(ctx context.Context, engine BuildEngine, buildDir, imageRef, dockerfilePath string, logLine func(string)) error {
	return BuildDockerfileWithOpts(ctx, engine, buildDir, imageRef, dockerfilePath, BuildDockerfileOpts{LocalLayerCache: true}, logLine)
}

// BuildDockerfileOpts configures image build behavior.
type BuildDockerfileOpts struct {
	// LocalLayerCache enables buildah --layers for intermediate layer reuse on the build host.
	LocalLayerCache bool
}

// BuildDockerfileWithOpts is like BuildDockerfile but allows tuning build behavior.
func BuildDockerfileWithOpts(ctx context.Context, engine BuildEngine, buildDir, imageRef, dockerfilePath string, opts BuildDockerfileOpts, logLine func(string)) error {
	var cmd *exec.Cmd
	switch engine {
	case EngineBuildah:
		cmd = exec.CommandContext(ctx, "buildah", BuildahBudArgs(imageRef, dockerfilePath, opts.LocalLayerCache)...)
	default:
		return fmt.Errorf("unsupported build engine: %q", engine)
	}
	cmd.Dir = buildDir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s build: %w", engine, err)
	}
	var wg sync.WaitGroup
	stream := func(r io.Reader) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			if logLine != nil && sc.Text() != "" {
				logLine(sc.Text())
			}
		}
	}
	wg.Add(2)
	go stream(stdout)
	go stream(stderr)
	err = cmd.Wait()
	wg.Wait()
	if err != nil {
		return fmt.Errorf("%s build: %w", engine, err)
	}
	return nil
}

// PushImage pushes a local tag to docker://imageRef using registry credentials when set.
// Credentials are passed via a temporary authfile (mode 0600) instead of CLI arguments.
func PushImage(ctx context.Context, engine BuildEngine, imageRef string, auth *Auth) error {
	switch engine {
	case EngineBuildah:
		var authFilePath string
		if auth != nil && auth.Username != "" {
			af, err := WriteAuthFile(imageRef, auth)
			if err != nil {
				return fmt.Errorf("create auth file for push: %w", err)
			}
			defer os.Remove(af)
			authFilePath = af
		}
		args := BuildahPushArgs(imageRef, authFilePath)
		out, err := exec.CommandContext(ctx, "buildah", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("buildah push: %s: %w", redactCredentials(string(out), auth), err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported build engine: %q", engine)
	}
}
