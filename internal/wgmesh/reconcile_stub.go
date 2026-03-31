//go:build !linux

package wgmesh

import (
	"context"

	"github.com/google/uuid"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// RunReconcileLoop is a no-op on non-Linux platforms.
func RunReconcileLoop(ctx context.Context, q *queries.Queries, serverID uuid.UUID, priv wgtypes.Key) {
}
