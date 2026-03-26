package auth

const (
	SessionCookieName = "kindling_session"
	// SessionMaxAge is browser Max-Age hint (seconds); DB expiry is authoritative.
	SessionMaxAgeSeconds = 30 * 24 * 60 * 60
)
