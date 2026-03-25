-- name: ServerRegister :one
INSERT INTO servers (id, hostname, internal_ip, ip_range, status, last_heartbeat_at)
VALUES ($1, $2, $3, $4, 'active', NOW())
ON CONFLICT (id) DO UPDATE SET
    hostname = EXCLUDED.hostname,
    internal_ip = EXCLUDED.internal_ip,
    status = 'active',
    last_heartbeat_at = NOW(),
    updated_at = NOW()
RETURNING *;

-- name: ServerFindByID :one
SELECT * FROM servers WHERE id = $1;

-- name: ServerHeartbeat :exec
UPDATE servers SET last_heartbeat_at = NOW(), updated_at = NOW() WHERE id = $1;

-- name: ServerFindDead :many
SELECT * FROM servers
WHERE status = 'active'
  AND last_heartbeat_at < NOW() - INTERVAL '30 seconds';

-- name: ServerUpdateStatus :exec
UPDATE servers SET status = $2, updated_at = NOW() WHERE id = $1;

-- name: ServerSetDrained :exec
UPDATE servers SET status = 'drained', updated_at = NOW() WHERE id = $1;

-- name: ServerFindLeastLoaded :one
SELECT s.* FROM servers s
LEFT JOIN vms v ON v.server_id = s.id AND v.deleted_at IS NULL
WHERE s.status = 'active'
GROUP BY s.id
ORDER BY COUNT(v.id) ASC
LIMIT 1;

-- name: ServerAllocateIPRange :one
SELECT CASE
    WHEN MAX(ip_range) IS NULL THEN '10.0.0.0/20'::CIDR
    ELSE (MAX(ip_range) + 1)::CIDR
END AS ip_range
FROM servers;

-- name: ServerFindAll :many
SELECT * FROM servers ORDER BY created_at;

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
INSERT INTO projects (id, name, github_repository, github_installation_id, github_webhook_secret, root_directory, dockerfile_path)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ProjectFirstByID :one
SELECT * FROM projects WHERE id = $1;

-- name: ProjectFindAll :many
SELECT * FROM projects ORDER BY created_at DESC;

-- name: ProjectFindByGitHubRepo :one
SELECT * FROM projects WHERE github_repository = $1;

-- name: ProjectDelete :exec
DELETE FROM projects WHERE id = $1;

-- name: ProjectUpdateWebhookSecret :one
UPDATE projects
SET github_webhook_secret = $2, updated_at = NOW()
WHERE id = $1
RETURNING *;

-- Environment Variables --

-- name: EnvironmentVariableCreate :one
INSERT INTO environment_variables (id, project_id, name, value)
VALUES ($1, $2, $3, $4)
ON CONFLICT (project_id, name) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()
RETURNING *;

-- name: EnvironmentVariableFindByProjectID :many
SELECT * FROM environment_variables WHERE project_id = $1 ORDER BY name;

-- name: EnvironmentVariableDelete :exec
DELETE FROM environment_variables WHERE id = $1;

-- VMs --

-- name: VMCreate :one
INSERT INTO vms (id, server_id, image_id, status, vcpus, memory, ip_address, port, env_variables)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: VMFirstByID :one
SELECT * FROM vms WHERE id = $1;

-- name: VMUpdateStatus :one
UPDATE vms SET status = $2, updated_at = NOW() WHERE id = $1 RETURNING *;

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
INSERT INTO deployments (id, project_id, github_commit)
VALUES ($1, $2, $3)
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

-- name: DeploymentMarkStopped :exec
UPDATE deployments SET stopped_at = NOW(), updated_at = NOW() WHERE id = $1;

-- name: DeploymentUpdateFailedAt :exec
UPDATE deployments SET failed_at = NOW(), updated_at = NOW() WHERE id = $1;

-- name: DeploymentFindByVMID :one
SELECT * FROM deployments WHERE vm_id = $1 AND deleted_at IS NULL;

-- name: DeploymentFindRunningAndOlder :many
SELECT * FROM deployments
WHERE project_id = $1
  AND running_at IS NOT NULL
  AND stopped_at IS NULL
  AND failed_at IS NULL
  AND deleted_at IS NULL
  AND id != $2
ORDER BY created_at;

-- name: DeploymentFindRunningByServerID :many
SELECT d.* FROM deployments d
JOIN vms v ON d.vm_id = v.id
WHERE v.server_id = $1
  AND d.running_at IS NOT NULL
  AND d.stopped_at IS NULL
  AND d.failed_at IS NULL
  AND d.deleted_at IS NULL;

-- name: DeploymentFindByProjectID :many
SELECT * FROM deployments WHERE project_id = $1 ORDER BY created_at DESC;

-- name: DeploymentLatestRunningByProjectID :one
SELECT * FROM deployments
WHERE project_id = $1
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
    d.running_at,
    d.stopped_at,
    d.failed_at,
    d.deleted_at,
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

-- name: BuildLogsAfterCreatedAt :many
SELECT * FROM build_logs
WHERE build_id = $1 AND created_at > $2
ORDER BY created_at;

-- Domains --

-- name: DomainCreate :one
INSERT INTO domains (id, project_id, domain_name)
VALUES ($1, $2, $3)
RETURNING *;

-- name: DomainVerified :one
SELECT verified_at FROM domains WHERE domain_name = $1;

-- name: DomainUpdateDeploymentForProject :exec
UPDATE domains SET deployment_id = $1, updated_at = NOW() WHERE project_id = $2;

-- name: DomainFindVerifiedByDeploymentID :many
SELECT *
FROM domains
WHERE deployment_id = $1
  AND verified_at IS NOT NULL
ORDER BY domain_name ASC;

-- name: RouteFindActive :many
SELECT d.domain_name,
       d.redirect_to,
       d.redirect_status_code,
       v.ip_address AS vm_ip,
       v.port AS vm_port,
       v.server_id,
       v.id AS vm_id
FROM domains d
LEFT JOIN deployments dep ON d.deployment_id = dep.id
LEFT JOIN vms v ON dep.vm_id = v.id AND v.deleted_at IS NULL
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
