package httputil

import (
	"errors"
	"io"
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
type bodyLimitResponseWriter struct {
	http.ResponseWriter
	maxBytes    int64
	tracker     *bodyLimitTracker
	wroteHeader bool
}

func (bw *bodyLimitResponseWriter) WriteHeader(code int) {
	if bw.wroteHeader {
		return
	}
	bw.wroteHeader = true

	// If the body limit was exceeded and the handler is reporting an
	// error (typically 400 from a failed JSON decode), override to 413.
	if bw.tracker.isExceeded() && code >= 400 && code < 500 {
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
	return bw.ResponseWriter.Write(b)
}

// Unwrap supports http.ResponseController and middleware that unwrap
// response writers.
func (bw *bodyLimitResponseWriter) Unwrap() http.ResponseWriter {
	return bw.ResponseWriter
}
