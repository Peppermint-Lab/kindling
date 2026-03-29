package rpc

import "strings"

// consumerDomains is the set of common consumer email domains that should NOT
// trigger org-domain matching during OAuth login. Users with these email
// domains always get a fresh personal organization (existing behavior).
var consumerDomains = map[string]bool{
	"gmail.com":       true,
	"yahoo.com":       true,
	"outlook.com":     true,
	"hotmail.com":     true,
	"icloud.com":      true,
	"aol.com":         true,
	"protonmail.com":  true,
	"mail.com":        true,
	"zoho.com":        true,
}

// isConsumerDomain returns true if the email domain is a well-known consumer
// provider that should never be matched to an existing organization.
func isConsumerDomain(domain string) bool {
	return consumerDomains[strings.ToLower(strings.TrimSpace(domain))]
}

// extractEmailDomain returns the lowercase domain part of an email address.
// Returns empty string if the email is malformed or empty.
func extractEmailDomain(email string) string {
	email = strings.TrimSpace(strings.ToLower(email))
	at := strings.LastIndex(email, "@")
	if at < 0 || at >= len(email)-1 {
		return ""
	}
	return email[at+1:]
}
