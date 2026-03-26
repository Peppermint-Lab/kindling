package edgeproxy

import "testing"

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
