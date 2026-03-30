//go:build linux

package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"

	"github.com/creack/pty"
	"github.com/google/uuid"
)

func (r *CrunRuntime) crunInstanceForAccess(id uuid.UUID) (*crunInstance, error) {
	r.mu.Lock()
	ci, ok := r.instances[id]
	r.mu.Unlock()
	if !ok {
		return nil, ErrInstanceNotRunning
	}
	return ci, nil
}

// ExecGuest runs argv inside the container via `crun exec`.
func (r *CrunRuntime) ExecGuest(ctx context.Context, id uuid.UUID, argv []string, cwd string, env []string) (GuestExecResult, error) {
	if len(argv) == 0 {
		return GuestExecResult{}, fmt.Errorf("exec argv empty")
	}
	if _, err := r.crunInstanceForAccess(id); err != nil {
		return GuestExecResult{}, err
	}
	args := []string{"exec"}
	if cwd != "" {
		args = append(args, "--cwd", cwd)
	}
	for _, e := range env {
		args = append(args, "--env", e)
	}
	args = append(args, crunInstContainerID(id))
	args = append(args, argv...)
	cmd := exec.CommandContext(ctx, "crun", args...)
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			return GuestExecResult{}, err
		}
	}
	return GuestExecResult{ExitCode: exitCode, Output: string(out)}, nil
}

// ReadGuestFile reads filePath inside the container via shell `cat` with quoting.
func (r *CrunRuntime) ReadGuestFile(ctx context.Context, id uuid.UUID, filePath string) ([]byte, error) {
	if filePath == "" {
		return nil, fmt.Errorf("empty path")
	}
	// Shell quote so odd paths remain a single argument.
	q := strconv.Quote(filePath)
	res, err := r.ExecGuest(ctx, id, []string{"/bin/sh", "-c", "cat " + q}, "/", nil)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("read guest file: exit %d: %s", res.ExitCode, strings.TrimSpace(res.Output))
	}
	return []byte(res.Output), nil
}

// WriteGuestFile writes data to filePath inside the container using `crun exec -i … tee`.
func (r *CrunRuntime) WriteGuestFile(ctx context.Context, id uuid.UUID, filePath string, data []byte) error {
	if filePath == "" {
		return fmt.Errorf("empty path")
	}
	if _, err := r.crunInstanceForAccess(id); err != nil {
		return err
	}
	args := []string{"exec", "-i", crunInstContainerID(id), "tee", filePath}
	cmd := exec.CommandContext(ctx, "crun", args...)
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.CombinedOutput()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(out) > 0 {
			return fmt.Errorf("write guest file: %w: %s", err, string(out))
		}
		return err
	}
	return nil
}

// StreamGuest runs an interactive session using `crun exec -t`.
func (r *CrunRuntime) StreamGuest(ctx context.Context, id uuid.UUID, argv []string, cwd string, env []string) (io.ReadWriteCloser, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("stream argv empty")
	}
	if _, err := r.crunInstanceForAccess(id); err != nil {
		return nil, err
	}
	args := []string{"exec", "-t"}
	if cwd != "" {
		args = append(args, "--cwd", cwd)
	}
	for _, e := range env {
		args = append(args, "--env", e)
	}
	args = append(args, crunInstContainerID(id))
	args = append(args, argv...)
	cmd := exec.CommandContext(ctx, "crun", args...)
	master, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	return newShellwirePTYBridge(master, cmd), nil
}

// ConnectGuestTCP dials TCP inside the container network namespace (e.g. 127.0.0.1:22 for sshd).
func (r *CrunRuntime) ConnectGuestTCP(ctx context.Context, id uuid.UUID, port int) (io.ReadWriteCloser, error) {
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid port %d", port)
	}
	ci, err := r.crunInstanceForAccess(id)
	if err != nil {
		return nil, err
	}
	if ci.mainPID <= 0 {
		return nil, ErrInstanceNotRunning
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	return dialTCPInPIDNetworkNamespace(ctx, ci.mainPID, addr)
}
