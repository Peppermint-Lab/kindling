-- Kindling v0.1.0 schema
-- Idempotent: safe to run repeatedly.

-- Servers: each host running the kindling binary
CREATE TABLE IF NOT EXISTS servers (
    id              UUID PRIMARY KEY,
    hostname        TEXT NOT NULL DEFAULT '',
    internal_ip     TEXT NOT NULL DEFAULT '',
    ip_range        CIDR NOT NULL,
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'draining', 'drained', 'dead')),
    last_heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Images: OCI image references (registry/repo:tag)
CREATE TABLE IF NOT EXISTS images (
    id          UUID PRIMARY KEY,
    registry    TEXT NOT NULL,
    repository  TEXT NOT NULL,
    tag         TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (registry, repository, tag)
);

-- Organizations & identity (before projects: projects.org_id FK)
CREATE TABLE IF NOT EXISTS organizations (
    id          UUID PRIMARY KEY,
    name        TEXT NOT NULL,
    slug        TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO organizations (id, name, slug, created_at, updated_at)
VALUES ('c0000000-0000-4000-a000-000000000001', 'Default', 'default', NOW(), NOW())
ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS users (
    id             UUID PRIMARY KEY,
    email          TEXT NOT NULL UNIQUE,
    password_hash  TEXT NOT NULL,
    display_name   TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS organization_memberships (
    id                UUID PRIMARY KEY,
    organization_id   UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id           UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role              TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (organization_id, user_id)
);

CREATE TABLE IF NOT EXISTS teams (
    id                UUID PRIMARY KEY,
    organization_id   UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (organization_id, name)
);

CREATE TABLE IF NOT EXISTS team_memberships (
    team_id   UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id   UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (team_id, user_id)
);

CREATE TABLE IF NOT EXISTS user_sessions (
    id                       UUID PRIMARY KEY,
    user_id                  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash               BYTEA NOT NULL,
    current_organization_id  UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    expires_at               TIMESTAMPTZ NOT NULL,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_sessions_token_hash ON user_sessions (token_hash);

CREATE TABLE IF NOT EXISTS org_provider_connections (
    id                     UUID PRIMARY KEY,
    organization_id        UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    provider               TEXT NOT NULL CHECK (provider IN ('github', 'gitlab')),
    external_slug          TEXT NOT NULL DEFAULT '',
    display_label          TEXT NOT NULL DEFAULT '',
    credentials_ciphertext BYTEA,
    metadata               JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_org_provider_connections_org ON org_provider_connections (organization_id);

-- Projects: a git-connected application
CREATE TABLE IF NOT EXISTS projects (
    id                      UUID PRIMARY KEY,
    org_id                  UUID NOT NULL REFERENCES organizations(id),
    name                    TEXT NOT NULL,
    github_repository       TEXT NOT NULL DEFAULT '',
    github_installation_id  BIGINT NOT NULL DEFAULT 0,
    github_webhook_secret   TEXT NOT NULL DEFAULT '',
    root_directory          TEXT NOT NULL DEFAULT '',
    dockerfile_path         TEXT NOT NULL DEFAULT 'Dockerfile',
    desired_instance_count  INT NOT NULL DEFAULT 1,
    last_request_at         TIMESTAMPTZ,
    scaled_to_zero          BOOLEAN NOT NULL DEFAULT false,
    scale_to_zero_enabled   BOOLEAN NOT NULL DEFAULT false,
    build_only_on_root_changes BOOLEAN NOT NULL DEFAULT false,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Migrate existing DBs created before desired_instance_count existed
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'projects' AND column_name = 'desired_instance_count'
    ) THEN
        ALTER TABLE projects ADD COLUMN desired_instance_count INT NOT NULL DEFAULT 1;
    END IF;
END $$;

-- Allow scale-to-zero (0 replicas when idle-scaled)
DO $$ BEGIN
    ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_desired_instance_count_check;
EXCEPTION WHEN undefined_object THEN NULL;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'projects_desired_instance_count_check'
    ) THEN
        ALTER TABLE projects ADD CONSTRAINT projects_desired_instance_count_check
            CHECK (desired_instance_count >= 0);
    END IF;
END $$;

-- Scale-to-zero: traffic/idle bookkeeping (idle scaler sets scaled_to_zero; edge clears on wake)
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'projects' AND column_name = 'last_request_at'
    ) THEN
        ALTER TABLE projects ADD COLUMN last_request_at TIMESTAMPTZ;
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'projects' AND column_name = 'scaled_to_zero'
    ) THEN
        ALTER TABLE projects ADD COLUMN scaled_to_zero BOOLEAN NOT NULL DEFAULT false;
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'projects' AND column_name = 'scale_to_zero_enabled'
    ) THEN
        ALTER TABLE projects ADD COLUMN scale_to_zero_enabled BOOLEAN NOT NULL DEFAULT false;
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'projects' AND column_name = 'build_only_on_root_changes'
    ) THEN
        ALTER TABLE projects ADD COLUMN build_only_on_root_changes BOOLEAN NOT NULL DEFAULT false;
    END IF;
