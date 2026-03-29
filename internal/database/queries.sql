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

-- name: ServerSettingSeedCloudHypervisorStateDirIfUnset :exec
UPDATE server_settings
SET cloud_hypervisor_state_dir = $2, updated_at = NOW()
WHERE server_id = $1
  AND (cloud_hypervisor_state_dir = '' OR BTRIM(cloud_hypervisor_state_dir) = '');

-- name: InstanceMigrationCreate :one
INSERT INTO instance_migrations (
    id, deployment_instance_id, source_server_id, destination_server_id, source_vm_id, state, mode,
    receive_addr, receive_token_hash, destination_runtime_url, failure_code, failure_message, cutover_deadline_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7,
    '', $8, '', '', '', $9
)
RETURNING *;

-- name: InstanceMigrationFirstByID :one
SELECT * FROM instance_migrations WHERE id = $1;

-- name: InstanceMigrationLatestByDeploymentInstanceID :one
SELECT * FROM instance_migrations
WHERE deployment_instance_id = $1
ORDER BY started_at DESC
LIMIT 1;

-- name: InstanceMigrationFindActiveByDeploymentInstanceID :one
SELECT * FROM instance_migrations
WHERE deployment_instance_id = $1
  AND state NOT IN ('completed', 'failed', 'aborted', 'fallback_evacuating')
ORDER BY started_at DESC
LIMIT 1;

-- name: InstanceMigrationCountActiveByServerID :one
SELECT COUNT(*)::bigint AS count
FROM instance_migrations
WHERE state NOT IN ('completed', 'failed', 'aborted', 'fallback_evacuating')
  AND (source_server_id = $1 OR destination_server_id = $1);

-- name: InstanceMigrationUpdatePrepared :one
UPDATE instance_migrations
SET state = 'destination_prepared',
    receive_addr = $2,
    cutover_deadline_at = $3,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: InstanceMigrationUpdateSending :one
UPDATE instance_migrations
SET state = 'sending',
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: InstanceMigrationUpdateReceived :one
UPDATE instance_migrations
SET state = 'received',
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: InstanceMigrationUpdateCompleted :one
UPDATE instance_migrations
SET state = 'completed',
    destination_runtime_url = $2,
    completed_at = NOW(),
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: InstanceMigrationUpdateFallbackEvacuating :one
UPDATE instance_migrations
SET state = 'fallback_evacuating',
    failure_code = $2,
    failure_message = $3,
    failed_at = NOW(),
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: InstanceMigrationUpdateFailed :one
UPDATE instance_migrations
SET state = 'failed',
    failure_code = $2,
    failure_message = $3,
    failed_at = NOW(),
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: InstanceMigrationUpdateAborted :one
UPDATE instance_migrations
SET state = 'aborted',
    failure_code = $2,
    failure_message = $3,
    aborted_at = NOW(),
    updated_at = NOW()
WHERE id = $1
RETURNING *;

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

-- Auth providers --

-- name: AuthProviderListAll :many
SELECT * FROM auth_providers
ORDER BY provider ASC;

-- name: AuthProviderListEnabled :many
SELECT * FROM auth_providers
WHERE enabled = TRUE
  AND BTRIM(client_id) <> ''
ORDER BY provider ASC;

-- name: AuthProviderByProvider :one
SELECT * FROM auth_providers WHERE provider = $1;

-- name: AuthProviderUpsert :one
INSERT INTO auth_providers (
    provider, display_name, enabled, client_id, client_secret_ciphertext,
    issuer_url, auth_url, token_url, userinfo_url, scopes, metadata, updated_at
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, $8, $9, $10, $11, NOW()
)
ON CONFLICT (provider) DO UPDATE SET
    display_name = EXCLUDED.display_name,
    enabled = EXCLUDED.enabled,
    client_id = EXCLUDED.client_id,
    client_secret_ciphertext = EXCLUDED.client_secret_ciphertext,
    issuer_url = EXCLUDED.issuer_url,
    auth_url = EXCLUDED.auth_url,
    token_url = EXCLUDED.token_url,
    userinfo_url = EXCLUDED.userinfo_url,
    scopes = EXCLUDED.scopes,
    metadata = EXCLUDED.metadata,
    updated_at = NOW()
RETURNING *;

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
WITH inserted AS (
    INSERT INTO projects (
        id, org_id, name, github_repository, github_installation_id, github_webhook_secret,
        root_directory, dockerfile_path, desired_instance_count, min_instance_count, max_instance_count,
        scale_to_zero_enabled, build_only_on_root_changes
    )
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
    RETURNING *
), primary_service AS (
    INSERT INTO services (
        id, project_id, name, slug, root_directory, dockerfile_path,
        desired_instance_count, build_only_on_root_changes, public_default, is_primary
    )
    SELECT gen_random_uuid(), id, name, 'app', root_directory, dockerfile_path,
           desired_instance_count, build_only_on_root_changes, false, true
    FROM inserted
    RETURNING id, project_id, public_default
), primary_endpoint AS (
    INSERT INTO service_endpoints (
        id, service_id, name, protocol, target_port, visibility, private_ip, public_hostname
    )
    SELECT
        gen_random_uuid(),
        ps.id,
        'web',
        'http',
        3000,
        CASE WHEN ps.public_default THEN 'public' ELSE 'private' END,
        (COALESCE(MAX(se.private_ip), (host(network(n.cidr))::INET + 9)::INET) + 1)::INET,
        ''
    FROM primary_service ps
    JOIN projects p ON p.id = ps.project_id
    JOIN org_networks n ON n.organization_id = p.org_id
    LEFT JOIN projects p2 ON p2.org_id = p.org_id
    LEFT JOIN services s2 ON s2.project_id = p2.id
    LEFT JOIN service_endpoints se ON se.service_id = s2.id
    GROUP BY ps.id, ps.public_default, n.cidr
)
SELECT * FROM inserted;

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

