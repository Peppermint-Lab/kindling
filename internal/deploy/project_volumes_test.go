package deploy

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestProjectVolumeRetryDelay(t *testing.T) {
	t.Parallel()

	if got := projectVolumeRetryDelay(errors.New("transient")); got != 5*time.Second {
		t.Fatalf("transient retry delay = %s, want %s", got, 5*time.Second)
	}
	if got := projectVolumeRetryDelay(&projectVolumeUnavailableError{message: "pinned server unavailable"}); got != 30*time.Second {
		t.Fatalf("unavailable retry delay = %s, want %s", got, 30*time.Second)
	}
}

func TestProjectVolumeServerStatusMessage(t *testing.T) {
	t.Parallel()

	serverID := pgtype.UUID{Bytes: uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"), Valid: true}
	got := projectVolumeServerStatusMessage(serverID, "dead")
	want := "pinned server aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee is dead"
	if got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}
