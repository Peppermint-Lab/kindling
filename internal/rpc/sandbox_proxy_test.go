package rpc

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/sandbox"
)

func TestSandboxIsLocalOwnerRequiresRuntime(t *testing.T) {
	t.Parallel()

	serverID := uuid.New()
	api := &API{
		sandboxSvc: &sandbox.Service{
			ServerID: serverID,
		},
	}
	sb := queries.RemoteVm{
		ServerID: pgtype.UUID{Bytes: serverID, Valid: true},
	}

	if api.sandboxIsLocalOwner(sb) {
		t.Fatal("sandboxIsLocalOwner returned true without a local runtime")
	}
}

func TestCopySandboxProxyableHeadersSkipsWebsocketNegotiationHeaders(t *testing.T) {
	t.Parallel()

	src := http.Header{}
	src.Set("Authorization", "Bearer test")
	src.Set("Sec-WebSocket-Key", "abc")
	src.Set("Sec-WebSocket-Version", "13")
	src.Set("Sec-WebSocket-Protocol", "chat")
	src.Set("Sec-WebSocket-Extensions", "permessage-deflate")
	src.Set("X-Test", "ok")

	dst := http.Header{}
	copySandboxProxyableHeaders(dst, src)

	if got := dst.Get("Authorization"); got != "Bearer test" {
		t.Fatalf("authorization header = %q, want %q", got, "Bearer test")
	}
	if got := dst.Get("X-Test"); got != "ok" {
		t.Fatalf("x-test header = %q, want %q", got, "ok")
	}
	for _, key := range []string{
		"Sec-WebSocket-Key",
		"Sec-WebSocket-Version",
		"Sec-WebSocket-Protocol",
		"Sec-WebSocket-Extensions",
	} {
		if got := dst.Get(key); got != "" {
			t.Fatalf("%s header = %q, want empty", key, got)
		}
	}
}