-- name: ProjectSetDesiredInstanceCount :one
UPDATE projects
SET desired_instance_count = $2, updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: ProjectUpdateScaleToZeroEnabled :one
UPDATE projects
SET scale_to_zero_enabled = $2, updated_at = NOW()
WHERE id = $1 AND org_id = $3
RETURNING *;

-- name: ProjectUpdateScalingConfig :one
UPDATE projects
SET min_instance_count = $2,
    max_instance_count = $3,
    scale_to_zero_enabled = $4,
    desired_instance_count = $5,
    updated_at = NOW()
WHERE id = $1 AND org_id = $6
RETURNING *;

-- name: ProjectUpdateBuildOnlyOnRootChanges :one
UPDATE projects
SET build_only_on_root_changes = $2, updated_at = NOW()
WHERE id = $1 AND org_id = $3
RETURNING *;

-- Services --

-- name: ServiceCreate :one
WITH inserted AS (
    INSERT INTO services (
        id, project_id, name, slug, root_directory, dockerfile_path,
        desired_instance_count, build_only_on_root_changes, public_default, is_primary
    )
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
    RETURNING *
), endpoint AS (
    INSERT INTO service_endpoints (
        id, service_id, name, protocol, target_port, visibility, private_ip, public_hostname
    )
    SELECT
        gen_random_uuid(),
        i.id,
        'web',
        'http',
        3000,
        CASE WHEN i.public_default THEN 'public' ELSE 'private' END,
        (COALESCE(MAX(se.private_ip), (host(network(n.cidr))::INET + 9)::INET) + 1)::INET,
        ''
    FROM inserted i
    JOIN projects p ON p.id = i.project_id
    JOIN org_networks n ON n.organization_id = p.org_id
    LEFT JOIN projects p2 ON p2.org_id = p.org_id
    LEFT JOIN services s2 ON s2.project_id = p2.id
    LEFT JOIN service_endpoints se ON se.service_id = s2.id
    GROUP BY i.id, i.public_default, n.cidr
)
SELECT * FROM inserted;

-- name: ServiceListByProjectID :many
SELECT * FROM services WHERE project_id = $1 ORDER BY is_primary DESC, created_at ASC;

-- name: DeploymentFindByServiceID :many
SELECT * FROM deployments
WHERE service_id = $1
  AND deleted_at IS NULL
ORDER BY created_at DESC;

-- name: ServiceFirstByID :one
SELECT * FROM services WHERE id = $1;

-- name: ServiceFirstByIDAndOrg :one
SELECT s.*
FROM services s
JOIN projects p ON p.id = s.project_id
WHERE s.id = $1 AND p.org_id = $2;

-- name: ServicePrimaryByProjectID :one
SELECT * FROM services
WHERE project_id = $1 AND is_primary = true
LIMIT 1;

-- name: ServiceSyncPrimaryFromProject :one
UPDATE services s
SET name = p.name,
    root_directory = p.root_directory,
    dockerfile_path = p.dockerfile_path,
    desired_instance_count = p.desired_instance_count,
    build_only_on_root_changes = p.build_only_on_root_changes,
    updated_at = NOW()
FROM projects p
WHERE s.project_id = p.id
  AND s.project_id = $1
  AND s.is_primary = true
RETURNING s.*;

-- name: ServiceEndpointListByServiceID :many
SELECT * FROM service_endpoints WHERE service_id = $1 ORDER BY created_at ASC;

-- name: ServiceEndpointFirstByIDAndServiceID :one
SELECT * FROM service_endpoints
WHERE id = $1 AND service_id = $2;

-- name: ServiceEndpointDNSLookupCandidates :many
SELECT
    se.id AS endpoint_id,
    se.name AS endpoint_name,
    se.protocol AS endpoint_protocol,
    se.target_port AS endpoint_target_port,
    se.visibility AS endpoint_visibility,
    se.private_ip AS endpoint_private_ip,
    s.id AS service_id,
    s.slug AS service_slug,
    p.id AS project_id,
    p.name AS project_name,
    o.id AS organization_id,
    o.slug AS organization_slug
FROM service_endpoints se
JOIN services s ON s.id = se.service_id
JOIN projects p ON p.id = s.project_id
JOIN organizations o ON o.id = p.org_id
WHERE LOWER(se.name) = LOWER($1)
  AND s.slug = $2
  AND o.slug = $3
ORDER BY p.created_at ASC, s.created_at ASC, se.created_at ASC;

-- name: ServiceEndpointCreate :one
WITH svc AS (
    SELECT s.id, p.org_id
    FROM services s
    JOIN projects p ON p.id = s.project_id
    WHERE s.id = $1
), inserted AS (
    INSERT INTO service_endpoints (
        id, service_id, name, protocol, target_port, visibility, private_ip, public_hostname
    )
    SELECT
        $2,
        svc.id,
        $3,
        $4,
        $5,
        $6,
        (COALESCE(MAX(se.private_ip), (host(network(n.cidr))::INET + 9)::INET) + 1)::INET,
        $7
    FROM svc
    JOIN org_networks n ON n.organization_id = svc.org_id
    LEFT JOIN projects p2 ON p2.org_id = svc.org_id
    LEFT JOIN services s2 ON s2.project_id = p2.id
    LEFT JOIN service_endpoints se ON se.service_id = s2.id
    GROUP BY svc.id, n.cidr
    RETURNING *
)
SELECT * FROM inserted;

-- name: ServiceEndpointUpdateByIDAndServiceID :one
UPDATE service_endpoints
SET name = $3,
    protocol = $4,
    target_port = $5,
    visibility = $6,
    updated_at = NOW()
