-- name: ServerRegister :one
INSERT INTO servers (id, hostname, internal_ip, ip_range, status, last_heartbeat_at)
VALUES ($1, $2, $3, $4, 'active', NOW())
ON CONFLICT (id) DO UPDATE SET
    hostname = EXCLUDED.hostname,
    internal_ip = EXCLUDED.internal_ip,
    status = CASE
        WHEN servers.status IN ('draining', 'drained', 'dead') THEN servers.status
        ELSE 'active'
    END,
    last_heartbeat_at = NOW(),
    updated_at = NOW()
RETURNING *;

-- name: ServerFindByID :one
SELECT * FROM servers WHERE id = $1;

-- name: ServerHeartbeat :exec
UPDATE servers SET last_heartbeat_at = NOW(), updated_at = NOW() WHERE id = $1;

-- name: ServerFindDead :many
SELECT * FROM servers
WHERE status IN ('active', 'draining')
  AND last_heartbeat_at < NOW() - INTERVAL '30 seconds';

-- name: ServerUpdateStatus :exec
UPDATE servers SET status = $2, updated_at = NOW() WHERE id = $1;

-- name: ServerSetDrained :exec
UPDATE servers SET status = 'drained', updated_at = NOW() WHERE id = $1;

-- name: ServerSetDraining :exec
UPDATE servers SET status = 'draining', updated_at = NOW()
WHERE id = $1 AND status = 'active';

-- name: ServerSetActive :exec
UPDATE servers SET status = 'active', updated_at = NOW()
WHERE id = $1 AND status IN ('draining', 'drained');

-- name: ServerFindLeastLoaded :one
SELECT s.* FROM servers s
LEFT JOIN vms v ON v.server_id = s.id AND v.deleted_at IS NULL
WHERE s.status = 'active'
  AND s.last_heartbeat_at > NOW() - INTERVAL '3 minutes'
GROUP BY s.id
ORDER BY COUNT(v.id) ASC, s.last_heartbeat_at DESC
LIMIT 1;

-- name: ServerAllocateIPRange :one
SELECT CASE
    WHEN MAX(ip_range) IS NULL THEN '10.0.0.0/20'::CIDR
    ELSE (MAX(ip_range) + 1)::CIDR
END AS ip_range
FROM servers;

-- name: ServerFindAll :many
SELECT * FROM servers ORDER BY created_at;

-- name: ServerComponentStatusUpsert :exec
INSERT INTO server_component_statuses (
    server_id, component, status, observed_at, last_success_at, last_error_at, last_error_message, metadata, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW()
)
ON CONFLICT (server_id, component) DO UPDATE SET
    status = EXCLUDED.status,
    observed_at = EXCLUDED.observed_at,
    last_success_at = EXCLUDED.last_success_at,
    last_error_at = EXCLUDED.last_error_at,
    last_error_message = EXCLUDED.last_error_message,
    metadata = EXCLUDED.metadata,
    updated_at = NOW();

-- name: ServerComponentStatusFindAll :many
SELECT * FROM server_component_statuses
ORDER BY server_id, component;

-- name: ServerComponentStatusFindByServerID :many
SELECT * FROM server_component_statuses
WHERE server_id = $1
ORDER BY component;

-- name: TrySessionAdvisoryLock :one
SELECT pg_try_advisory_lock(hashtext($1));

-- name: AdvisoryLock :exec
SELECT pg_advisory_xact_lock(hashtext($1));

-- Cluster settings --

-- name: ClusterSettingGet :one
SELECT value FROM cluster_settings WHERE key = $1;

-- name: ClusterSettingUpsert :exec
INSERT INTO cluster_settings (key, value, updated_at)
VALUES ($1, $2, NOW())
ON CONFLICT (key) DO UPDATE SET
    value = EXCLUDED.value,
    updated_at = NOW();

-- name: ClusterSettingsAll :many
SELECT key, value FROM cluster_settings ORDER BY key;

-- name: ServerSettingEnsure :exec
INSERT INTO server_settings (server_id) VALUES ($1) ON CONFLICT (server_id) DO NOTHING;

-- name: ServerSettingGet :one
SELECT * FROM server_settings WHERE server_id = $1;

-- name: ServerSettingSeedAdvertiseHostIfUnset :exec
UPDATE server_settings
SET advertise_host = $2, updated_at = NOW()
WHERE server_id = $1
  AND (advertise_host = '' OR BTRIM(advertise_host) = '');

-- name: ClusterSecretGet :one
SELECT ciphertext FROM cluster_secrets WHERE key = $1;

