package auth

import (
	"context"

	"github.com/google/uuid"
)

type ctxWorkerAgentKey int

const workerAgentPrincipalKey ctxWorkerAgentKey = iota

type WorkerAgentPrincipal struct {
	AgentID uuid.UUID
	OrgID   uuid.UUID
}

func WithWorkerAgent(ctx context.Context, w WorkerAgentPrincipal) context.Context {
	return context.WithValue(ctx, workerAgentPrincipalKey, w)
}

func WorkerAgentFrom(ctx context.Context) (WorkerAgentPrincipal, bool) {
	w, ok := ctx.Value(workerAgentPrincipalKey).(WorkerAgentPrincipal)
	return w, ok
}
