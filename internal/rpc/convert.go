package rpc

import "github.com/jackc/pgx/v5/pgtype"

func pgTextString(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}
