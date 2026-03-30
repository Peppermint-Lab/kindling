package pguuid

import (
	"testing"

	"github.com/google/uuid"
)

func TestToPgtypeMarksNilUUIDInvalid(t *testing.T) {
	t.Parallel()

	got := ToPgtype(uuid.Nil)
	if got.Valid {
		t.Fatalf("expected nil UUID to produce invalid pgtype UUID, got %+v", got)
	}
}

func TestToPgtypeMarksRealUUIDValid(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	got := ToPgtype(id)
	if !got.Valid {
		t.Fatalf("expected real UUID to be valid, got %+v", got)
	}
	if got.Bytes != id {
		t.Fatalf("bytes = %v, want %v", got.Bytes, id)
	}
}
