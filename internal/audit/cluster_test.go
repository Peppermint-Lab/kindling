package audit

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestRecordClusterEvent_NilQueriesIsNoop(t *testing.T) {
	t.Parallel()
	RecordClusterEvent(context.Background(), nil, uuid.Nil, nil, ActionServerDrain, "server", "x", map[string]any{
		"hostname": "worker-1",
	})
}