END $$;

-- PR preview environments (one row per open/closed PR per project)
CREATE TABLE IF NOT EXISTS preview_environments (
    id                   UUID PRIMARY KEY,
    project_id           UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    provider             TEXT NOT NULL DEFAULT 'github' CHECK (provider IN ('github')),
    pr_number            INT NOT NULL,
    head_branch          TEXT NOT NULL DEFAULT '',
    head_sha             TEXT NOT NULL DEFAULT '',
    latest_deployment_id UUID,
    stable_domain_name   TEXT NOT NULL DEFAULT '',
    closed_at            TIMESTAMPTZ,
    expires_at           TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (project_id, provider, pr_number)
);

CREATE INDEX IF NOT EXISTS idx_preview_environments_project_id ON preview_environments(project_id);
CREATE INDEX IF NOT EXISTS idx_preview_environments_expires_at ON preview_environments(expires_at) WHERE expires_at IS NOT NULL;

-- Environment variables per project (values are encrypted)
CREATE TABLE IF NOT EXISTS environment_variables (
    id          UUID PRIMARY KEY,
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    value       TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (project_id, name)
);

-- Builds: a single build attempt for a commit
CREATE TABLE IF NOT EXISTS builds (
    id              UUID PRIMARY KEY,
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'building', 'successful', 'failed')),
    github_commit   TEXT NOT NULL DEFAULT '',
    github_branch   TEXT NOT NULL DEFAULT '',
    image_id        UUID REFERENCES images(id),
    vm_id           UUID,  -- build VM (forward ref, set after VM creation)
    processing_by   UUID REFERENCES servers(id),
    building_at     TIMESTAMPTZ,
    failed_at       TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- VMs: a running or pending Cloud Hypervisor microVM
CREATE TABLE IF NOT EXISTS vms (
    id              UUID PRIMARY KEY,
    server_id       UUID NOT NULL REFERENCES servers(id),
    image_id        UUID NOT NULL REFERENCES images(id),
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'starting', 'running', 'stopped', 'failed', 'suspending', 'suspended', 'warming', 'template')),
    runtime         TEXT NOT NULL DEFAULT '',
    snapshot_ref    TEXT,
    clone_source_vm_id UUID REFERENCES vms(id),
    vcpus           INT NOT NULL DEFAULT 1,
    memory          INT NOT NULL DEFAULT 512,  -- MB
    ip_address      INET NOT NULL,
    port            INT DEFAULT 3000,
    env_variables   TEXT,  -- encrypted JSON
    deleted_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Forward reference from builds to vms
DO $$ BEGIN
    ALTER TABLE builds ADD CONSTRAINT builds_vm_id_fkey FOREIGN KEY (vm_id) REFERENCES vms(id);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Deployments: a specific version of a project deployed to an environment
CREATE TABLE IF NOT EXISTS deployments (
    id                  UUID PRIMARY KEY,
    project_id          UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    build_id            UUID REFERENCES builds(id),
    image_id            UUID REFERENCES images(id),
    vm_id               UUID REFERENCES vms(id),
    github_commit       TEXT NOT NULL DEFAULT '',
    github_branch       TEXT NOT NULL DEFAULT '',
    deployment_kind     TEXT NOT NULL DEFAULT 'production'
        CHECK (deployment_kind IN ('production', 'preview')),
    preview_environment_id UUID REFERENCES preview_environments(id) ON DELETE SET NULL,
    preview_last_request_at TIMESTAMPTZ,
    preview_scaled_to_zero BOOLEAN NOT NULL DEFAULT false,
    running_at          TIMESTAMPTZ,
    stopped_at          TIMESTAMPTZ,
    failed_at           TIMESTAMPTZ,
    deleted_at          TIMESTAMPTZ,
    wake_requested_at   TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Upgrade path: DBs created before wake_requested_at existed
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'deployments' AND column_name = 'wake_requested_at'
    ) THEN
        ALTER TABLE deployments ADD COLUMN wake_requested_at TIMESTAMPTZ;
    END IF;
END $$;

