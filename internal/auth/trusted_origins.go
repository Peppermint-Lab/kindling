package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/rpc/settings"
)

// TrustedOrigins returns the configured browser origins that may send
// credentialed requests to the API in addition to the request's own origin.
func TrustedOrigins(ctx context.Context, q *queries.Queries) []string {
	var out []string
	add := func(v string) {
		v = strings.TrimRight(strings.TrimSpace(v), "/")
		if v == "" {
			return
		}
		for _, existing := range out {
			if strings.EqualFold(existing, v) {
				return
			}
		}
		out = append(out, v)
	}

	for _, origin := range strings.Split(os.Getenv("KINDLING_CORS_ORIGINS"), ",") {
		add(origin)
	}

	if q == nil {
		return out
	}

	if v, err := q.ClusterSettingGet(ctx, settings.ClusterSettingKeyDashboardPublicHost); err == nil {
		if host := settings.NormalizeDashboardPublicHost(v); host != "" {
			add("https://" + host)
			add("http://" + host)
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out
	}

	if v, err := q.ClusterSettingGet(ctx, settings.ClusterSettingKeyPublicBaseURL); err == nil {
		add(settings.NormalizePublicBaseURL(v))
	}

	return out
}

func originMatchesAny(raw string, allow []string) bool {
	for _, target := range allow {
		if originMatchesTarget(raw, target) {
			return true
		}
	}
	return false
}

func hostIsLocal(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	host = strings.ToLower(host)
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func loopbackOriginAllowed(r *http.Request, raw string) bool {
	if !hostIsLocal(r.Host) {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return false
	}
	return hostIsLocal(u.Hostname())
}
