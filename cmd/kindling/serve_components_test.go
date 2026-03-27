package main

import "testing"

func TestParseServeComponents(t *testing.T) {
	got, err := parseServeComponents("api, edge,worker")
	if err != nil {
		t.Fatalf("parseServeComponents returned error: %v", err)
	}
	if !got.api || !got.edge || !got.worker {
		t.Fatalf("parsed components = %+v", got)
	}

	got, err = parseServeComponents("edge")
	if err != nil {
		t.Fatalf("parseServeComponents returned error: %v", err)
	}
	if got.api || !got.edge || got.worker {
		t.Fatalf("parsed components = %+v", got)
	}

	got, err = parseServeComponents("all")
	if err != nil {
		t.Fatalf("parseServeComponents returned error: %v", err)
	}
	if !got.api || !got.edge || !got.worker {
		t.Fatalf("parsed components = %+v", got)
	}
}

func TestParseServeComponentsRejectsUnknownOrEmpty(t *testing.T) {
	if _, err := parseServeComponents(""); err == nil {
		t.Fatal("expected error for empty components")
	}
	if _, err := parseServeComponents("api,unknown"); err == nil {
		t.Fatal("expected error for unknown component")
	}
}
