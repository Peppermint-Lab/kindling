package httputil

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

type hijackableRecorder struct {
	*httptest.ResponseRecorder
	hijacked bool
}

func newHijackableRecorder() *hijackableRecorder {
	return &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	r.hijacked = true
	client, server := net.Pipe()
	// The test only needs the client side returned via Hijack.
	_ = server.Close()
	rw := bufio.NewReadWriter(bufio.NewReader(client), bufio.NewWriter(client))
	return client, rw, nil
}

func TestBodyLimitMiddlewarePreservesHijacker(t *testing.T) {
	t.Parallel()

	handler := BodyLimitMiddleware(1<<20, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "missing hijacker", http.StatusInternalServerError)
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			http.Error(w, fmt.Sprintf("hijack failed: %v", err), http.StatusInternalServerError)
			return
		}
		_ = conn.Close()
	}))

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	rec := newHijackableRecorder()
	handler.ServeHTTP(rec, req)

	if !rec.hijacked {
		t.Fatal("response writer was not hijacked")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}
