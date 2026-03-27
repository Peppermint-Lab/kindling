package edgeproxy

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func TestPickBackend_empty(t *testing.T) {
	var s Service
	_, ok := s.pickBackend(Route{})
	if ok {
		t.Fatal("expected no backend")
	}
}

func TestPickBackend_nonEmpty(t *testing.T) {
	var s Service
	r := Route{Backends: []Backend{{IP: "127.0.0.1", Port: 3000}}}
	be, ok := s.pickBackend(r)
	if !ok || be.Port != 3000 {
		t.Fatalf("backend %+v ok=%v", be, ok)
	}
}

func TestPreviewLookupShouldReturnGone(t *testing.T) {
	t.Parallel()

	if !previewLookupShouldReturnGone(queries.DomainEdgeLookupRow{
		DomainKind:      "preview_stable",
		PreviewClosedAt: pgtype.Timestamptz{Valid: true},
	}) {
		t.Fatal("expected closed preview lookup to return gone")
	}

	if previewLookupShouldReturnGone(queries.DomainEdgeLookupRow{
		DomainKind:      "production",
		PreviewClosedAt: pgtype.Timestamptz{Valid: true},
	}) {
		t.Fatal("production domain should not return gone")
	}

	if previewLookupShouldReturnGone(queries.DomainEdgeLookupRow{
		DomainKind: "preview_immutable",
	}) {
		t.Fatal("open preview domain should not return gone")
	}
}
