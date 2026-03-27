package rpc

import (
	"net/http"

	"github.com/kindlingvm/kindling/internal/auth"
)

func orgRoleCanManage(role string) bool {
	return role == "owner" || role == "admin"
}

func requireOrgAdmin(w http.ResponseWriter, p auth.Principal) bool {
	if orgRoleCanManage(p.OrgRole) {
		return true
	}
	writeAPIError(w, http.StatusForbidden, "forbidden", "owner or admin role required")
	return false
}