WHERE id = $1 AND service_id = $2
RETURNING *;

-- name: ServiceEndpointDeleteByIDAndServiceID :exec
DELETE FROM service_endpoints
WHERE id = $1 AND service_id = $2;

-- name: OrgNetworkByOrganizationID :one
SELECT * FROM org_networks WHERE organization_id = $1;

-- name: ProjectUpdateLastRequestAt :exec
UPDATE projects SET last_request_at = NOW(), updated_at = NOW() WHERE id = $1;

-- name: ProjectClearScaledToZero :exec
UPDATE projects SET scaled_to_zero = false, updated_at = NOW() WHERE id = $1;

-- name: ProjectMarkScaledToZero :exec
UPDATE projects
SET scaled_to_zero = true, updated_at = NOW()
WHERE id = $1
  AND scale_to_zero_enabled = true
  AND max_instance_count > 0
  AND scaled_to_zero = false;

-- name: ProjectFindAll :many
SELECT * FROM projects ORDER BY created_at DESC;

-- name: ProjectsFindForIdleScaleDown :many
SELECT * FROM projects
WHERE scale_to_zero_enabled = true
  AND max_instance_count > 0
  AND scaled_to_zero = false
  AND last_request_at IS NOT NULL
  AND last_request_at < NOW() - ($1::bigint * INTERVAL '1 second')
ORDER BY last_request_at ASC
LIMIT 100;

-- Project volumes --

-- name: ProjectVolumeFindByProjectID :one
SELECT * FROM project_volumes
WHERE project_id = $1 AND deleted_at IS NULL;

-- name: ProjectVolumeFindByServiceID :one
SELECT * FROM project_volumes
WHERE service_id = $1 AND deleted_at IS NULL;

-- name: ProjectVolumeFindAnyByProjectID :one
SELECT * FROM project_volumes
WHERE project_id = $1
LIMIT 1;

-- name: ProjectVolumeFindAnyByServiceID :one
SELECT * FROM project_volumes
WHERE service_id = $1
LIMIT 1;

-- name: ProjectVolumeFindByServerID :many
SELECT * FROM project_volumes
WHERE server_id = $1 AND deleted_at IS NULL
ORDER BY created_at ASC;

-- name: ProjectVolumeCreate :one
INSERT INTO project_volumes (
    id, project_id, service_id, mount_path, size_gb, filesystem, status, health, backup_schedule, backup_retention_count, pre_delete_backup_enabled
) VALUES (
    $1, $2, $3, $4, $5, $6, 'detached', 'unknown', $7, $8, $9
)
RETURNING *;

-- name: ProjectVolumeUpdateSpec :one
UPDATE project_volumes
SET size_gb = $2,
    filesystem = $3,
    backup_schedule = $4,
    backup_retention_count = $5,
    pre_delete_backup_enabled = $6,
    updated_at = NOW()
WHERE project_id = $1
  AND deleted_at IS NULL
RETURNING *;

-- name: ProjectVolumeRevive :one
UPDATE project_volumes
SET size_gb = $2,
    filesystem = $3,
    server_id = NULL,
    attached_vm_id = NULL,
    status = 'detached',
    health = 'unknown',
    backup_schedule = $4,
    backup_retention_count = $5,
    pre_delete_backup_enabled = $6,
    last_error = '',
    deleted_at = NULL,
    updated_at = NOW()
WHERE project_id = $1
RETURNING *;

-- name: ProjectVolumeAssignServer :one
UPDATE project_volumes
SET server_id = $2,
    updated_at = NOW()
WHERE project_id = $1
  AND deleted_at IS NULL
RETURNING *;

-- name: ProjectVolumeFindByID :one
SELECT * FROM project_volumes
WHERE id = $1
  AND deleted_at IS NULL;

-- name: ProjectVolumeAttachVM :one
UPDATE project_volumes
SET server_id = $2,
    attached_vm_id = $3,
    status = 'attached',
    health = 'healthy',
    last_error = '',
    updated_at = NOW()
WHERE project_id = $1
  AND deleted_at IS NULL
RETURNING *;

-- name: ProjectVolumeDetachVM :one
UPDATE project_volumes
SET attached_vm_id = NULL,
    status = $2,
    last_error = $3,
    updated_at = NOW()
WHERE project_id = $1
  AND deleted_at IS NULL
RETURNING *;

-- name: ProjectVolumeUpdateStatus :one
UPDATE project_volumes
SET status = $2,
    last_error = $3,
    updated_at = NOW()
WHERE project_id = $1
  AND deleted_at IS NULL
RETURNING *;

-- name: ProjectVolumeUpdateStatusAndHealth :one
UPDATE project_volumes
SET status = $2,
    health = $3,
    last_error = $4,
    updated_at = NOW()
WHERE project_id = $1
  AND deleted_at IS NULL
RETURNING *;

-- name: ProjectVolumeUpdateHealth :one
UPDATE project_volumes
SET health = $2,
    last_error = $3,
    updated_at = NOW()
WHERE project_id = $1
  AND deleted_at IS NULL
RETURNING *;

-- name: ProjectVolumeSoftDelete :exec
UPDATE project_volumes
SET server_id = NULL,
    attached_vm_id = NULL,
    status = 'detached',
    health = 'unknown',
    last_error = '',
    deleted_at = NOW(),
    updated_at = NOW()
WHERE project_id = $1
  AND deleted_at IS NULL;

-- name: ProjectVolumeOperationCreate :one
INSERT INTO project_volume_operations (
    id, project_volume_id, server_id, kind, status, request_metadata, result_metadata, error
) VALUES (
    $1, $2, $3, $4, 'pending', $5, '{}'::jsonb, ''
)
RETURNING *;

-- name: ProjectVolumeOperationFindByID :one
SELECT * FROM project_volume_operations
WHERE id = $1;