-- name: ClusterSecretUpsert :exec
INSERT INTO cluster_secrets (key, ciphertext, updated_at)
VALUES ($1, $2, NOW())
ON CONFLICT (key) DO UPDATE SET
    ciphertext = EXCLUDED.ciphertext,
    updated_at = NOW();

-- name: ClusterSecretDelete :exec
DELETE FROM cluster_secrets WHERE key = $1;

-- Images --

-- name: ImageFindOrCreate :one
INSERT INTO images (id, registry, repository, tag)
VALUES ($1, $2, $3, $4)
ON CONFLICT (registry, repository, tag) DO UPDATE SET registry = EXCLUDED.registry
RETURNING *;

-- name: ImageFindByID :one
SELECT * FROM images WHERE id = $1;

-- Projects --

-- name: ProjectCreate :one
INSERT INTO projects (id, org_id, name, github_repository, github_installation_id, github_webhook_secret, root_directory, dockerfile_path, desired_instance_count, build_only_on_root_changes)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: ProjectFirstByID :one
SELECT * FROM projects WHERE id = $1;

-- name: ProjectFirstByIDAndOrg :one
SELECT * FROM projects WHERE id = $1 AND org_id = $2;

-- name: ProjectFindAllByOrgID :many
SELECT * FROM projects WHERE org_id = $1 ORDER BY created_at DESC;

-- name: ProjectFindByGitHubRepo :one
SELECT * FROM projects WHERE github_repository = $1;

-- name: ProjectDeleteByIDAndOrg :exec
DELETE FROM projects WHERE id = $1 AND org_id = $2;

-- name: ProjectUpdateWebhookSecret :one
UPDATE projects
SET github_webhook_secret = $2, updated_at = NOW()
WHERE id = $1 AND org_id = $3
RETURNING *;

-- name: ProjectUpdateDesiredInstanceCount :one
UPDATE projects
SET desired_instance_count = $2, updated_at = NOW()
WHERE id = $1 AND org_id = $3
RETURNING *;

-- name: ProjectUpdateScaleToZeroEnabled :one
UPDATE projects
SET scale_to_zero_enabled = $2, updated_at = NOW()
WHERE id = $1 AND org_id = $3
RETURNING *;

-- name: ProjectUpdateBuildOnlyOnRootChanges :one
UPDATE projects
SET build_only_on_root_changes = $2, updated_at = NOW()
WHERE id = $1 AND org_id = $3
RETURNING *;

-- name: ProjectUpdateLastRequestAt :exec
UPDATE projects SET last_request_at = NOW(), updated_at = NOW() WHERE id = $1;

-- name: ProjectClearScaledToZero :exec
UPDATE projects SET scaled_to_zero = false, updated_at = NOW() WHERE id = $1;

-- name: ProjectMarkScaledToZero :exec
UPDATE projects
SET scaled_to_zero = true, updated_at = NOW()
WHERE id = $1
  AND scale_to_zero_enabled = true
  AND desired_instance_count > 0
  AND scaled_to_zero = false;

-- name: ProjectsFindForIdleScaleDown :many
SELECT * FROM projects
WHERE scale_to_zero_enabled = true
  AND desired_instance_count > 0
  AND scaled_to_zero = false
  AND last_request_at IS NOT NULL
  AND last_request_at < NOW() - ($1::bigint * INTERVAL '1 second')
ORDER BY last_request_at ASC
LIMIT 100;

-- Deployment instances (horizontal scaling) --

-- name: DeploymentInstanceCreate :one
INSERT INTO deployment_instances (id, deployment_id, role, status)
VALUES ($1, $2, 'active', 'pending')
RETURNING *;

-- name: DeploymentInstanceFirstByID :one
SELECT * FROM deployment_instances WHERE id = $1;

-- name: DeploymentInstanceFindByDeploymentID :many
SELECT * FROM deployment_instances
WHERE deployment_id = $1 AND deleted_at IS NULL
ORDER BY created_at ASC;

-- name: DeploymentInstanceUpdateServer :one
UPDATE deployment_instances SET server_id = $2, updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: DeploymentInstanceUpdateStatus :one
UPDATE deployment_instances SET status = $2, updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: DeploymentInstanceUpdateRole :one
UPDATE deployment_instances SET role = $2, updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: DeploymentInstanceAttachVM :one
UPDATE deployment_instances SET vm_id = $2, status = $3, updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: DeploymentInstanceSetCloneSource :one
UPDATE deployment_instances SET clone_source_instance_id = $2, updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: DeploymentInstanceSoftDelete :exec
UPDATE deployment_instances SET deleted_at = NOW(), updated_at = NOW() WHERE id = $1;

