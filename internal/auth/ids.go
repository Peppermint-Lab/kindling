package auth

import "github.com/google/uuid"

// BootstrapOrganizationID is inserted by schema migration and owns existing projects
// until additional orgs are created.
var BootstrapOrganizationID = uuid.MustParse("c0000000-0000-4000-a000-000000000001")
