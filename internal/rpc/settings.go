package rpc

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

// ClusterSettingKeyPublicBaseURL is the cluster_settings key for the public API base URL.
const ClusterSettingKeyPublicBaseURL = "public_base_url"

// NormalizePublicBaseURL trims whitespace and trailing slashes for stored URLs.
func NormalizePublicBaseURL(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, "/")
	return s
}

func (a *API) publicBaseURL(ctx context.Context) (string, error) {
	v, err := a.q.ClusterSettingGet(ctx, ClusterSettingKeyPublicBaseURL)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return NormalizePublicBaseURL(v), nil
}

func (a *API) clusterSettingUpsertPublicBaseURL(ctx context.Context, raw string) error {
	return a.q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{
		Key:   ClusterSettingKeyPublicBaseURL,
		Value: NormalizePublicBaseURL(raw),
	})
}
