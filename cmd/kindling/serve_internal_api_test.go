package main

import "testing"

func TestInternalAPIListenAddr(t *testing.T) {
	t.Parallel()

	t.Run("worker_only_uses_adjacent_port", func(t *testing.T) {
		t.Parallel()

		addr, err := internalAPIListenAddr(":8080", serveComponents{worker: true})
		if err != nil {
			t.Fatalf("internalAPIListenAddr: %v", err)
		}
		if addr != ":8081" {
			t.Fatalf("addr = %q, want %q", addr, ":8081")
		}
	})

	t.Run("api_worker_keeps_public_port", func(t *testing.T) {
		t.Parallel()

		addr, err := internalAPIListenAddr(":8080", serveComponents{api: true, worker: true})
		if err != nil {
			t.Fatalf("internalAPIListenAddr: %v", err)
		}
		if addr != ":8080" {
			t.Fatalf("addr = %q, want %q", addr, ":8080")
		}
	})
}