-- name: DeploymentInstanceSoftDeleteByDeploymentID :exec
UPDATE deployment_instances SET deleted_at = NOW(), updated_at = NOW()
WHERE deployment_id = $1 AND deleted_at IS NULL;

-- name: DeploymentInstanceVMIDsByServerID :many
SELECT vm_id FROM deployment_instances
WHERE server_id = $1 AND deleted_at IS NULL AND vm_id IS NOT NULL;

-- name: DeploymentInstanceResetForDeadServer :exec
UPDATE deployment_instances
SET server_id = NULL, vm_id = NULL, role = 'active', clone_source_instance_id = NULL, status = 'pending', updated_at = NOW()
WHERE server_id = $1 AND deleted_at IS NULL;

-- name: DeploymentInstancePrepareRetry :one
UPDATE deployment_instances
SET server_id = NULL, vm_id = NULL, role = 'active', clone_source_instance_id = NULL, status = 'pending', updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: DeploymentInstanceCountByServerID :one
SELECT COUNT(*)::bigint AS count FROM deployment_instances
WHERE server_id = $1 AND deleted_at IS NULL;

-- name: DeploymentInstanceActiveCountByServerID :one
SELECT COUNT(*)::bigint AS count FROM deployment_instances
WHERE server_id = $1
  AND deleted_at IS NULL
  AND role = 'active';

-- name: DeploymentIDsForInstancesOnServer :many
SELECT DISTINCT deployment_id FROM deployment_instances
WHERE server_id = $1 AND deleted_at IS NULL;

-- Environment Variables --

-- name: EnvironmentVariableCreate :one
INSERT INTO environment_variables (id, project_id, name, value)
VALUES ($1, $2, $3, $4)
ON CONFLICT (project_id, name) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()
RETURNING *;

-- name: EnvironmentVariableFindAll :many
SELECT * FROM environment_variables ORDER BY project_id, name;

-- name: EnvironmentVariableFindByProjectID :many
SELECT * FROM environment_variables WHERE project_id = $1 ORDER BY name;

-- name: EnvironmentVariableMetadataFindByProjectID :many
SELECT id, project_id, name, created_at, updated_at
FROM environment_variables
WHERE project_id = $1
ORDER BY name;

-- name: EnvironmentVariableUpdateValue :one
UPDATE environment_variables
SET value = $2, updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: EnvironmentVariableDeleteByIDAndProjectID :one
DELETE FROM environment_variables
WHERE id = $1 AND project_id = $2
RETURNING *;

-- VMs --

-- name: VMCreate :one
INSERT INTO vms (id, server_id, image_id, status, runtime, snapshot_ref, clone_source_vm_id, vcpus, memory, ip_address, port, env_variables)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: VMFirstByID :one
SELECT * FROM vms WHERE id = $1;

-- name: VMUpdateStatus :one
UPDATE vms SET status = $2, updated_at = NOW() WHERE id = $1 RETURNING *;

-- name: VMUpdateLifecycleMetadata :one
UPDATE vms
SET status = $2,
    snapshot_ref = $3,
    clone_source_vm_id = $4,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: VMSoftDelete :exec
UPDATE vms SET deleted_at = NOW(), updated_at = NOW() WHERE id = $1;

-- name: VMFindByServerID :many
SELECT * FROM vms WHERE server_id = $1 AND deleted_at IS NULL;

-- name: VMNextIPAddress :one
SELECT (CASE
    WHEN MAX(ip_address) IS NULL THEN (host(network(sqlc.arg(ip_range)::CIDR))::INET + 1)
    ELSE (MAX(ip_address) + 2)
END)::INET AS ip_address
FROM vms
WHERE server_id = sqlc.arg(server_id);

-- Builds --

-- name: BuildCreate :one
INSERT INTO builds (id, project_id, status, github_commit, github_branch)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: BuildFirstByID :one
SELECT * FROM builds WHERE id = $1;

-- name: BuildClaimLease :one
UPDATE builds SET processing_by = $2, updated_at = NOW()
WHERE id = $1 AND (processing_by IS NULL OR processing_by = $2)
RETURNING *;

-- name: BuildReleaseLease :exec
UPDATE builds SET processing_by = NULL, updated_at = NOW()
WHERE id = $1 AND processing_by = $2;

-- name: BuildMarkBuilding :exec
UPDATE builds SET status = 'building', vm_id = $2, building_at = NOW(), updated_at = NOW()
WHERE id = $1;

-- name: BuildMarkSuccessful :exec
UPDATE builds SET status = 'successful', image_id = $2, updated_at = NOW()
WHERE id = $1;

