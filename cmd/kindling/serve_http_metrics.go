package main

import (
	"context"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/usage"
)

func controlPlaneTrafficMiddleware(q *queries.Queries, serverID uuid.UUID, next http.Handler) http.Handler {
	sid := pgtype.UUID{Bytes: serverID, Valid: serverID != uuid.Nil}
	if !sid.Valid {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		var inBytes int64
		if r.Body != nil {
			r.Body = &countingReadCloser{ReadCloser: r.Body, n: &inBytes}
		}
		mw := &recordingResponseWriter{ResponseWriter: w}
		next.ServeHTTP(mw, r)
		if statusCode := mw.StatusCode(); statusCode > 0 {
			go usage.IncrementServerHTTPUsageRollup(
				context.Background(),
				q,
				sid,
				usage.TrafficKindControlPlaneAPI,
				statusCode,
				inBytes,
				mw.n,
			)
		}
	})
}

type countingReadCloser struct {
	io.ReadCloser
	n *int64
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	nn, err := c.ReadCloser.Read(p)
	*c.n += int64(nn)
	return nn, err
}

type recordingResponseWriter struct {
	http.ResponseWriter
	n          int64
	statusCode int
}

func (m *recordingResponseWriter) WriteHeader(statusCode int) {
	m.statusCode = statusCode
	m.ResponseWriter.WriteHeader(statusCode)
}

func (m *recordingResponseWriter) Write(b []byte) (int, error) {
	if m.statusCode == 0 {
		m.statusCode = http.StatusOK
	}
	nn, err := m.ResponseWriter.Write(b)
	m.n += int64(nn)
	return nn, err
}

func (m *recordingResponseWriter) StatusCode() int {
	return m.statusCode
}
