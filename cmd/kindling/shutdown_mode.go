package main

import (
	"os"
	"strings"
)

func shouldStopWorkloadsOnShutdown() bool {
	raw := strings.TrimSpace(os.Getenv("KINDLING_PRESERVE_WORKLOADS_ON_SHUTDOWN"))
	if raw == "" {
		return true
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return false
	default:
		return true
	}
}