-- Preview / branch metadata on deployments (production rows use defaults)
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'deployments' AND column_name = 'github_branch'
    ) THEN
        ALTER TABLE deployments ADD COLUMN github_branch TEXT NOT NULL DEFAULT '';
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'deployments' AND column_name = 'deployment_kind'
    ) THEN
        ALTER TABLE deployments ADD COLUMN deployment_kind TEXT NOT NULL DEFAULT 'production';
    END IF;
END $$;

DO $$ BEGIN
    ALTER TABLE deployments DROP CONSTRAINT IF EXISTS deployments_deployment_kind_check;
EXCEPTION WHEN undefined_object THEN NULL;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'deployments_deployment_kind_check'
    ) THEN
        ALTER TABLE deployments ADD CONSTRAINT deployments_deployment_kind_check
            CHECK (deployment_kind IN ('production', 'preview'));
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'deployments' AND column_name = 'preview_environment_id'
    ) THEN
        ALTER TABLE deployments ADD COLUMN preview_environment_id UUID REFERENCES preview_environments(id) ON DELETE SET NULL;
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'deployments' AND column_name = 'preview_last_request_at'
    ) THEN
        ALTER TABLE deployments ADD COLUMN preview_last_request_at TIMESTAMPTZ;
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'deployments' AND column_name = 'preview_scaled_to_zero'
    ) THEN
        ALTER TABLE deployments ADD COLUMN preview_scaled_to_zero BOOLEAN NOT NULL DEFAULT false;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_deployments_preview_environment_id
    ON deployments(preview_environment_id) WHERE preview_environment_id IS NOT NULL AND deleted_at IS NULL;

-- Deployment instances: one row per replica (horizontal scale) for a deployment revision
CREATE TABLE IF NOT EXISTS deployment_instances (
    id              UUID PRIMARY KEY,
    deployment_id   UUID NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    server_id       UUID REFERENCES servers(id),
    vm_id           UUID REFERENCES vms(id),
    role            TEXT NOT NULL DEFAULT 'active'
        CHECK (role IN ('active', 'warm_pool', 'template')),
    clone_source_instance_id UUID REFERENCES deployment_instances(id),
    status          TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'starting', 'running', 'failed', 'stopped')),
    deleted_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Upgrade path: older DBs may have deployment_instances without role or clone FK;
-- must run before any index or INSERT referencing those columns.
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'deployment_instances' AND column_name = 'role'
    ) THEN
        ALTER TABLE deployment_instances ADD COLUMN role TEXT NOT NULL DEFAULT 'active';
    END IF;
END $$;

DO $$ BEGIN
    ALTER TABLE deployment_instances DROP CONSTRAINT IF EXISTS deployment_instances_role_check;
EXCEPTION WHEN undefined_object THEN NULL;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'deployment_instances_role_check'
    ) THEN
        ALTER TABLE deployment_instances ADD CONSTRAINT deployment_instances_role_check
            CHECK (role IN ('active', 'warm_pool', 'template'));
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'deployment_instances' AND column_name = 'clone_source_instance_id'
    ) THEN
        ALTER TABLE deployment_instances ADD COLUMN clone_source_instance_id UUID REFERENCES deployment_instances(id);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_deployment_instances_deployment_id
    ON deployment_instances(deployment_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_deployment_instances_server_id
    ON deployment_instances(server_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_deployment_instances_role
    ON deployment_instances(deployment_id, role) WHERE deleted_at IS NULL;

-- Backfill from legacy deployments.vm_id (one VM per deployment) into deployment_instances
INSERT INTO deployment_instances (id, deployment_id, server_id, vm_id, role, status, created_at, updated_at)
SELECT gen_random_uuid(), d.id, v.server_id, d.vm_id, 'active', 'running', NOW(), NOW()
FROM deployments d
JOIN vms v ON v.id = d.vm_id AND v.deleted_at IS NULL
WHERE d.vm_id IS NOT NULL AND d.deleted_at IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM deployment_instances di
      WHERE di.deployment_id = d.id AND di.deleted_at IS NULL
  );

DO $$ BEGIN
    ALTER TABLE vms DROP CONSTRAINT IF EXISTS vms_status_check;
EXCEPTION WHEN undefined_object THEN NULL;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'vms_status_check'
    ) THEN
        ALTER TABLE vms ADD CONSTRAINT vms_status_check
            CHECK (status IN ('pending', 'starting', 'running', 'stopped', 'failed', 'suspending', 'suspended', 'warming', 'template'));
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'vms' AND column_name = 'runtime'
    ) THEN
        ALTER TABLE vms ADD COLUMN runtime TEXT NOT NULL DEFAULT '';
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'vms' AND column_name = 'snapshot_ref'
    ) THEN
        ALTER TABLE vms ADD COLUMN snapshot_ref TEXT;
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'vms' AND column_name = 'clone_source_vm_id'
    ) THEN
        ALTER TABLE vms ADD COLUMN clone_source_vm_id UUID REFERENCES vms(id);
    END IF;