-- name: ProjectVolumeOperationFindCurrentByVolumeID :one
SELECT * FROM project_volume_operations
WHERE project_volume_id = $1
  AND status IN ('pending', 'running')
ORDER BY created_at DESC
LIMIT 1;

-- name: ProjectVolumeOperationFindStalePending :many
SELECT * FROM project_volume_operations
WHERE status = 'pending'
  AND created_at < NOW() - ($1::bigint * INTERVAL '1 second')
ORDER BY created_at ASC;

-- name: ProjectVolumeOperationFindStaleRunning :many
SELECT * FROM project_volume_operations
WHERE status = 'running'
  AND started_at IS NOT NULL
  AND started_at < NOW() - ($1::bigint * INTERVAL '1 second')
ORDER BY started_at ASC;

-- name: ProjectVolumeOperationUpdateRunning :one
UPDATE project_volume_operations
SET status = 'running',
    error = '',
    started_at = NOW(),
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: ProjectVolumeOperationUpdateState :one
UPDATE project_volume_operations
SET server_id = $2,
    status = $3,
    request_metadata = $4,
    result_metadata = $5,
    error = $6,
    started_at = CASE WHEN $3 = 'running' AND started_at IS NULL THEN NOW() ELSE started_at END,
    completed_at = CASE WHEN $3 = 'succeeded' THEN NOW() ELSE completed_at END,
    failed_at = CASE WHEN $3 = 'failed' THEN NOW() ELSE failed_at END,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: ProjectVolumeBackupCreate :one
INSERT INTO project_volume_backups (
    id, project_volume_id, kind, status, storage_url, storage_key, size_bytes, error, metadata
) VALUES (
    $1, $2, $3, 'pending', '', '', 0, '', $4
)
RETURNING *;

-- name: ProjectVolumeBackupFindByID :one
SELECT * FROM project_volume_backups
WHERE id = $1;

-- name: ProjectVolumeBackupFindByProjectID :many
SELECT b.*
FROM project_volume_backups b
JOIN project_volumes v ON v.id = b.project_volume_id
WHERE v.project_id = $1
  AND v.deleted_at IS NULL
ORDER BY b.created_at DESC;

-- name: ProjectVolumeBackupFindLastSuccessfulByProjectID :one
SELECT b.*
FROM project_volume_backups b
JOIN project_volumes v ON v.id = b.project_volume_id
WHERE v.project_id = $1
  AND v.deleted_at IS NULL
  AND b.status = 'succeeded'
ORDER BY b.completed_at DESC, b.created_at DESC
LIMIT 1;

-- name: ProjectVolumeBackupDeleteByID :exec
DELETE FROM project_volume_backups
WHERE id = $1;

-- name: ProjectVolumeBackupUpdateState :one
UPDATE project_volume_backups
SET status = $2,
    storage_url = $3,
    storage_key = $4,
    size_bytes = $5,
    error = $6,
    metadata = $7,
    started_at = CASE WHEN $2 = 'running' AND started_at IS NULL THEN NOW() ELSE started_at END,
    completed_at = CASE WHEN $2 = 'succeeded' THEN NOW() ELSE completed_at END,
    failed_at = CASE WHEN $2 = 'failed' THEN NOW() ELSE failed_at END,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

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

-- name: DeploymentInstanceRetainedStateByServerID :many
SELECT
    di.id AS deployment_instance_id,
    di.vm_id,
    v.snapshot_ref
FROM deployment_instances di
INNER JOIN deployments d ON d.id = di.deployment_id
  AND d.deleted_at IS NULL
  AND d.stopped_at IS NULL
  AND d.failed_at IS NULL
LEFT JOIN vms v ON v.id = di.vm_id
  AND v.deleted_at IS NULL
WHERE di.server_id = $1
  AND di.deleted_at IS NULL;

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

-- name: EnvironmentVariableUpsertProjectDefault :one
WITH updated AS (
    UPDATE environment_variables ev
    SET value = $3, updated_at = NOW()
    WHERE ev.project_id = $1
      AND ev.service_id IS NULL
      AND ev.name = $2
    RETURNING *
), inserted AS (
    INSERT INTO environment_variables (id, project_id, service_id, name, value)
    SELECT $4, $1, NULL, $2, $3
    WHERE NOT EXISTS (SELECT 1 FROM updated)
    RETURNING *
)
SELECT * FROM updated
UNION ALL
SELECT * FROM inserted;

-- name: EnvironmentVariableUpsertForService :one
WITH updated AS (
    UPDATE environment_variables ev
    SET value = $4, updated_at = NOW()
    WHERE ev.project_id = $1
      AND ev.service_id = $2
      AND ev.name = $3
    RETURNING *
), inserted AS (
    INSERT INTO environment_variables (id, project_id, service_id, name, value)
    SELECT $5, $1, $2, $3, $4
    WHERE NOT EXISTS (SELECT 1 FROM updated)
    RETURNING *
)
SELECT * FROM updated
UNION ALL
SELECT * FROM inserted;

-- name: EnvironmentVariableFindAll :many
SELECT * FROM environment_variables ORDER BY project_id, name;

-- name: EnvironmentVariableFindByProjectID :many
SELECT * FROM environment_variables WHERE project_id = $1 ORDER BY name;

-- name: EnvironmentVariableFindEffectiveByServiceID :many
SELECT DISTINCT ON (ev.name) ev.*
FROM environment_variables ev
JOIN services s ON s.id = $1
WHERE ev.project_id = s.project_id
  AND (ev.service_id = s.id OR ev.service_id IS NULL)
ORDER BY ev.name ASC,
         CASE WHEN ev.service_id = s.id THEN 0 ELSE 1 END,
         ev.updated_at DESC;

