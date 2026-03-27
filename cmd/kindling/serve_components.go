package main

import (
	"fmt"
	"os"
	"strings"
)

type serveComponents struct {
	api    bool
	edge   bool
	worker bool
}

func resolveServeComponents(flagValue string) (serveComponents, error) {
	raw := strings.TrimSpace(flagValue)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("KINDLING_COMPONENTS"))
	}
	if raw == "" {
		raw = "api,edge,worker"
	}
	return parseServeComponents(raw)
}

func parseServeComponents(raw string) (serveComponents, error) {
	var out serveComponents
	for _, part := range strings.Split(raw, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		switch name {
		case "all":
			out.api = true
			out.edge = true
			out.worker = true
		case "api":
			out.api = true
		case "edge":
			out.edge = true
		case "worker":
			out.worker = true
		default:
			return serveComponents{}, fmt.Errorf("unknown serve component %q", name)
		}
	}
	if !out.api && !out.edge && !out.worker {
		return serveComponents{}, fmt.Errorf("no serve components enabled")
	}
	return out, nil
}