END $$;

-- Domains: hostname routing
CREATE TABLE IF NOT EXISTS domains (
    id                  UUID PRIMARY KEY,
    project_id          UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    deployment_id       UUID REFERENCES deployments(id),
    domain_name         TEXT NOT NULL UNIQUE,
    verification_token  TEXT NOT NULL DEFAULT '',
    verified_at         TIMESTAMPTZ,
    redirect_to         TEXT,
    redirect_status_code INT,
    domain_kind         TEXT NOT NULL DEFAULT 'production'
        CHECK (domain_kind IN ('production', 'preview_stable', 'preview_immutable')),
    preview_environment_id UUID REFERENCES preview_environments(id) ON DELETE CASCADE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Preview vs production domain rows (custom domains are production)
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'domains' AND column_name = 'domain_kind'
    ) THEN
        ALTER TABLE domains ADD COLUMN domain_kind TEXT NOT NULL DEFAULT 'production';
    END IF;
END $$;

DO $$ BEGIN
    ALTER TABLE domains DROP CONSTRAINT IF EXISTS domains_domain_kind_check;
EXCEPTION WHEN undefined_object THEN NULL;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'domains_domain_kind_check'
    ) THEN
        ALTER TABLE domains ADD CONSTRAINT domains_domain_kind_check
            CHECK (domain_kind IN ('production', 'preview_stable', 'preview_immutable'));
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'domains' AND column_name = 'preview_environment_id'
    ) THEN
        ALTER TABLE domains ADD COLUMN preview_environment_id UUID REFERENCES preview_environments(id) ON DELETE CASCADE;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_domains_preview_environment_id ON domains(preview_environment_id) WHERE preview_environment_id IS NOT NULL;

-- Existing DBs: add verification_token for DNS domain verification
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'domains' AND column_name = 'verification_token'
    ) THEN
        ALTER TABLE domains ADD COLUMN verification_token TEXT NOT NULL DEFAULT '';
    END IF;
END $$;

