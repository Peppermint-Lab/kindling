package netnames

import (
	"strings"
	"unicode"
)

func NormalizeLabel(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	var b strings.Builder
	b.Grow(len(raw))
	lastHyphen := false
	for _, r := range raw {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastHyphen = false
		case r == '-' || r == '_' || unicode.IsSpace(r):
			if b.Len() == 0 || lastHyphen {
				continue
			}
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func PrivateDNSName(endpoint, service, project, env, org string) string {
	parts := []string{
		NormalizeLabel(endpoint),
		NormalizeLabel(service),
		NormalizeLabel(project),
		NormalizeLabel(env),
		NormalizeLabel(org),
		"kindling",
		"internal",
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return strings.Join(out, ".")
}
