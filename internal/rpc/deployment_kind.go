package rpc

import (
	"os"
	"strings"
)

// DeploymentKind is "hosted" when KINDLING_DEPLOYMENT_KIND=hosted; otherwise "self_hosted".
func DeploymentKind() string {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("KINDLING_DEPLOYMENT_KIND")))
	if v == "hosted" {
		return "hosted"
	}
	return "self_hosted"
}
