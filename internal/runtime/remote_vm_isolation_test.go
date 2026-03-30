package runtime

import "testing"

func TestParseRemoteVMIsolationPolicy(t *testing.T) {
	t.Parallel()
	got, err := ParseRemoteVMIsolationPolicy("")
	if err != nil || got != RemoteVMIsolationBestAvailable {
		t.Fatalf("empty: got %q err %v", got, err)
	}
	got, err = ParseRemoteVMIsolationPolicy("  best_AVAILABLE ")
	if err != nil || got != RemoteVMIsolationBestAvailable {
		t.Fatalf("best: got %q err %v", got, err)
	}
	got, err = ParseRemoteVMIsolationPolicy("require_microvm")
	if err != nil || got != RemoteVMIsolationRequireMicrovm {
		t.Fatalf("require: got %q err %v", got, err)
	}
	_, err = ParseRemoteVMIsolationPolicy("nope")
	if err == nil {
		t.Fatal("invalid policy should error")
	}
}

func TestNormalizeRemoteVMIsolationPolicy(t *testing.T) {
	t.Parallel()
	if got := NormalizeRemoteVMIsolationPolicy(""); got != RemoteVMIsolationBestAvailable {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeRemoteVMIsolationPolicy("require_microvm"); got != RemoteVMIsolationRequireMicrovm {
		t.Fatalf("got %q", got)
	}
}