-- name: BuildMarkFailed :exec
UPDATE builds SET status = 'failed', failed_at = NOW(), updated_at = NOW()
WHERE id = $1;

-- name: BuildLogCreate :exec
INSERT INTO build_logs (id, build_id, message, level)
VALUES ($1, $2, $3, $4);

-- name: BuildLogsByBuildID :many
SELECT * FROM build_logs WHERE build_id = $1 ORDER BY created_at;

-- Deployments --

-- name: DeploymentCreate :one
INSERT INTO deployments (id, project_id, github_commit, github_branch, deployment_kind, preview_environment_id)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: DeploymentFirstByID :one
SELECT * FROM deployments WHERE id = $1;

-- name: DeploymentUpdateBuild :one
UPDATE deployments SET build_id = $2, updated_at = NOW() WHERE id = $1 RETURNING *;

-- name: DeploymentUpdateImage :one
UPDATE deployments SET image_id = $2, updated_at = NOW() WHERE id = $1 RETURNING *;

-- name: DeploymentUpdateVM :one
UPDATE deployments SET vm_id = $2, updated_at = NOW() WHERE id = $1 RETURNING *;

-- name: DeploymentMarkRunning :exec
UPDATE deployments SET running_at = NOW(), updated_at = NOW() WHERE id = $1;

-- name: DeploymentRequestWake :one
UPDATE deployments
SET wake_requested_at = COALESCE(wake_requested_at, NOW()), updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL AND failed_at IS NULL AND stopped_at IS NULL
RETURNING *;

-- name: DeploymentClearWakeRequested :exec
UPDATE deployments SET wake_requested_at = NULL, updated_at = NOW() WHERE id = $1;

-- name: DeploymentMarkStopped :exec
UPDATE deployments SET stopped_at = NOW(), updated_at = NOW() WHERE id = $1;

-- name: DeploymentUpdateFailedAt :exec
UPDATE deployments SET failed_at = NOW(), updated_at = NOW() WHERE id = $1;

-- name: DeploymentFindByVMID :one
SELECT d.* FROM deployments d
JOIN deployment_instances di ON di.deployment_id = d.id AND di.deleted_at IS NULL
WHERE di.vm_id = $1 AND d.deleted_at IS NULL;

-- name: DeploymentFindRunningAndOlder :many
SELECT * FROM deployments
WHERE project_id = $1
  AND deployment_kind = 'production'
  AND running_at IS NOT NULL
  AND stopped_at IS NULL
  AND failed_at IS NULL
  AND deleted_at IS NULL
  AND id != $2
ORDER BY created_at;

-- name: DeploymentFindRunningByServerID :many
SELECT DISTINCT d.* FROM deployments d
JOIN deployment_instances di ON di.deployment_id = d.id AND di.deleted_at IS NULL
JOIN vms v ON di.vm_id = v.id AND v.deleted_at IS NULL
WHERE v.server_id = $1
  AND d.running_at IS NOT NULL
  AND d.stopped_at IS NULL
  AND d.failed_at IS NULL
  AND d.deleted_at IS NULL;

-- name: DeploymentFindRecoverableByServerID :many
SELECT DISTINCT d.* FROM deployments d
JOIN deployment_instances di ON di.deployment_id = d.id AND di.deleted_at IS NULL
LEFT JOIN vms v ON di.vm_id = v.id AND v.deleted_at IS NULL
WHERE (di.server_id = $1 OR v.server_id = $1)
  AND d.stopped_at IS NULL
  AND d.failed_at IS NULL
  AND d.deleted_at IS NULL
ORDER BY d.created_at;

-- name: DeploymentFindByProjectID :many
SELECT * FROM deployments WHERE project_id = $1 ORDER BY created_at DESC;

-- name: DeploymentLatestRunningByProjectID :one
SELECT * FROM deployments
WHERE project_id = $1
  AND deployment_kind = 'production'
  AND running_at IS NOT NULL
  AND stopped_at IS NULL
  AND failed_at IS NULL
  AND deleted_at IS NULL
ORDER BY running_at DESC
LIMIT 1;

-- name: DeploymentFindRecentWithProject :many
SELECT
    d.id,
    d.project_id,
    d.build_id,
    d.image_id,
    d.vm_id,
    d.github_commit,
    d.github_branch,
    d.deployment_kind,
    d.preview_environment_id,
    d.preview_last_request_at,
    d.preview_scaled_to_zero,
    d.running_at,
    d.stopped_at,
    d.failed_at,
    d.deleted_at,
    d.wake_requested_at,
    d.created_at,
    d.updated_at,
    p.name AS project_name,
    b.status AS build_status