-- name: EnvironmentVariableMetadataFindProjectDefaultsByProjectID :many
SELECT id, project_id, service_id, name, created_at, updated_at
FROM environment_variables
WHERE project_id = $1
  AND service_id IS NULL
ORDER BY name;

-- name: EnvironmentVariableMetadataFindEffectiveByServiceID :many
SELECT DISTINCT ON (ev.name)
    ev.id,
    ev.project_id,
    ev.service_id,
    ev.name,
    ev.created_at,
    ev.updated_at
FROM environment_variables ev
JOIN services s ON s.id = $1
WHERE ev.project_id = s.project_id
  AND (ev.service_id = s.id OR ev.service_id IS NULL)
ORDER BY ev.name ASC,
         CASE WHEN ev.service_id = s.id THEN 0 ELSE 1 END,
         ev.updated_at DESC;

-- name: EnvironmentVariableUpdateValue :one
UPDATE environment_variables
SET value = $2, updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: EnvironmentVariableDeleteByIDAndProjectID :one
DELETE FROM environment_variables
WHERE id = $1 AND project_id = $2
RETURNING *;

-- name: EnvironmentVariableDeleteProjectDefaultByIDAndProjectID :one
DELETE FROM environment_variables
WHERE id = $1
  AND project_id = $2
  AND service_id IS NULL
RETURNING *;

-- name: EnvironmentVariableDeleteByIDAndServiceID :one
DELETE FROM environment_variables
WHERE id = $1
  AND service_id = $2
RETURNING *;

-- VMs --

-- name: VMCreate :one
INSERT INTO vms (id, server_id, image_id, status, runtime, snapshot_ref, shared_rootfs_ref, clone_source_vm_id, vcpus, memory, ip_address, port, env_variables)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: VMFirstByID :one
SELECT * FROM vms WHERE id = $1;

-- name: VMFindByIPAddress :one
SELECT * FROM vms
WHERE ip_address = $1
  AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT 1;

-- name: VMUpdateStatus :one
UPDATE vms SET status = $2, updated_at = NOW() WHERE id = $1 RETURNING *;

-- name: VMUpdateLifecycleMetadata :one
UPDATE vms
SET status = $2,
    snapshot_ref = $3,
    shared_rootfs_ref = $4,
    clone_source_vm_id = $5,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: VMUpdateRuntimeAddress :one
UPDATE vms
SET ip_address = $2,
    port = $3,
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
INSERT INTO builds (id, project_id, service_id, status, github_commit, github_branch)
VALUES ($1, $2, $3, $4, $5, $6)
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

-- CI jobs --

