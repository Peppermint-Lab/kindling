//go:build !linux

package runtime

import (
	"context"
	"errors"
	"io"

	"github.com/google/uuid"
)

func (r *CrunRuntime) ExecGuest(ctx context.Context, id uuid.UUID, argv []string, cwd string, env []string) (GuestExecResult, error) {
	_, _, _, _, _ = ctx, id, argv, cwd, env
	return GuestExecResult{}, errors.New("crun guest access requires linux")
}

func (r *CrunRuntime) ReadGuestFile(ctx context.Context, id uuid.UUID, filePath string) ([]byte, error) {
	_, _, _ = ctx, id, filePath
	return nil, errors.New("crun guest access requires linux")
}

func (r *CrunRuntime) WriteGuestFile(ctx context.Context, id uuid.UUID, filePath string, data []byte) error {
	_, _, _, _ = ctx, id, filePath, data
	return errors.New("crun guest access requires linux")
}

func (r *CrunRuntime) StreamGuest(ctx context.Context, id uuid.UUID, argv []string, cwd string, env []string) (io.ReadWriteCloser, error) {
	_, _, _, _, _ = ctx, id, argv, cwd, env
	return nil, errors.New("crun guest access requires linux")
}

func (r *CrunRuntime) ConnectGuestTCP(ctx context.Context, id uuid.UUID, port int) (io.ReadWriteCloser, error) {
	_, _, _ = ctx, id, port
	return nil, errors.New("crun guest access requires linux")
}