-- Build logs
CREATE TABLE IF NOT EXISTS build_logs (
    id          UUID PRIMARY KEY,
    build_id    UUID NOT NULL REFERENCES builds(id) ON DELETE CASCADE,
    message     TEXT NOT NULL,
    level       TEXT NOT NULL DEFAULT 'info',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- VM logs
CREATE TABLE IF NOT EXISTS vm_logs (
    id          UUID PRIMARY KEY,
    vm_id       UUID NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
    message     TEXT NOT NULL,
    level       TEXT NOT NULL DEFAULT 'info',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- CertMagic TLS certificate storage
CREATE TABLE IF NOT EXISTS certmagic_data (
    key         TEXT PRIMARY KEY,
    value       BYTEA NOT NULL,
    modified    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Cluster-wide settings (key/value). Keys include: public_base_url
CREATE TABLE IF NOT EXISTS cluster_settings (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Per-server host/runtime settings (non-secret). Filled when a server registers.
CREATE TABLE IF NOT EXISTS server_settings (
    server_id                       UUID PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    runtime_override                TEXT NOT NULL DEFAULT '',
    advertise_host                  TEXT NOT NULL DEFAULT '',
    cloud_hypervisor_bin            TEXT NOT NULL DEFAULT '',
    cloud_hypervisor_kernel_path    TEXT NOT NULL DEFAULT '',
    cloud_hypervisor_initramfs_path TEXT NOT NULL DEFAULT '',
    updated_at                       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Cluster-wide secrets (AES-GCM ciphertext, see internal/config/crypto.go)
CREATE TABLE IF NOT EXISTS cluster_secrets (
    key         TEXT PRIMARY KEY,
    ciphertext   BYTEA NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Polled resource usage per deployment instance (control plane / workload server)
CREATE TABLE IF NOT EXISTS instance_usage_samples (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id               UUID NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    project_id              UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    deployment_id           UUID NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    deployment_instance_id  UUID NOT NULL REFERENCES deployment_instances(id) ON DELETE CASCADE,
    sampled_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    cpu_nanos_cumulative    BIGINT NOT NULL DEFAULT 0,
    cpu_percent             DOUBLE PRECISION,
    memory_rss_bytes        BIGINT NOT NULL DEFAULT 0,
    disk_read_bytes         BIGINT NOT NULL DEFAULT 0,
    disk_write_bytes        BIGINT NOT NULL DEFAULT 0,
    source                  TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_instance_usage_samples_project_time
    ON instance_usage_samples (project_id, sampled_at DESC);
CREATE INDEX IF NOT EXISTS idx_instance_usage_samples_instance_time
    ON instance_usage_samples (deployment_instance_id, sampled_at DESC);

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'instance_usage_samples' AND column_name = 'cpu_percent'
    ) THEN
        ALTER TABLE instance_usage_samples ADD COLUMN cpu_percent DOUBLE PRECISION;
    END IF;
END $$;

-- Edge proxy HTTP traffic rollups (one row per server × project × deployment × minute)
CREATE TABLE IF NOT EXISTS project_http_usage_rollups (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id       UUID NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    deployment_id UUID NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    bucket_start    TIMESTAMPTZ NOT NULL,
    request_count   BIGINT NOT NULL DEFAULT 0,
    status_2xx      BIGINT NOT NULL DEFAULT 0,
    status_4xx      BIGINT NOT NULL DEFAULT 0,
    status_5xx      BIGINT NOT NULL DEFAULT 0,
    bytes_in        BIGINT NOT NULL DEFAULT 0,
    bytes_out       BIGINT NOT NULL DEFAULT 0,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (server_id, project_id, deployment_id, bucket_start)
);

CREATE INDEX IF NOT EXISTS idx_http_rollups_project_bucket
    ON project_http_usage_rollups (project_id, bucket_start DESC);

-- Existing deployments: add projects.org_id, backfill to bootstrap org, enforce NOT NULL
DO $$
DECLARE
    bootstrap_org UUID := 'c0000000-0000-4000-a000-000000000001';
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'projects' AND column_name = 'org_id'
    ) THEN
        ALTER TABLE projects ADD COLUMN org_id UUID REFERENCES organizations(id);
    END IF;

    UPDATE projects SET org_id = bootstrap_org WHERE org_id IS NULL;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'projects' AND column_name = 'org_id'
    ) THEN
        IF NOT EXISTS (SELECT 1 FROM projects WHERE org_id IS NULL) THEN
            ALTER TABLE projects ALTER COLUMN org_id SET NOT NULL;
        END IF;
    END IF;
END $$;

-- NOTIFY for runtime config reload (LISTEN kindling_config)
CREATE OR REPLACE FUNCTION kindling_notify_config_change()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM pg_notify('kindling_config', TG_TABLE_NAME);
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS cluster_settings_config_notify ON cluster_settings;
CREATE TRIGGER cluster_settings_config_notify
    AFTER INSERT OR UPDATE OR DELETE ON cluster_settings
    FOR EACH ROW EXECUTE PROCEDURE kindling_notify_config_change();

DROP TRIGGER IF EXISTS server_settings_config_notify ON server_settings;
CREATE TRIGGER server_settings_config_notify
    AFTER INSERT OR UPDATE OR DELETE ON server_settings
    FOR EACH ROW EXECUTE PROCEDURE kindling_notify_config_change();

DROP TRIGGER IF EXISTS cluster_secrets_config_notify ON cluster_secrets;
CREATE TRIGGER cluster_secrets_config_notify
    AFTER INSERT OR UPDATE OR DELETE ON cluster_secrets
    FOR EACH ROW EXECUTE PROCEDURE kindling_notify_config_change();

-- Indexes
CREATE INDEX IF NOT EXISTS idx_vms_server_id ON vms(server_id);
CREATE INDEX IF NOT EXISTS idx_vms_status ON vms(status) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_builds_project_id ON builds(project_id);
CREATE INDEX IF NOT EXISTS idx_builds_status ON builds(status);
CREATE INDEX IF NOT EXISTS idx_deployments_project_id ON deployments(project_id);
CREATE INDEX IF NOT EXISTS idx_build_logs_build_id ON build_logs(build_id);
CREATE INDEX IF NOT EXISTS idx_vm_logs_vm_id ON vm_logs(vm_id);
CREATE INDEX IF NOT EXISTS idx_domains_project_id ON domains(project_id);
CREATE INDEX IF NOT EXISTS idx_domains_deployment_id ON domains(deployment_id);
CREATE INDEX IF NOT EXISTS idx_environment_variables_project_id ON environment_variables(project_id);
CREATE INDEX IF NOT EXISTS idx_projects_org_id ON projects(org_id);
CREATE INDEX IF NOT EXISTS idx_instance_usage_samples_server_time
    ON instance_usage_samples (server_id, sampled_at DESC);