FROM deployments d
JOIN projects p ON p.id = d.project_id
LEFT JOIN builds b ON d.build_id = b.id
WHERE d.deleted_at IS NULL
ORDER BY d.created_at DESC
LIMIT $1;

-- name: DeploymentFindRecentWithProjectForOrg :many
SELECT
    d.id,
    d.project_id,
    d.build_id,
    d.image_id,
    d.vm_id,
    d.github_commit,
    d.github_branch,
    d.deployment_kind,
    d.preview_environment_id,
    d.preview_last_request_at,
    d.preview_scaled_to_zero,
    d.running_at,
    d.stopped_at,
    d.failed_at,
    d.deleted_at,
    d.wake_requested_at,
    d.created_at,
    d.updated_at,
    p.name AS project_name,
    b.status AS build_status
FROM deployments d
JOIN projects p ON p.id = d.project_id
LEFT JOIN builds b ON d.build_id = b.id
WHERE d.deleted_at IS NULL
  AND p.org_id = $2
ORDER BY d.created_at DESC
LIMIT $1;

-- Preview environments --

-- name: PreviewEnvironmentCreate :one
INSERT INTO preview_environments (id, project_id, provider, pr_number, head_branch, head_sha, stable_domain_name)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: PreviewEnvironmentByProjectAndPR :one
SELECT * FROM preview_environments
WHERE project_id = $1 AND provider = $2 AND pr_number = $3;

-- name: PreviewEnvironmentByID :one
SELECT * FROM preview_environments WHERE id = $1;

-- name: PreviewEnvironmentUpdateHead :one
UPDATE preview_environments
SET head_branch = $2, head_sha = $3, updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: PreviewEnvironmentSetLatestDeployment :exec
UPDATE preview_environments SET latest_deployment_id = $2, updated_at = NOW() WHERE id = $1;

-- name: PreviewEnvironmentSetStableDomain :exec
UPDATE preview_environments SET stable_domain_name = $2, updated_at = NOW() WHERE id = $1;

-- name: PreviewEnvironmentMarkClosed :one
UPDATE preview_environments
SET closed_at = NOW(), expires_at = $2, updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: PreviewEnvironmentsByProjectID :many
SELECT * FROM preview_environments WHERE project_id = $1 ORDER BY updated_at DESC;

-- name: PreviewImmutableDomainsByProjectID :many
SELECT
  d.preview_environment_id,
  d.domain_name,
  d.deployment_id,
  dep.github_commit
FROM domains d
JOIN preview_environments pe ON pe.id = d.preview_environment_id
LEFT JOIN deployments dep ON dep.id = d.deployment_id AND dep.deleted_at IS NULL
WHERE pe.project_id = $1
  AND d.domain_kind = 'preview_immutable'
ORDER BY d.created_at DESC;

-- name: PreviewEnvironmentsDueForCleanup :many
SELECT * FROM preview_environments
WHERE expires_at IS NOT NULL AND expires_at <= NOW();

-- name: PreviewEnvironmentDelete :exec
DELETE FROM preview_environments WHERE id = $1;

-- name: DeploymentsMarkStoppedByPreviewEnvironment :exec
UPDATE deployments SET stopped_at = NOW(), updated_at = NOW()
WHERE preview_environment_id = $1
  AND deployment_kind = 'preview'
  AND deleted_at IS NULL
  AND failed_at IS NULL
  AND stopped_at IS NULL;

-- name: DeploymentsByPreviewEnvironmentID :many
SELECT * FROM deployments
WHERE preview_environment_id = $1 AND deleted_at IS NULL
ORDER BY created_at DESC;

-- name: DeploymentPreviewUpdateLastRequestAt :exec
UPDATE deployments
SET preview_last_request_at = NOW(), updated_at = NOW()
WHERE id = $1 AND deployment_kind = 'preview';

-- name: DeploymentPreviewClearScaledToZero :exec
UPDATE deployments SET preview_scaled_to_zero = false, updated_at = NOW() WHERE id = $1;

-- name: DeploymentPreviewMarkScaledToZero :exec
UPDATE deployments
SET preview_scaled_to_zero = true, updated_at = NOW()
WHERE id = $1
  AND deployment_kind = 'preview'
  AND running_at IS NOT NULL
  AND stopped_at IS NULL
  AND failed_at IS NULL
  AND deleted_at IS NULL
  AND preview_scaled_to_zero = false;

-- name: DeploymentsFindPreviewForIdleScaleDown :many
SELECT * FROM deployments
WHERE deployment_kind = 'preview'
  AND running_at IS NOT NULL
  AND stopped_at IS NULL
  AND failed_at IS NULL
  AND deleted_at IS NULL
  AND preview_scaled_to_zero = false
  AND preview_last_request_at IS NOT NULL
  AND preview_last_request_at < NOW() - ($1::bigint * INTERVAL '1 second')
