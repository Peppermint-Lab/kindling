package builder

import (
	"encoding/json"
	"strings"
)

// frameworkSignals collects file presence and content signals for detection.
type frameworkSignals struct {
	hasNuxtConfig  bool
	hasNextConfig  bool
	hasGemfile     bool
	hasRakefile    bool
	hasRailsRoutes bool
	hasArtisan     bool
	hasGoMod       bool

	packageJSON  []byte
	gemfileBytes []byte
	composerJSON []byte
}

// DetectFramework determines the framework from file signals.
// Returns the framework name or empty string if unknown.
func DetectFramework(s frameworkSignals) string {
	// Config file detection (highest confidence).
	if s.hasNuxtConfig {
		return "nuxt"
	}
	if s.hasNextConfig {
		return "nextjs"
	}
	if s.hasArtisan {
		return "laravel"
	}
	if s.hasGoMod {
		return "go"
	}
	if s.hasRailsRoutes || (s.hasGemfile && s.hasRakefile) {
		return "rails"
	}

	// package.json dependency detection.
	if s.packageJSON != nil {
		var pkg struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal(s.packageJSON, &pkg) == nil {
			if _, ok := pkg.Dependencies["nuxt"]; ok {
				return "nuxt"
			}
			if _, ok := pkg.DevDependencies["nuxt"]; ok {
				return "nuxt"
			}
			if _, ok := pkg.Dependencies["next"]; ok {
				return "nextjs"
			}
			if _, ok := pkg.DevDependencies["next"]; ok {
				return "nextjs"
			}
		}
	}

	// Gemfile content detection.
	if s.gemfileBytes != nil {
		content := string(s.gemfileBytes)
		if strings.Contains(content, "'rails'") || strings.Contains(content, "\"rails\"") {
			return "rails"
		}
	}

	// composer.json detection.
	if s.composerJSON != nil {
		var composer struct {
			Require map[string]string `json:"require"`
		}
		if json.Unmarshal(s.composerJSON, &composer) == nil {
			if _, ok := composer.Require["laravel/framework"]; ok {
				return "laravel"
			}
		}
	}

	return ""
}
