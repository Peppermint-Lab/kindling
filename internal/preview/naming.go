// Package preview hosts helpers for PR preview hostnames and slugs.
package preview

import (
	"fmt"
	"regexp"
	"strings"
)

var nonSlug = regexp.MustCompile(`[^a-z0-9-]+`)

// SlugProjectName turns a project name into a DNS-label-friendly slug.
func SlugProjectName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = nonSlug.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "app"
	}
	if len(s) > 40 {
		s = s[:40]
		s = strings.TrimRight(s, "-")
	}
	return s
}

// SlugServiceName turns a service slug or name into a DNS-label-friendly slug.
func SlugServiceName(name string) string {
	return SlugProjectName(name)
}

// ProductionServiceHostname is the generated service hostname for a public production service.
func ProductionServiceHostname(serviceName, projectName, baseDomain string) string {
	base := strings.Trim(strings.ToLower(baseDomain), ".")
	if base == "" {
		return ""
	}
	return fmt.Sprintf("%s-%s.%s", SlugServiceName(serviceName), SlugProjectName(projectName), base)
}

// StableHostname is the PR-stable preview URL (latest deployment for the PR).
func StableHostname(prNumber int, serviceName, projectName, baseDomain string) string {
	base := strings.Trim(strings.ToLower(baseDomain), ".")
	if base == "" {
		return ""
	}
	return fmt.Sprintf("pr-%d-%s-%s.%s", prNumber, SlugServiceName(serviceName), SlugProjectName(projectName), base)
}

// ImmutableHostname is pinned to one deployment revision (commit).
func ImmutableHostname(shortSHA string, prNumber int, serviceName, projectName, baseDomain string) string {
	base := strings.Trim(strings.ToLower(baseDomain), ".")
	if base == "" {
		return ""
	}
	sh := strings.ToLower(strings.TrimSpace(shortSHA))
	if len(sh) > 7 {
		sh = sh[:7]
	}
	if sh == "" {
		sh = "unknown"
	}
	return fmt.Sprintf("%s-pr%d-%s-%s.%s", sh, prNumber, SlugServiceName(serviceName), SlugProjectName(projectName), base)
}
