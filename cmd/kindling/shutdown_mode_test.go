package main

import "testing"

func TestShouldStopWorkloadsOnShutdown(t *testing.T) {
	t.Setenv("KINDLING_PRESERVE_WORKLOADS_ON_SHUTDOWN", "")
	if !shouldStopWorkloadsOnShutdown() {
		t.Fatal("expected default shutdown behavior to stop workloads")
	}

	t.Setenv("KINDLING_PRESERVE_WORKLOADS_ON_SHUTDOWN", "1")
	if shouldStopWorkloadsOnShutdown() {
		t.Fatal("expected preserve flag to skip StopAll")
	}

	t.Setenv("KINDLING_PRESERVE_WORKLOADS_ON_SHUTDOWN", "true")
	if shouldStopWorkloadsOnShutdown() {
		t.Fatal("expected true preserve flag to skip StopAll")
	}

	t.Setenv("KINDLING_PRESERVE_WORKLOADS_ON_SHUTDOWN", "false")
	if !shouldStopWorkloadsOnShutdown() {
		t.Fatal("expected false preserve flag to stop workloads")
	}
}