-- name: CIJobCreate :one
INSERT INTO ci_jobs (
  id, project_id, status, source, workflow_name, workflow_file, selected_job_id,
  event_name, input_values, input_archive_path, require_microvm, workspace_dir, error_message
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: CIJobCreateGitHubRunner :one
INSERT INTO ci_jobs (
  id, project_id, status, source, workflow_name, workflow_file, selected_job_id,
  event_name, input_values, input_archive_path, provider_connection_id, external_repo,
  external_installation_id, external_workflow_job_id, external_workflow_run_id, external_run_attempt,
  external_html_url, runner_labels, runner_name, require_microvm, workspace_dir, error_message
)
VALUES (
  $1, $2, $3, 'github_actions_runner', $4, $5, $6, $7, $8, $9, $10, $11,
  $12, $13, $14, $15, $16, $17, $18, $19, $20, $21
)
RETURNING *;

-- name: CIJobFirstByID :one
SELECT * FROM ci_jobs WHERE id = $1;

-- name: CIJobFirstByExternalWorkflowJobID :one
SELECT * FROM ci_jobs
WHERE source = 'github_actions_runner' AND external_workflow_job_id = $1;

-- name: CIJobFirstByIDAndOrg :one
SELECT j.*
FROM ci_jobs j
JOIN projects p ON p.id = j.project_id
WHERE j.id = $1 AND p.org_id = $2;

-- name: CIJobFindByProjectID :many
SELECT * FROM ci_jobs WHERE project_id = $1 ORDER BY created_at DESC;

-- name: CIJobFindRecentWithProjectForOrg :many
SELECT j.*, p.name AS project_name
FROM ci_jobs j
JOIN projects p ON p.id = j.project_id
WHERE p.org_id = $1
ORDER BY j.created_at DESC
LIMIT $2;

-- name: CIJobClaimLease :one
UPDATE ci_jobs SET processing_by = $2, updated_at = NOW()
WHERE id = $1 AND status = 'queued' AND (processing_by IS NULL OR processing_by = $2)
RETURNING *;

-- name: CIJobReleaseLease :exec
UPDATE ci_jobs SET processing_by = NULL, updated_at = NOW()
WHERE id = $1 AND processing_by = $2;

-- name: CIJobMarkRunning :exec
UPDATE ci_jobs
SET status = 'running',
    workspace_dir = $2,
    execution_backend = $3,
    started_at = NOW(),
    updated_at = NOW()
WHERE id = $1;

-- name: CIJobMarkSuccessful :exec
UPDATE ci_jobs
SET status = 'successful',
    exit_code = $2,
    finished_at = NOW(),
    updated_at = NOW()
WHERE id = $1;

-- name: CIJobMarkFailed :exec
UPDATE ci_jobs
SET status = 'failed',
    exit_code = $2,
    error_message = $3,
    finished_at = NOW(),
    updated_at = NOW()
WHERE id = $1;

-- name: CIJobMarkCanceled :exec
UPDATE ci_jobs
SET status = 'canceled',
    canceled_at = NOW(),
    finished_at = NOW(),
    updated_at = NOW()
WHERE id = $1;

-- name: CIJobUpdateInputArchivePath :exec
UPDATE ci_jobs SET input_archive_path = $2, updated_at = NOW() WHERE id = $1;

-- name: CIJobLogCreate :exec
INSERT INTO ci_job_logs (id, ci_job_id, message, level)
VALUES ($1, $2, $3, $4);

-- name: CIJobLogsByJobID :many
SELECT * FROM ci_job_logs WHERE ci_job_id = $1 ORDER BY created_at;

-- name: CIJobArtifactDeleteByJobID :exec
DELETE FROM ci_job_artifacts WHERE ci_job_id = $1;

-- name: CIJobArtifactCreate :exec
INSERT INTO ci_job_artifacts (id, ci_job_id, name, path)
VALUES ($1, $2, $3, $4);

-- name: CIJobArtifactsByJobID :many
SELECT * FROM ci_job_artifacts WHERE ci_job_id = $1 ORDER BY created_at, name;

-- Deployments --

-- name: DeploymentCreate :one
INSERT INTO deployments (
  id,
  project_id,
  service_id,
  build_id,
  image_id,
  promoted_from_deployment_id,
  github_commit,
  github_branch,
  deployment_kind,
  preview_environment_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
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

-- name: DeploymentLatestRunningByServiceID :one
SELECT * FROM deployments
WHERE service_id = $1
  AND deployment_kind = 'production'
  AND running_at IS NOT NULL
  AND stopped_at IS NULL
  AND failed_at IS NULL
  AND deleted_at IS NULL
ORDER BY running_at DESC
LIMIT 1;

-- name: DeploymentLatestRunningPreviewByServiceAndPreviewEnvironmentID :one
SELECT * FROM deployments
WHERE service_id = $1
  AND preview_environment_id = $2
  AND deployment_kind = 'preview'
  AND running_at IS NOT NULL
  AND stopped_at IS NULL
  AND failed_at IS NULL
  AND deleted_at IS NULL
ORDER BY running_at DESC
LIMIT 1;

-- name: DeploymentRunningBackendIPs :many
SELECT DISTINCT vm.ip_address
FROM deployment_instances di
JOIN vms vm ON vm.id = di.vm_id
WHERE di.deployment_id = $1
  AND di.deleted_at IS NULL
  AND di.role = 'active'
  AND di.status = 'running'
  AND vm.deleted_at IS NULL
  AND vm.status = 'running'
ORDER BY vm.ip_address;

-- name: DeploymentFindRecentWithProject :many
SELECT
    d.id,
    d.project_id,
    d.service_id,
    d.build_id,
    d.image_id,
    d.vm_id,
    d.promoted_from_deployment_id,
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
    d.service_id,
    d.build_id,
    d.image_id,
    d.vm_id,
    d.promoted_from_deployment_id,
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
INSERT INTO preview_environments (id, project_id, service_id, provider, pr_number, head_branch, head_sha, stable_domain_name)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
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

-- name: PreviewEnvironmentReopen :one
UPDATE preview_environments
SET head_branch = $2,
    head_sha = $3,
    closed_at = NULL,
    expires_at = NULL,
    updated_at = NOW()
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

-- name: PreviewEnvironmentsByProjectAndPRNumber :many
SELECT * FROM preview_environments
WHERE project_id = $1 AND pr_number = $2
ORDER BY updated_at DESC;

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
SELECT deployments.*
FROM deployments
JOIN preview_environments pe ON pe.id = deployments.preview_environment_id
WHERE deployment_kind = 'preview'
  AND pe.closed_at IS NULL
  AND running_at IS NOT NULL
  AND stopped_at IS NULL
  AND failed_at IS NULL
  AND deleted_at IS NULL
  AND preview_scaled_to_zero = false
  AND preview_last_request_at IS NOT NULL
  AND preview_last_request_at < NOW() - ($1::bigint * INTERVAL '1 second')
ORDER BY deployments.preview_last_request_at ASC
LIMIT 100;

-- Sandboxes --

-- name: SandboxCreate :one
INSERT INTO sandboxes (
  id, org_id, name, host_group, backend, arch, desired_state, observed_state,
  server_id, vm_id, template_id, base_image_ref, vcpu, memory_mb, disk_gb,
  env_json, git_repo, git_ref, auto_suspend_seconds, last_used_at, expires_at,
  published_http_port, runtime_url, failure_message, created_by_user_id
)
VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8,
  $9, $10, $11, $12, $13, $14, $15,
  $16, $17, $18, $19, $20, $21,
  $22, $23, $24, $25
)
RETURNING *;

-- name: SandboxFirstByID :one
SELECT * FROM sandboxes WHERE id = $1;

-- name: SandboxFirstByIDAndOrg :one
SELECT * FROM sandboxes
WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL;

-- name: SandboxListByOrg :many
SELECT * FROM sandboxes
WHERE org_id = $1 AND deleted_at IS NULL
ORDER BY updated_at DESC;

-- name: SandboxUpdateDesiredState :one
UPDATE sandboxes
SET desired_state = $2,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: SandboxUpdatePlacement :one
UPDATE sandboxes
SET host_group = $2,
    backend = $3,
    arch = $4,
    server_id = $5,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: SandboxAttachVM :one
UPDATE sandboxes
SET vm_id = $2,
    observed_state = $3,
    runtime_url = $4,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: SandboxUpdateObservedState :one
UPDATE sandboxes
SET observed_state = $2,
    runtime_url = $3,
    failure_message = $4,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: SandboxUpdateLastUsedAt :exec
UPDATE sandboxes
SET last_used_at = NOW(),
    updated_at = NOW()
WHERE id = $1
  AND deleted_at IS NULL;

-- name: SandboxUpdatePublishPort :one
UPDATE sandboxes
SET published_http_port = $2,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: SandboxClearVM :one
UPDATE sandboxes
SET vm_id = NULL,
    runtime_url = '',
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: SandboxMarkDeleted :one
UPDATE sandboxes
SET observed_state = 'deleted',
    deleted_at = NOW(),
    runtime_url = '',
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: SandboxesDueForExpiry :many
SELECT * FROM sandboxes
WHERE deleted_at IS NULL
  AND expires_at IS NOT NULL
  AND expires_at <= NOW();

-- name: SandboxesDueForIdleSuspend :many
SELECT * FROM sandboxes
WHERE deleted_at IS NULL
  AND desired_state = 'running'
  AND observed_state = 'running'
  AND auto_suspend_seconds > 0
  AND last_used_at IS NOT NULL
  AND last_used_at < NOW() - (auto_suspend_seconds * INTERVAL '1 second');

-- name: SandboxFindByServerID :many
SELECT * FROM sandboxes
WHERE server_id = $1
  AND deleted_at IS NULL
ORDER BY updated_at DESC;

-- name: SandboxTemplateCreate :one
INSERT INTO sandbox_templates (
  id, org_id, name, host_group, backend, arch, source_sandbox_id, server_id,
  base_image_ref, snapshot_ref, vcpu, memory_mb, disk_gb, status, failure_message, created_by_user_id
)
VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8,
  $9, $10, $11, $12, $13, $14, $15, $16
)
RETURNING *;

