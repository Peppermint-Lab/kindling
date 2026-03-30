//go:build !linux

package runtime

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

func waitCrunInitPID(ctx context.Context, containerID string) (int, error) {
	_ = containerID
	return 0, errors.New("crun: unsupported on this platform")
}

func startCrunHostTCPForward(ctx context.Context, listenAddr string, guestAddr string, pid int) (func(), error) {
	_, _, _ = ctx, listenAddr, guestAddr
	_ = pid
	return nil, errors.New("crun: unsupported on this platform")
}

func setupCrunContainerNetworking(id uuid.UUID, pid int) (func(), error) {
	_, _ = id, pid
	return nil, errors.New("crun: unsupported on this platform")
}