ORDER BY deployments.preview_last_request_at ASC
LIMIT 100;

-- name: BuildLogsAfterCreatedAt :many
SELECT * FROM build_logs
WHERE build_id = $1 AND created_at > $2
ORDER BY created_at;

-- Domains --

-- name: DomainCreate :one
INSERT INTO domains (id, project_id, domain_name, verification_token)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: DomainListByProjectID :many
SELECT * FROM domains WHERE project_id = $1 ORDER BY domain_name ASC;

-- name: DomainFirstByIDAndProject :one
SELECT * FROM domains WHERE id = $1 AND project_id = $2;

-- name: DomainProjectIDByDomainID :one
SELECT project_id FROM domains WHERE id = $1;

-- name: DomainDelete :exec
DELETE FROM domains WHERE id = $1 AND project_id = $2;

-- name: DomainSetVerified :one
UPDATE domains
SET verified_at = NOW(), verification_token = '', updated_at = NOW()
WHERE id = $1 AND project_id = $2
RETURNING *;

-- name: DomainVerified :one
SELECT verified_at FROM domains WHERE domain_name = $1;

-- name: DomainUpdateDeploymentForProject :exec
UPDATE domains SET deployment_id = $1, updated_at = NOW()
WHERE project_id = $2 AND domain_kind = 'production';

-- name: DomainUpdateDeploymentForDomainID :exec
UPDATE domains SET deployment_id = $2, updated_at = NOW() WHERE id = $1;

-- name: DomainCreatePreview :one
INSERT INTO domains (id, project_id, deployment_id, domain_name, verification_token, verified_at, domain_kind, preview_environment_id)
VALUES ($1, $2, $3, $4, '', NOW(), $5, $6)
RETURNING *;

-- name: DomainFindByPreviewEnvironmentAndKind :one
SELECT * FROM domains
WHERE preview_environment_id = $1 AND domain_kind = $2
LIMIT 1;

-- name: DomainFindByDeploymentIDAndKind :one
SELECT * FROM domains
WHERE deployment_id = $1 AND domain_kind = $2
LIMIT 1;

-- name: DomainFindVerifiedByDeploymentID :many
SELECT *
FROM domains
WHERE deployment_id = $1
  AND verified_at IS NOT NULL
ORDER BY domain_name ASC;

-- name: DomainEdgeLookup :one
SELECT
    d.id AS domain_id,
    d.project_id,
    d.deployment_id,
    d.redirect_to,
    d.redirect_status_code,
    dep.wake_requested_at AS deployment_wake_requested_at,
    dep.deployment_kind AS deployment_kind,
    (SELECT COUNT(*)::bigint FROM deployment_instances di
     INNER JOIN vms vm ON di.vm_id = vm.id AND vm.deleted_at IS NULL AND vm.status = 'running'
     WHERE di.deployment_id = d.deployment_id AND di.deleted_at IS NULL AND di.role = 'active' AND di.status = 'running') AS running_backend_count
FROM domains d
LEFT JOIN deployments dep ON d.deployment_id = dep.id AND dep.deleted_at IS NULL
WHERE d.domain_name = $1 AND d.verified_at IS NOT NULL;

-- name: RouteFindActive :many
SELECT d.domain_name,
       d.project_id,
       d.deployment_id,
       d.redirect_to,
       d.redirect_status_code,
       COALESCE(dep.deployment_kind, 'production') AS deployment_kind,
       v.ip_address AS vm_ip,
       v.port AS vm_port,
       v.server_id,
       v.id AS vm_id
FROM domains d
LEFT JOIN deployments dep ON d.deployment_id = dep.id
LEFT JOIN deployment_instances di ON di.deployment_id = dep.id
    AND di.deleted_at IS NULL
    AND di.role = 'active'
    AND di.status = 'running'
LEFT JOIN vms v ON di.vm_id = v.id AND v.deleted_at IS NULL AND v.status = 'running'
WHERE d.verified_at IS NOT NULL;

-- VM Logs --

-- name: VMLogCreate :exec
INSERT INTO vm_logs (id, vm_id, message, level)
VALUES ($1, $2, $3, $4);

-- name: VMLogsByVMID :many
SELECT * FROM vm_logs WHERE vm_id = $1 ORDER BY created_at;

-- CertMagic Storage --

-- name: CertMagicStore :exec
INSERT INTO certmagic_data (key, value, modified)
VALUES ($1, $2, NOW())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, modified = NOW();