-- name: SandboxTemplateFirstByID :one
SELECT * FROM sandbox_templates WHERE id = $1;

-- name: SandboxTemplateFirstByIDAndOrg :one
SELECT * FROM sandbox_templates
WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL;

-- name: SandboxTemplateListByOrg :many
SELECT * FROM sandbox_templates
WHERE org_id = $1 AND deleted_at IS NULL
ORDER BY updated_at DESC;

-- name: SandboxTemplateMarkReady :one
UPDATE sandbox_templates
SET status = 'ready',
    server_id = $2,
    snapshot_ref = $3,
    failure_message = '',
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: SandboxTemplateMarkFailed :one
UPDATE sandbox_templates
SET status = 'failed',
    failure_message = $2,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: SandboxTemplateMarkDeleted :one
UPDATE sandbox_templates
SET status = 'deleted',
    deleted_at = NOW(),
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: SandboxPublishedPortUpsert :one
INSERT INTO sandbox_published_ports (
  id, sandbox_id, target_port, protocol, visibility, public_hostname
)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (sandbox_id, target_port) DO UPDATE SET
  protocol = EXCLUDED.protocol,
  visibility = EXCLUDED.visibility,
  public_hostname = EXCLUDED.public_hostname,
  updated_at = NOW()
RETURNING *;

-- name: SandboxPublishedPortsBySandboxID :many
SELECT * FROM sandbox_published_ports
WHERE sandbox_id = $1
ORDER BY created_at ASC;

-- name: SandboxPublishedPortDeleteBySandboxAndPort :exec
DELETE FROM sandbox_published_ports
WHERE sandbox_id = $1 AND target_port = $2;

-- name: BuildLogsAfterCreatedAt :many
SELECT * FROM build_logs
WHERE build_id = $1 AND created_at > $2
ORDER BY created_at;

-- Domains --

-- name: DomainCreate :one
INSERT INTO domains (id, project_id, service_id, domain_name, verification_token)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: DomainListByProjectID :many
SELECT * FROM domains
WHERE project_id = $1
  AND domain_kind = 'production'
ORDER BY domain_name ASC;

-- name: DomainListByServiceID :many
SELECT * FROM domains
WHERE service_id = $1
  AND domain_kind = 'production'
ORDER BY domain_name ASC;

-- name: DomainFirstByIDAndProject :one
SELECT * FROM domains WHERE id = $1 AND project_id = $2;

-- name: DomainFirstByIDAndService :one
SELECT * FROM domains WHERE id = $1 AND service_id = $2;

-- name: DomainProjectIDByDomainID :one
SELECT project_id FROM domains WHERE id = $1;

-- name: DomainDelete :exec
DELETE FROM domains WHERE id = $1 AND project_id = $2;

-- name: DomainDeleteByServiceID :exec
DELETE FROM domains WHERE id = $1 AND service_id = $2;

-- name: DomainSetVerified :one
UPDATE domains
SET verified_at = NOW(), verification_token = '', updated_at = NOW()
WHERE id = $1 AND project_id = $2
RETURNING *;

-- name: DomainSetVerifiedByServiceID :one
UPDATE domains
SET verified_at = NOW(), verification_token = '', updated_at = NOW()
WHERE id = $1 AND service_id = $2
RETURNING *;

-- name: DomainVerified :one
SELECT verified_at FROM domains WHERE domain_name = $1;

-- name: DomainUpdateDeploymentForProject :exec
UPDATE domains SET deployment_id = $1, updated_at = NOW()
WHERE project_id = $2 AND domain_kind = 'production';

-- name: DomainUpdateDeploymentForService :exec
UPDATE domains
SET deployment_id = $1, updated_at = NOW()
WHERE service_id = $2
  AND domain_kind IN ('production', 'service_managed');

-- name: DomainUpdateDeploymentForDomainID :exec
UPDATE domains SET deployment_id = $2, updated_at = NOW() WHERE id = $1;

-- name: DomainManagedByServiceID :one
SELECT * FROM domains
WHERE service_id = $1
  AND domain_kind = 'service_managed'
LIMIT 1;

-- name: DomainCreateManaged :one
INSERT INTO domains (
    id, project_id, service_id, deployment_id, domain_name, verification_token, verified_at, domain_kind
)
VALUES ($1, $2, $3, $4, $5, '', NOW(), 'service_managed')
RETURNING *;

-- name: DomainUpdateManagedByServiceID :one
UPDATE domains
SET domain_name = $2,
    deployment_id = $3,
    verification_token = '',
    verified_at = NOW(),
    updated_at = NOW()
