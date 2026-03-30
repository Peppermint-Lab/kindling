package httputil

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
)

// BodyLimitMiddleware enforces a maximum request body size. It wraps
// r.Body with http.MaxBytesReader and returns 413 Payload Too Large
// when the limit is exceeded — either immediately (if Content-Length
// is known) or during body reads (for chunked / streaming bodies).
//
// When MaxBytesReader triggers during a read, the middleware's custom
// ResponseWriter intercepts the handler's error response and replaces
// it with a proper 413.
func BodyLimitMiddleware(maxBytes int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fast-path: reject if Content-Length header exceeds limit.
		if r.ContentLength > maxBytes {
			WriteAPIError(w, http.StatusRequestEntityTooLarge,
				"payload_too_large",
				"request body too large (limit "+strconv.FormatInt(maxBytes, 10)+" bytes)")
			return
		}

		// Wrap the body with a reader that tracks MaxBytesError.
		tracker := &bodyLimitTracker{
			body: http.MaxBytesReader(w, r.Body, maxBytes),
		}
		r.Body = tracker

		// Wrap the ResponseWriter to intercept handler error responses
		// caused by body-too-large reads.
		bw := &bodyLimitResponseWriter{
			ResponseWriter: w,
			maxBytes:       maxBytes,
			tracker:        tracker,
		}
		next.ServeHTTP(bw, r)
	})
}

// bodyLimitTracker wraps a MaxBytesReader body and records whether a
// MaxBytesError was encountered.
type bodyLimitTracker struct {
	body     io.ReadCloser
	mu       sync.Mutex
	exceeded bool
}

func (t *bodyLimitTracker) Read(p []byte) (int, error) {
	n, err := t.body.Read(p)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			t.mu.Lock()
			t.exceeded = true
			t.mu.Unlock()
		}
	}
	return n, err
}

func (t *bodyLimitTracker) Close() error {
	return t.body.Close()
}

func (t *bodyLimitTracker) isExceeded() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exceeded
}

// bodyLimitResponseWriter intercepts WriteHeader to convert error
// responses caused by body-too-large into proper 413 responses.
// Once a 413 has been emitted, all subsequent Write calls from
// the handler are silently discarded to prevent concatenated bodies.
type bodyLimitResponseWriter struct {
	http.ResponseWriter
	maxBytes    int64
	tracker     *bodyLimitTracker
	wroteHeader bool
	hijacked    bool // true after middleware emits its own 413 response
}

func (bw *bodyLimitResponseWriter) WriteHeader(code int) {
	if bw.wroteHeader {
		return
	}
	bw.wroteHeader = true

	// If the body limit was exceeded and the handler is reporting an
	// error (typically 400 from a failed JSON decode), override to 413.
	if bw.tracker.isExceeded() && code >= 400 && code < 500 {
		bw.hijacked = true
		WriteAPIError(bw.ResponseWriter, http.StatusRequestEntityTooLarge,
			"payload_too_large",
			"request body too large (limit "+strconv.FormatInt(bw.maxBytes, 10)+" bytes)")
		return
	}

	bw.ResponseWriter.WriteHeader(code)
}

func (bw *bodyLimitResponseWriter) Write(b []byte) (int, error) {
	if !bw.wroteHeader {
		bw.WriteHeader(http.StatusOK)
	}
	// If the middleware already emitted a 413 response, discard the
	// handler's body bytes to avoid concatenated JSON error payloads.
	if bw.hijacked {
		return len(b), nil
	}
	return bw.ResponseWriter.Write(b)
}

// Hijack preserves websocket and other upgraded connections when the
// underlying response writer supports connection hijacking.
func (bw *bodyLimitResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := bw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, nil, err
	}
	bw.hijacked = true
	return conn, rw, nil
}

// Unwrap supports http.ResponseController and middleware that unwrap
// response writers.
func (bw *bodyLimitResponseWriter) Unwrap() http.ResponseWriter {
	return bw.ResponseWriter
}