-- name: CertMagicLoad :one
SELECT value, modified FROM certmagic_data WHERE key = $1;

-- name: CertMagicDelete :exec
DELETE FROM certmagic_data WHERE key = $1;

-- name: CertMagicExists :one
SELECT EXISTS(SELECT 1 FROM certmagic_data WHERE key = $1);

-- name: CertMagicList :many
SELECT key FROM certmagic_data WHERE key LIKE $1 ORDER BY key;

-- Auth & organizations --

-- name: UserCount :one
SELECT COUNT(*)::bigint FROM users;

-- name: UserCreate :one
INSERT INTO users (id, email, password_hash, display_name)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UserByEmail :one
SELECT * FROM users WHERE LOWER(email) = LOWER($1);

-- name: UserByID :one
SELECT * FROM users WHERE id = $1;

-- name: OrganizationByID :one
SELECT * FROM organizations WHERE id = $1;

-- name: OrganizationCreate :one
INSERT INTO organizations (id, name, slug)
VALUES ($1, $2, $3)
RETURNING *;

-- name: OrganizationMembershipCreate :one
INSERT INTO organization_memberships (id, organization_id, user_id, role)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: OrganizationMembershipByUserAndOrg :one
SELECT * FROM organization_memberships
WHERE user_id = $1 AND organization_id = $2;

-- name: OrganizationsForUser :many
SELECT o.* FROM organizations o
INNER JOIN organization_memberships m ON m.organization_id = o.id
WHERE m.user_id = $1
ORDER BY o.name ASC;

-- name: OrganizationsListAll :many
SELECT * FROM organizations ORDER BY name ASC;

-- name: OrganizationMembershipUpsertOwner :exec
INSERT INTO organization_memberships (id, organization_id, user_id, role)
VALUES ($1, $2, $3, 'owner')
ON CONFLICT (organization_id, user_id) DO UPDATE SET role = EXCLUDED.role;

-- name: UserUpdatePasswordHash :exec
UPDATE users SET password_hash = $2, updated_at = NOW() WHERE id = $1;

-- name: UserUpdateDisplayName :exec
UPDATE users SET display_name = $2, updated_at = NOW() WHERE id = $1;

