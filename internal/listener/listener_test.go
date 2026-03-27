package listener

import (
	"regexp"
	"strings"
	"testing"
)

func TestDefaultSlotName(t *testing.T) {
	a := defaultSlotName()
	b := defaultSlotName()

	if a == b {
		t.Fatalf("expected unique slot names, got %q twice", a)
	}
	if !strings.HasPrefix(a, "kindling_listener_") {
		t.Fatalf("slot name %q missing expected prefix", a)
	}
	if len(a) > 63 {
		t.Fatalf("slot name %q too long: %d", a, len(a))
	}
	if !regexp.MustCompile(`^[a-z0-9_]+$`).MatchString(a) {
		t.Fatalf("slot name %q contains invalid characters", a)
	}
}

func TestPublicationSQL(t *testing.T) {
	create := publicationCreateSQL("kindling_changes")
	alter := publicationAlterSQL("kindling_changes")

	if strings.Contains(create, "DROP PUBLICATION") || strings.Contains(alter, "DROP PUBLICATION") {
		t.Fatal("publication SQL should never drop the shared publication")
	}
	if !strings.HasPrefix(create, `CREATE PUBLICATION "kindling_changes" FOR TABLE `) {
		t.Fatalf("unexpected create SQL: %q", create)
	}
	if !strings.HasPrefix(alter, `ALTER PUBLICATION "kindling_changes" SET TABLE `) {
		t.Fatalf("unexpected alter SQL: %q", alter)
	}
	if !strings.Contains(create, "deployments, deployment_instances, projects, builds") {
		t.Fatalf("create SQL missing tracked tables: %q", create)
	}
}