WHERE service_id = $1
  AND domain_kind = 'service_managed'
RETURNING *;

-- name: DomainDeleteManagedByServiceID :exec
DELETE FROM domains
WHERE service_id = $1
  AND domain_kind = 'service_managed';

-- name: DomainCreatePreview :one
INSERT INTO domains (id, project_id, service_id, deployment_id, domain_name, verification_token, verified_at, domain_kind, preview_environment_id)
VALUES ($1, $2, $3, $4, $5, '', NOW(), $6, $7)
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
    d.domain_kind,
    d.redirect_to,
    d.redirect_status_code,
    dep.wake_requested_at AS deployment_wake_requested_at,
    dep.deployment_kind AS deployment_kind,
    pe.closed_at AS preview_closed_at,
    (SELECT COUNT(*)::bigint FROM deployment_instances di
     INNER JOIN vms vm ON di.vm_id = vm.id AND vm.deleted_at IS NULL AND vm.status = 'running'
     WHERE di.deployment_id = d.deployment_id AND di.deleted_at IS NULL AND di.role = 'active' AND di.status = 'running') AS running_backend_count
FROM domains d
LEFT JOIN deployments dep ON d.deployment_id = dep.id AND dep.deleted_at IS NULL
LEFT JOIN preview_environments pe ON d.preview_environment_id = pe.id
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
    AND dep.stopped_at IS NULL
    AND dep.failed_at IS NULL
    AND dep.deleted_at IS NULL
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
INSERT INTO users (id, email, password_hash, display_name, is_platform_admin)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: UserByEmail :one
SELECT * FROM users WHERE LOWER(email) = LOWER($1);

-- name: UserByID :one
SELECT * FROM users WHERE id = $1;

-- name: OrganizationByID :one
SELECT * FROM organizations WHERE id = $1;

-- name: OrganizationCreate :one
WITH inserted AS (
    INSERT INTO organizations (id, name, slug)
    VALUES ($1, $2, $3)
    RETURNING *
), network AS (
    INSERT INTO org_networks (organization_id, cidr)
    SELECT id, ((SELECT COALESCE(MAX(cidr), '172.19.255.0/24'::CIDR) FROM org_networks) + 1)::CIDR
    FROM inserted
)
SELECT * FROM inserted;

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

-- name: OrganizationMembershipUpsert :exec
INSERT INTO organization_memberships (id, organization_id, user_id, role)
VALUES ($1, $2, $3, $4)
ON CONFLICT (organization_id, user_id) DO NOTHING;

-- name: UserUpdatePasswordHash :exec
UPDATE users SET password_hash = $2, updated_at = NOW() WHERE id = $1;

-- name: UserUpdateDisplayName :exec
UPDATE users SET display_name = $2, updated_at = NOW() WHERE id = $1;

-- name: UserSetPlatformAdmin :exec
UPDATE users SET is_platform_admin = $2, updated_at = NOW() WHERE id = $1;

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

-- name: UserSessionDeleteOthersByTokenHash :exec
DELETE FROM user_sessions
WHERE user_id = $1 AND token_hash != $2;

-- name: UserSessionUpdateCurrentOrg :one
UPDATE user_sessions
SET current_organization_id = $2
WHERE id = $1
RETURNING *;

-- name: UserApiKeyCreate :one
INSERT INTO user_api_keys (id, user_id, organization_id, name, token_hash)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: UserApiKeyByTokenHash :one
SELECT * FROM user_api_keys
WHERE token_hash = $1 AND revoked_at IS NULL;

-- name: UserApiKeyListByUserAndOrg :many
SELECT * FROM user_api_keys
WHERE user_id = $1 AND organization_id = $2 AND revoked_at IS NULL
ORDER BY created_at DESC;

-- name: UserApiKeyRevoke :exec
UPDATE user_api_keys
SET revoked_at = NOW()
WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL;

-- name: UserApiKeyTouchLastUsed :exec
UPDATE user_api_keys
SET last_used_at = NOW()
WHERE id = $1;

-- name: UserIdentityListByUser :many
SELECT * FROM user_identities
WHERE user_id = $1
ORDER BY provider ASC;

-- name: UserIdentityByProviderSubject :one
SELECT * FROM user_identities
WHERE provider = $1 AND provider_subject = $2;

-- name: UserIdentityByUserAndProvider :one
SELECT * FROM user_identities
WHERE user_id = $1 AND provider = $2;

-- name: UserIdentityCreate :one
INSERT INTO user_identities (
    id, user_id, provider, provider_subject, provider_login, provider_email,
    provider_display_name, claims, last_login_at
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9
)
RETURNING *;

-- name: UserIdentityUpdateByID :one
UPDATE user_identities
SET provider_subject = $2,
    provider_login = $3,
    provider_email = $4,
    provider_display_name = $5,
    claims = $6,
    last_login_at = $7,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: UserIdentityDeleteByUserAndProvider :exec
DELETE FROM user_identities
WHERE user_id = $1 AND provider = $2;

-- name: OrgProviderConnectionListByOrg :many
SELECT * FROM org_provider_connections
WHERE organization_id = $1
ORDER BY provider ASC, display_label ASC;

-- name: OrgProviderConnectionListByProvider :many
SELECT * FROM org_provider_connections
WHERE provider = $1
ORDER BY organization_id ASC, external_slug ASC;

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
    m.id AS migration_id,
    m.state AS migration_state,
    m.destination_server_id AS migration_destination_server_id,
    m.failure_message AS migration_failure_message,
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
    SELECT id, state, destination_server_id, failure_message
    FROM instance_migrations
    WHERE deployment_instance_id = di.id
    ORDER BY started_at DESC
    LIMIT 1
) m ON TRUE
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