-- name: UserSessionCreate :one
INSERT INTO user_sessions (id, user_id, token_hash, current_organization_id, expires_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: UserSessionByTokenHash :one
SELECT * FROM user_sessions
WHERE token_hash = $1 AND expires_at > NOW();

-- name: UserSessionDelete :exec
DELETE FROM user_sessions WHERE id = $1;

-- name: UserSessionDeleteAllForUser :exec
DELETE FROM user_sessions WHERE user_id = $1;

-- name: UserSessionUpdateCurrentOrg :one
UPDATE user_sessions
SET current_organization_id = $2
WHERE id = $1
RETURNING *;

-- name: OrgProviderConnectionListByOrg :many
SELECT * FROM org_provider_connections
WHERE organization_id = $1
ORDER BY provider ASC, display_label ASC;

-- name: OrgProviderConnectionCreate :one
INSERT INTO org_provider_connections (
    id, organization_id, provider, external_slug, display_label, credentials_ciphertext, metadata
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: OrgProviderConnectionByIDAndOrg :one
SELECT * FROM org_provider_connections
WHERE id = $1 AND organization_id = $2;

-- name: OrgProviderConnectionDeleteByIDAndOrg :exec
DELETE FROM org_provider_connections WHERE id = $1 AND organization_id = $2;

-- name: OrgProviderConnectionUpdate :one
UPDATE org_provider_connections
SET display_label = $2,
    external_slug = $3,
    credentials_ciphertext = $4,
    metadata = $5,
    updated_at = NOW()
WHERE id = $1 AND organization_id = $6
RETURNING *;

-- Usage metrics --

-- name: InstanceUsageSampleInsert :exec
INSERT INTO instance_usage_samples (
    id, server_id, project_id, deployment_id, deployment_instance_id,
    sampled_at, cpu_nanos_cumulative, cpu_percent, memory_rss_bytes, disk_read_bytes, disk_write_bytes, source
) VALUES (
    $1, $2, $3, $4, $5, NOW(), $6, $7, $8, $9, $10, $11
);

-- name: DeploymentInstancesRunningForUsageOnServer :many
SELECT
    di.id,
    di.deployment_id,
    d.project_id
FROM deployment_instances di
INNER JOIN deployments d ON d.id = di.deployment_id AND d.deleted_at IS NULL
WHERE di.server_id = $1
  AND di.deleted_at IS NULL
  AND di.role = 'active'
  AND di.status = 'running'
  AND di.vm_id IS NOT NULL;

-- name: InstanceUsageLatestPerInstance :many
SELECT DISTINCT ON (s.deployment_instance_id)
    s.deployment_instance_id,
    s.sampled_at,
    s.cpu_nanos_cumulative,
    s.cpu_percent,
    s.memory_rss_bytes,
    s.disk_read_bytes,
    s.disk_write_bytes,
    s.source,
    s.server_id
FROM instance_usage_samples s
INNER JOIN deployment_instances di ON di.id = s.deployment_instance_id
  AND di.deleted_at IS NULL
  AND di.status = 'running'
  AND di.vm_id IS NOT NULL
INNER JOIN deployments d ON d.id = di.deployment_id
  AND d.deleted_at IS NULL
  AND d.project_id = s.project_id
WHERE s.project_id = $1
  AND s.sampled_at >= $2
ORDER BY s.deployment_instance_id, s.sampled_at DESC;

-- name: ServerInstanceUsageLatest :many
SELECT
    di.id AS deployment_instance_id,
    di.deployment_id,
    d.project_id,
    p.name AS project_name,
    di.vm_id,
    di.role,
    di.status,
    di.created_at,
    di.updated_at,
    s.sampled_at,
    s.cpu_percent,
    s.memory_rss_bytes,
    s.disk_read_bytes,
    s.disk_write_bytes,
    s.source
FROM deployment_instances di
INNER JOIN deployments d ON d.id = di.deployment_id
  AND d.deleted_at IS NULL
INNER JOIN projects p ON p.id = d.project_id
LEFT JOIN LATERAL (
    SELECT sampled_at, cpu_percent, memory_rss_bytes, disk_read_bytes, disk_write_bytes, source
    FROM instance_usage_samples
    WHERE deployment_instance_id = di.id
    ORDER BY sampled_at DESC
    LIMIT 1
) s ON TRUE
WHERE di.server_id = $1
  AND di.deleted_at IS NULL
ORDER BY di.created_at ASC;

-- name: InstanceUsageLastBefore :one
SELECT sampled_at, cpu_nanos_cumulative
FROM instance_usage_samples
WHERE deployment_instance_id = $1
  AND sampled_at < $2
ORDER BY sampled_at DESC
LIMIT 1;

-- name: InstanceUsageSamplesByProjectWindow :many
SELECT
    date_trunc('minute', sampled_at)::timestamptz AS bucket_start,
    COALESCE(MAX(memory_rss_bytes), 0)::bigint AS memory_rss_bytes_max,
    COALESCE(AVG(cpu_percent), 0)::float8 AS cpu_percent_avg
FROM instance_usage_samples
WHERE project_id = $1
  AND sampled_at >= $2
  AND sampled_at <= $3
GROUP BY 1
ORDER BY 1 ASC;

-- name: ProjectHTTPUsageRollupIncrement :exec
INSERT INTO project_http_usage_rollups (
    id, server_id, project_id, deployment_id, bucket_start,
    request_count, status_2xx, status_4xx, status_5xx, bytes_in, bytes_out, updated_at
) VALUES (
    gen_random_uuid(), $1, $2, $3, $4,
    $5, $6, $7, $8, $9, $10, NOW()
)
ON CONFLICT (server_id, project_id, deployment_id, bucket_start)
DO UPDATE SET
    request_count = project_http_usage_rollups.request_count + EXCLUDED.request_count,
    status_2xx = project_http_usage_rollups.status_2xx + EXCLUDED.status_2xx,
    status_4xx = project_http_usage_rollups.status_4xx + EXCLUDED.status_4xx,
    status_5xx = project_http_usage_rollups.status_5xx + EXCLUDED.status_5xx,
    bytes_in = project_http_usage_rollups.bytes_in + EXCLUDED.bytes_in,
    bytes_out = project_http_usage_rollups.bytes_out + EXCLUDED.bytes_out,
    updated_at = NOW();

-- name: ProjectHTTPUsageRollupsAggregated :many
SELECT
    bucket_start,
    COALESCE(SUM(request_count), 0)::bigint AS request_count,
    COALESCE(SUM(status_2xx), 0)::bigint AS status_2xx,
    COALESCE(SUM(status_4xx), 0)::bigint AS status_4xx,
    COALESCE(SUM(status_5xx), 0)::bigint AS status_5xx,
    COALESCE(SUM(bytes_in), 0)::bigint AS bytes_in,
    COALESCE(SUM(bytes_out), 0)::bigint AS bytes_out
FROM project_http_usage_rollups
WHERE project_id = $1
  AND bucket_start >= $2
  AND bucket_start <= $3
GROUP BY bucket_start
ORDER BY bucket_start ASC;
