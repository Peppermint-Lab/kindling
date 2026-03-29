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
    email_domain TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'organizations' AND column_name = 'email_domain'
    ) THEN
        ALTER TABLE organizations ADD COLUMN email_domain TEXT NOT NULL DEFAULT '';
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_organizations_email_domain
    ON organizations(email_domain) WHERE email_domain <> '';

CREATE TABLE IF NOT EXISTS org_networks (
    organization_id UUID PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
    cidr            CIDR NOT NULL UNIQUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO organizations (id, name, slug, created_at, updated_at)
VALUES ('c0000000-0000-4000-a000-000000000001', 'Default', 'default', NOW(), NOW())
ON CONFLICT (id) DO NOTHING;

WITH missing AS (
    SELECT o.id, ROW_NUMBER() OVER (ORDER BY o.created_at, o.id) AS rn
    FROM organizations o
    WHERE NOT EXISTS (
        SELECT 1 FROM org_networks n WHERE n.organization_id = o.id
    )
), base AS (
    SELECT COALESCE(MAX(cidr), '172.19.255.0/24'::CIDR) AS max_cidr
    FROM org_networks
)
INSERT INTO org_networks (organization_id, cidr)
SELECT m.id, ((SELECT max_cidr FROM base) + m.rn)::CIDR
FROM missing m;

CREATE TABLE IF NOT EXISTS users (
    id             UUID PRIMARY KEY,
    email          TEXT NOT NULL UNIQUE,
    password_hash  TEXT NOT NULL,
    display_name   TEXT NOT NULL DEFAULT '',
    is_platform_admin BOOLEAN NOT NULL DEFAULT false,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'users' AND column_name = 'is_platform_admin'
    ) THEN
        ALTER TABLE users ADD COLUMN is_platform_admin BOOLEAN NOT NULL DEFAULT false;
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS auth_providers (
    provider                 TEXT PRIMARY KEY CHECK (provider IN ('github', 'oidc')),
    display_name             TEXT NOT NULL DEFAULT '',
    enabled                  BOOLEAN NOT NULL DEFAULT false,
    client_id                TEXT NOT NULL DEFAULT '',
    client_secret_ciphertext BYTEA,
    issuer_url               TEXT NOT NULL DEFAULT '',
    auth_url                 TEXT NOT NULL DEFAULT '',
    token_url                TEXT NOT NULL DEFAULT '',
    userinfo_url             TEXT NOT NULL DEFAULT '',
    scopes                   TEXT NOT NULL DEFAULT '',
    metadata                 JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO auth_providers (
    provider, display_name, enabled, client_id, client_secret_ciphertext,
    issuer_url, auth_url, token_url, userinfo_url, scopes, metadata, created_at, updated_at
)
VALUES
    ('github', 'GitHub', false, '', NULL, '', '', '', '', 'read:user user:email read:org', '{}'::jsonb, NOW(), NOW()),
    ('oidc', 'OpenID Connect', false, '', NULL, '', '', '', '', 'openid profile email', '{}'::jsonb, NOW(), NOW())
ON CONFLICT (provider) DO NOTHING;

CREATE TABLE IF NOT EXISTS user_identities (
    id                    UUID PRIMARY KEY,
    user_id               UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider              TEXT NOT NULL REFERENCES auth_providers(provider) ON DELETE CASCADE,
    provider_subject      TEXT NOT NULL,
    provider_login        TEXT NOT NULL DEFAULT '',
    provider_email        TEXT NOT NULL DEFAULT '',
    provider_display_name TEXT NOT NULL DEFAULT '',
    claims                JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_login_at         TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (provider, provider_subject),
    UNIQUE (user_id, provider)
);

CREATE INDEX IF NOT EXISTS idx_user_identities_user_id ON user_identities (user_id);

CREATE TABLE IF NOT EXISTS organization_memberships (
    id                UUID PRIMARY KEY,
    organization_id   UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id           UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role              TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
    status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'pending', 'rejected')),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (organization_id, user_id)
);

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'organization_memberships' AND column_name = 'status'
    ) THEN
        ALTER TABLE organization_memberships ADD COLUMN status TEXT NOT NULL DEFAULT 'active';
    END IF;
END $$;

DO $$ BEGIN
    ALTER TABLE organization_memberships DROP CONSTRAINT IF EXISTS organization_memberships_status_check;
EXCEPTION WHEN undefined_object THEN NULL;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'organization_memberships_status_check'
    ) THEN
        ALTER TABLE organization_memberships ADD CONSTRAINT organization_memberships_status_check
            CHECK (status IN ('active', 'pending', 'rejected'));
    END IF;
END $$;

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

-- API keys for machine and CLI access (Authorization: Bearer knd_...)
CREATE TABLE IF NOT EXISTS user_api_keys (
    id                UUID PRIMARY KEY,
    user_id           UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    organization_id   UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name              TEXT NOT NULL DEFAULT '',
    token_hash        BYTEA NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at      TIMESTAMPTZ,
    revoked_at        TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_api_keys_token_hash_active
ON user_api_keys (token_hash)
WHERE revoked_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_user_api_keys_user_org ON user_api_keys (user_id, organization_id);

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
    min_instance_count      INT NOT NULL DEFAULT 0,
    max_instance_count      INT NOT NULL DEFAULT 3,
    last_request_at         TIMESTAMPTZ,
    scaled_to_zero          BOOLEAN NOT NULL DEFAULT false,
    scale_to_zero_enabled   BOOLEAN NOT NULL DEFAULT false,
    build_only_on_root_changes BOOLEAN NOT NULL DEFAULT false,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Services: deployable/networked workloads within a project.
CREATE TABLE IF NOT EXISTS services (
    id                      UUID PRIMARY KEY,
    project_id              UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name                    TEXT NOT NULL,
    slug                    TEXT NOT NULL,
    root_directory          TEXT NOT NULL DEFAULT '/',
    dockerfile_path         TEXT NOT NULL DEFAULT 'Dockerfile',
    desired_instance_count  INT NOT NULL DEFAULT 1,
    build_only_on_root_changes BOOLEAN NOT NULL DEFAULT false,
    public_default          BOOLEAN NOT NULL DEFAULT false,
    is_primary              BOOLEAN NOT NULL DEFAULT false,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (project_id, slug)
);

CREATE INDEX IF NOT EXISTS idx_services_project_id ON services(project_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_services_primary_per_project ON services(project_id) WHERE is_primary = true;

CREATE TABLE IF NOT EXISTS service_endpoints (
    id               UUID PRIMARY KEY,
    service_id       UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    protocol         TEXT NOT NULL DEFAULT 'http'
        CHECK (protocol IN ('http', 'tcp')),
    target_port      INT NOT NULL DEFAULT 3000 CHECK (target_port > 0 AND target_port <= 65535),
    visibility       TEXT NOT NULL DEFAULT 'private'
        CHECK (visibility IN ('private', 'public')),
    private_ip       INET NOT NULL UNIQUE,
    public_hostname  TEXT NOT NULL DEFAULT '',
    last_healthy_at  TIMESTAMPTZ,
    last_unhealthy_at TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (service_id, name)
);

CREATE INDEX IF NOT EXISTS idx_service_endpoints_service_id ON service_endpoints(service_id, created_at ASC);

-- Backfill one primary service per existing project.
INSERT INTO services (
    id, project_id, name, slug, root_directory, dockerfile_path,
    desired_instance_count, build_only_on_root_changes, public_default, is_primary
)
SELECT
    gen_random_uuid(),
    p.id,
    p.name,
    'app',
    p.root_directory,
    p.dockerfile_path,
    p.desired_instance_count,
    p.build_only_on_root_changes,
    false,
    true
FROM projects p
WHERE NOT EXISTS (
    SELECT 1 FROM services s WHERE s.project_id = p.id
);

WITH ranked_missing AS (
    SELECT
        s.id AS service_id,
        p.org_id,
        ROW_NUMBER() OVER (PARTITION BY p.org_id ORDER BY s.created_at, s.id) AS rn,
        CASE WHEN s.public_default THEN 'public' ELSE 'private' END AS visibility
    FROM services s
    JOIN projects p ON p.id = s.project_id
    WHERE NOT EXISTS (
        SELECT 1 FROM service_endpoints se WHERE se.service_id = s.id
    )
), existing_max AS (
    SELECT
        n.organization_id,
        COALESCE(MAX(se.private_ip), (host(network(n.cidr))::INET + 9)::INET) AS max_private_ip
    FROM org_networks n
    LEFT JOIN projects p ON p.org_id = n.organization_id
    LEFT JOIN services s ON s.project_id = p.id
    LEFT JOIN service_endpoints se ON se.service_id = s.id
    GROUP BY n.organization_id, n.cidr
)
INSERT INTO service_endpoints (
    id, service_id, name, protocol, target_port, visibility, private_ip, public_hostname
)
SELECT
    gen_random_uuid(),
    rm.service_id,
    'web',
    'http',
    3000,
    rm.visibility,
    (em.max_private_ip + rm.rn)::INET,
    ''
FROM ranked_missing rm
JOIN existing_max em ON em.organization_id = rm.org_id;

-- Migrate existing DBs created before desired_instance_count existed
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'projects' AND column_name = 'desired_instance_count'
    ) THEN
        ALTER TABLE projects ADD COLUMN desired_instance_count INT NOT NULL DEFAULT 1;
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'projects' AND column_name = 'min_instance_count'
    ) THEN
        ALTER TABLE projects ADD COLUMN min_instance_count INT;
    END IF;
END $$;

UPDATE projects
SET min_instance_count = desired_instance_count
WHERE min_instance_count IS NULL;

DO $$ BEGIN
    ALTER TABLE projects ALTER COLUMN min_instance_count SET DEFAULT 0;
    ALTER TABLE projects ALTER COLUMN min_instance_count SET NOT NULL;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'projects' AND column_name = 'max_instance_count'
    ) THEN
        ALTER TABLE projects ADD COLUMN max_instance_count INT;
    END IF;
END $$;

UPDATE projects
SET max_instance_count = GREATEST(desired_instance_count, 3)
WHERE max_instance_count IS NULL;

DO $$ BEGIN
    ALTER TABLE projects ALTER COLUMN max_instance_count SET DEFAULT 3;
    ALTER TABLE projects ALTER COLUMN max_instance_count SET NOT NULL;
END $$;

-- Allow scale-to-zero (0 replicas when idle-scaled)
DO $$ BEGIN
    ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_desired_instance_count_check;
    ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_min_instance_count_check;
    ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_max_instance_count_check;
    ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_instance_count_bounds_check;
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

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'projects_min_instance_count_check'
    ) THEN
        ALTER TABLE projects ADD CONSTRAINT projects_min_instance_count_check
            CHECK (min_instance_count >= 0);
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'projects_max_instance_count_check'
    ) THEN
        ALTER TABLE projects ADD CONSTRAINT projects_max_instance_count_check
            CHECK (max_instance_count >= 0);
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'projects_instance_count_bounds_check'
    ) THEN
        ALTER TABLE projects ADD CONSTRAINT projects_instance_count_bounds_check
            CHECK (min_instance_count <= max_instance_count);
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
    service_id           UUID REFERENCES services(id) ON DELETE CASCADE,
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

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'preview_environments' AND column_name = 'service_id'
    ) THEN
        ALTER TABLE preview_environments ADD COLUMN service_id UUID REFERENCES services(id) ON DELETE CASCADE;
    END IF;
END $$;

UPDATE preview_environments pe
SET service_id = s.id
FROM services s
WHERE pe.service_id IS NULL
  AND s.project_id = pe.project_id
  AND s.is_primary = true;

-- Org-scoped sandbox environments (separate from deployments/previews).
CREATE TABLE IF NOT EXISTS sandboxes (
    id                   UUID PRIMARY KEY,
    org_id               UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name                 TEXT NOT NULL,
    host_group           TEXT NOT NULL DEFAULT 'linux-sandbox',
    backend              TEXT NOT NULL DEFAULT ''
        CHECK (backend IN ('', 'cloud-hypervisor', 'apple-vz')),
    arch                 TEXT NOT NULL DEFAULT ''
        CHECK (arch IN ('', 'amd64', 'arm64')),
    desired_state        TEXT NOT NULL DEFAULT 'running'
        CHECK (desired_state IN ('running', 'stopped', 'deleted')),
    observed_state       TEXT NOT NULL DEFAULT 'pending'
        CHECK (observed_state IN ('pending', 'running', 'stopped', 'deleting', 'deleted', 'failed')),
    server_id            UUID REFERENCES servers(id) ON DELETE SET NULL,
    vm_id                UUID,
    template_id          UUID,
    base_image_ref       TEXT NOT NULL DEFAULT '',
    vcpu                 INT NOT NULL DEFAULT 2,
    memory_mb            INT NOT NULL DEFAULT 2048,
    disk_gb              INT NOT NULL DEFAULT 10,
    env_json             JSONB NOT NULL DEFAULT '{}'::jsonb,
    git_repo             TEXT NOT NULL DEFAULT '',
    git_ref              TEXT NOT NULL DEFAULT '',
    auto_suspend_seconds BIGINT NOT NULL DEFAULT 900,
    last_used_at         TIMESTAMPTZ,
    expires_at           TIMESTAMPTZ,
    published_http_port  INT CHECK (published_http_port IS NULL OR (published_http_port > 0 AND published_http_port <= 65535)),
    runtime_url          TEXT NOT NULL DEFAULT '',
    ssh_host_public_key  TEXT NOT NULL DEFAULT '',
    failure_message      TEXT NOT NULL DEFAULT '',
    created_by_user_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    deleted_at           TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_sandboxes_org_id ON sandboxes(org_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_sandboxes_server_id ON sandboxes(server_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_sandboxes_expires_at ON sandboxes(expires_at) WHERE expires_at IS NOT NULL AND deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS sandbox_templates (
    id                 UUID PRIMARY KEY,
    org_id             UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name               TEXT NOT NULL,
    host_group         TEXT NOT NULL DEFAULT 'linux-sandbox',
    backend            TEXT NOT NULL DEFAULT ''
        CHECK (backend IN ('', 'cloud-hypervisor', 'apple-vz')),
    arch               TEXT NOT NULL DEFAULT ''
        CHECK (arch IN ('', 'amd64', 'arm64')),
    source_sandbox_id  UUID REFERENCES sandboxes(id) ON DELETE SET NULL,
    server_id          UUID REFERENCES servers(id) ON DELETE SET NULL,
    base_image_ref     TEXT NOT NULL DEFAULT '',
    snapshot_ref       TEXT NOT NULL DEFAULT '',
    vcpu               INT NOT NULL DEFAULT 2,
    memory_mb          INT NOT NULL DEFAULT 2048,
    disk_gb            INT NOT NULL DEFAULT 10,
    status             TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'ready', 'failed', 'deleted')),
    failure_message    TEXT NOT NULL DEFAULT '',
    created_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    deleted_at         TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_sandbox_templates_org_id ON sandbox_templates(org_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_sandbox_templates_server_id ON sandbox_templates(server_id) WHERE deleted_at IS NULL;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'sandboxes_template_id_fkey'
    ) THEN
        ALTER TABLE sandboxes ADD CONSTRAINT sandboxes_template_id_fkey
            FOREIGN KEY (template_id) REFERENCES sandbox_templates(id) ON DELETE SET NULL;
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS sandbox_published_ports (
    id              UUID PRIMARY KEY,
    sandbox_id      UUID NOT NULL REFERENCES sandboxes(id) ON DELETE CASCADE,
    target_port     INT NOT NULL CHECK (target_port > 0 AND target_port <= 65535),
    protocol        TEXT NOT NULL DEFAULT 'http' CHECK (protocol IN ('http')),
    visibility      TEXT NOT NULL DEFAULT 'public' CHECK (visibility IN ('public')),
    public_hostname TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (sandbox_id, target_port)
);

CREATE INDEX IF NOT EXISTS idx_sandbox_published_ports_sandbox_id ON sandbox_published_ports(sandbox_id, created_at ASC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_sandbox_published_ports_public_hostname_unique
    ON sandbox_published_ports(LOWER(public_hostname))
    WHERE BTRIM(public_hostname) <> '';

CREATE TABLE IF NOT EXISTS user_ssh_keys (
    id          UUID PRIMARY KEY,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT NOT NULL DEFAULT '',
    public_key  TEXT NOT NULL,
    deleted_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_user_ssh_keys_user_id
    ON user_ssh_keys(user_id, created_at DESC)
    WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_ssh_keys_user_public_key_active
    ON user_ssh_keys(user_id, public_key)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS sandbox_access_events (
    id             UUID PRIMARY KEY,
    sandbox_id     UUID NOT NULL REFERENCES sandboxes(id) ON DELETE CASCADE,
    user_id        UUID REFERENCES users(id) ON DELETE SET NULL,
    access_method  TEXT NOT NULL CHECK (access_method IN ('shell_ws', 'ssh', 'exec', 'copy_in', 'copy_out')),
    event_type     TEXT NOT NULL CHECK (event_type IN ('started', 'ended', 'failed')),
    exit_code      INT,
    error_summary  TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_sandbox_access_events_sandbox_id
    ON sandbox_access_events(sandbox_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_sandbox_access_events_user_id
    ON sandbox_access_events(user_id, created_at DESC);

-- Environment variables per project (values stored as encrypted envelopes)
CREATE TABLE IF NOT EXISTS environment_variables (
    id          UUID PRIMARY KEY,
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    service_id  UUID REFERENCES services(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    value       TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'environment_variables' AND column_name = 'service_id'
    ) THEN
        ALTER TABLE environment_variables ADD COLUMN service_id UUID REFERENCES services(id) ON DELETE CASCADE;
    END IF;
END $$;

UPDATE environment_variables ev
SET service_id = s.id
FROM services s
WHERE ev.service_id IS NULL
  AND s.project_id = ev.project_id
  AND s.is_primary = true;

UPDATE environment_variables ev
SET service_id = NULL
FROM services s
WHERE ev.service_id = s.id
  AND s.project_id = ev.project_id
  AND s.is_primary = true;

DO $$ BEGIN
    ALTER TABLE environment_variables DROP CONSTRAINT IF EXISTS environment_variables_project_id_name_key;
EXCEPTION WHEN undefined_object THEN NULL;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS idx_environment_variables_project_default_name
    ON environment_variables(project_id, name)
    WHERE service_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_environment_variables_service_name
    ON environment_variables(service_id, name)
    WHERE service_id IS NOT NULL;

-- Builds: a single build attempt for a commit
CREATE TABLE IF NOT EXISTS builds (
    id              UUID PRIMARY KEY,
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    service_id      UUID REFERENCES services(id) ON DELETE CASCADE,
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

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'builds' AND column_name = 'service_id'
    ) THEN
        ALTER TABLE builds ADD COLUMN service_id UUID REFERENCES services(id) ON DELETE CASCADE;
    END IF;
END $$;

UPDATE builds b
SET service_id = s.id
FROM services s
WHERE b.service_id IS NULL
  AND s.project_id = b.project_id
  AND s.is_primary = true;

-- VMs: a running or pending Cloud Hypervisor microVM
CREATE TABLE IF NOT EXISTS vms (
    id              UUID PRIMARY KEY,
    server_id       UUID NOT NULL REFERENCES servers(id),
    image_id        UUID NOT NULL REFERENCES images(id),
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'starting', 'running', 'stopped', 'failed', 'suspending', 'suspended', 'warming', 'template')),
    runtime         TEXT NOT NULL DEFAULT '',
    snapshot_ref    TEXT,
    shared_rootfs_ref TEXT NOT NULL DEFAULT '',
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

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'sandboxes_vm_id_fkey'
    ) THEN
        ALTER TABLE sandboxes ADD CONSTRAINT sandboxes_vm_id_fkey
            FOREIGN KEY (vm_id) REFERENCES vms(id) ON DELETE SET NULL;
    END IF;
END $$;

-- Forward reference from builds to vms
DO $$ BEGIN
    ALTER TABLE builds ADD CONSTRAINT builds_vm_id_fkey FOREIGN KEY (vm_id) REFERENCES vms(id);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Deployments: a specific version of a project deployed to an environment
CREATE TABLE IF NOT EXISTS deployments (
    id                  UUID PRIMARY KEY,
    project_id          UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    service_id          UUID REFERENCES services(id) ON DELETE CASCADE,
    build_id            UUID REFERENCES builds(id),
    image_id            UUID REFERENCES images(id),
    vm_id               UUID REFERENCES vms(id),
    promoted_from_deployment_id UUID REFERENCES deployments(id) ON DELETE SET NULL,
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
        WHERE table_schema = 'public' AND table_name = 'deployments' AND column_name = 'service_id'
    ) THEN
        ALTER TABLE deployments ADD COLUMN service_id UUID REFERENCES services(id) ON DELETE CASCADE;
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'deployments' AND column_name = 'promoted_from_deployment_id'
    ) THEN
        ALTER TABLE deployments ADD COLUMN promoted_from_deployment_id UUID REFERENCES deployments(id) ON DELETE SET NULL;
    END IF;
END $$;

UPDATE deployments d
SET service_id = s.id
FROM services s
WHERE d.service_id IS NULL
  AND s.project_id = d.project_id
  AND s.is_primary = true;

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

-- Project volumes: single persistent block device per project.
CREATE TABLE IF NOT EXISTS project_volumes (
    id              UUID PRIMARY KEY,
    project_id      UUID NOT NULL UNIQUE REFERENCES projects(id) ON DELETE CASCADE,
    service_id      UUID REFERENCES services(id) ON DELETE CASCADE,
    server_id       UUID REFERENCES servers(id) ON DELETE SET NULL,
    attached_vm_id  UUID REFERENCES vms(id) ON DELETE SET NULL,
    mount_path      TEXT NOT NULL DEFAULT '/data',
    size_gb         INT NOT NULL DEFAULT 10 CHECK (size_gb > 0),
    filesystem      TEXT NOT NULL DEFAULT 'ext4'
        CHECK (filesystem IN ('ext4')),
    status          TEXT NOT NULL DEFAULT 'detached'
        CHECK (status IN ('detached', 'available', 'attached', 'unavailable', 'backing_up', 'restoring', 'repairing', 'deleting')),
    health          TEXT NOT NULL DEFAULT 'unknown'
        CHECK (health IN ('unknown', 'healthy', 'degraded', 'missing', 'corrupt')),
    backup_schedule TEXT NOT NULL DEFAULT 'manual'
        CHECK (backup_schedule IN ('off', 'manual', 'daily', 'weekly')),
    backup_retention_count INT NOT NULL DEFAULT 7 CHECK (backup_retention_count > 0),
    pre_delete_backup_enabled BOOLEAN NOT NULL DEFAULT false,
    last_error      TEXT NOT NULL DEFAULT '',
    deleted_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'project_volumes' AND column_name = 'service_id'
    ) THEN
        ALTER TABLE project_volumes ADD COLUMN service_id UUID REFERENCES services(id) ON DELETE CASCADE;
    END IF;
END $$;

UPDATE project_volumes pv
SET service_id = s.id
FROM services s
WHERE pv.service_id IS NULL
  AND s.project_id = pv.project_id
  AND s.is_primary = true;

CREATE UNIQUE INDEX IF NOT EXISTS idx_project_volumes_service_id
    ON project_volumes(service_id) WHERE deleted_at IS NULL AND service_id IS NOT NULL;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'project_volumes' AND column_name = 'health'
    ) THEN
        ALTER TABLE project_volumes ADD COLUMN health TEXT NOT NULL DEFAULT 'unknown';
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'project_volumes' AND column_name = 'backup_schedule'
    ) THEN
        ALTER TABLE project_volumes ADD COLUMN backup_schedule TEXT NOT NULL DEFAULT 'manual';
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'project_volumes' AND column_name = 'backup_retention_count'
    ) THEN
        ALTER TABLE project_volumes ADD COLUMN backup_retention_count INT NOT NULL DEFAULT 7;
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'project_volumes' AND column_name = 'pre_delete_backup_enabled'
    ) THEN
        ALTER TABLE project_volumes ADD COLUMN pre_delete_backup_enabled BOOLEAN NOT NULL DEFAULT false;
    END IF;
END $$;

DO $$ BEGIN
    ALTER TABLE project_volumes DROP CONSTRAINT IF EXISTS project_volumes_status_check;
EXCEPTION WHEN undefined_object THEN NULL;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'project_volumes_status_check'
    ) THEN
        ALTER TABLE project_volumes ADD CONSTRAINT project_volumes_status_check
            CHECK (status IN ('detached', 'available', 'attached', 'unavailable', 'backing_up', 'restoring', 'repairing', 'deleting'));
    END IF;
END $$;

DO $$ BEGIN
    ALTER TABLE project_volumes DROP CONSTRAINT IF EXISTS project_volumes_health_check;
EXCEPTION WHEN undefined_object THEN NULL;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'project_volumes_health_check'
    ) THEN
        ALTER TABLE project_volumes ADD CONSTRAINT project_volumes_health_check
            CHECK (health IN ('unknown', 'healthy', 'degraded', 'missing', 'corrupt'));
    END IF;
END $$;

DO $$ BEGIN
    ALTER TABLE project_volumes DROP CONSTRAINT IF EXISTS project_volumes_backup_schedule_check;
EXCEPTION WHEN undefined_object THEN NULL;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'project_volumes_backup_schedule_check'
    ) THEN
        ALTER TABLE project_volumes ADD CONSTRAINT project_volumes_backup_schedule_check
            CHECK (backup_schedule IN ('off', 'manual', 'daily', 'weekly'));
    END IF;
END $$;

DO $$ BEGIN
    ALTER TABLE project_volumes DROP CONSTRAINT IF EXISTS project_volumes_backup_retention_count_check;
EXCEPTION WHEN undefined_object THEN NULL;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'project_volumes_backup_retention_count_check'
    ) THEN
        ALTER TABLE project_volumes ADD CONSTRAINT project_volumes_backup_retention_count_check
            CHECK (backup_retention_count > 0);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_project_volumes_server_id
    ON project_volumes(server_id) WHERE deleted_at IS NULL AND server_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_project_volumes_attached_vm_id
    ON project_volumes(attached_vm_id) WHERE deleted_at IS NULL AND attached_vm_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS project_volume_operations (
    id                UUID PRIMARY KEY,
    project_volume_id UUID NOT NULL REFERENCES project_volumes(id) ON DELETE CASCADE,
    server_id         UUID REFERENCES servers(id) ON DELETE SET NULL,
    kind              TEXT NOT NULL
        CHECK (kind IN ('backup', 'restore', 'move', 'repair')),
    status            TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'succeeded', 'failed')),
    request_metadata  JSONB NOT NULL DEFAULT '{}'::jsonb,
    result_metadata   JSONB NOT NULL DEFAULT '{}'::jsonb,
    error             TEXT NOT NULL DEFAULT '',
    started_at        TIMESTAMPTZ,
    completed_at      TIMESTAMPTZ,
    failed_at         TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_project_volume_operations_volume_id
    ON project_volume_operations(project_volume_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_project_volume_operations_server_id
    ON project_volume_operations(server_id, created_at DESC) WHERE server_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_project_volume_operations_status
    ON project_volume_operations(status, created_at DESC);

CREATE TABLE IF NOT EXISTS project_volume_backups (
    id                UUID PRIMARY KEY,
    project_volume_id UUID NOT NULL REFERENCES project_volumes(id) ON DELETE CASCADE,
    kind              TEXT NOT NULL
        CHECK (kind IN ('manual', 'scheduled', 'pre_delete')),
    storage_url       TEXT NOT NULL DEFAULT '',
    storage_key       TEXT NOT NULL DEFAULT '',
    size_bytes        BIGINT NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    status            TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'succeeded', 'failed')),
    error             TEXT NOT NULL DEFAULT '',
    metadata          JSONB NOT NULL DEFAULT '{}'::jsonb,
    started_at        TIMESTAMPTZ,
    completed_at      TIMESTAMPTZ,
    failed_at         TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_project_volume_backups_volume_id
    ON project_volume_backups(project_volume_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_project_volume_backups_status
    ON project_volume_backups(status, created_at DESC);

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
        WHERE table_schema = 'public' AND table_name = 'vms' AND column_name = 'shared_rootfs_ref'
    ) THEN
        ALTER TABLE vms ADD COLUMN shared_rootfs_ref TEXT NOT NULL DEFAULT '';
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
    service_id          UUID REFERENCES services(id) ON DELETE CASCADE,
    deployment_id       UUID REFERENCES deployments(id),
    domain_name         TEXT NOT NULL UNIQUE,
    verification_token  TEXT NOT NULL DEFAULT '',
    verified_at         TIMESTAMPTZ,
    redirect_to         TEXT,
    redirect_status_code INT,
    domain_kind         TEXT NOT NULL DEFAULT 'production'
        CHECK (domain_kind IN ('production', 'service_managed', 'preview_stable', 'preview_immutable')),
    preview_environment_id UUID REFERENCES preview_environments(id) ON DELETE CASCADE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'domains' AND column_name = 'service_id'
    ) THEN
        ALTER TABLE domains ADD COLUMN service_id UUID REFERENCES services(id) ON DELETE CASCADE;
    END IF;
END $$;

UPDATE domains d
SET service_id = s.id
FROM services s
WHERE d.service_id IS NULL
  AND s.project_id = d.project_id
  AND s.is_primary = true;

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
            CHECK (domain_kind IN ('production', 'service_managed', 'preview_stable', 'preview_immutable'));
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

-- CI jobs
CREATE TABLE IF NOT EXISTS ci_jobs (
    id                 UUID PRIMARY KEY,
    project_id         UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    status             TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'successful', 'failed', 'canceled')),
    source             TEXT NOT NULL DEFAULT 'local_workflow_run'
        CHECK (source IN ('local_workflow_run', 'github_actions_runner')),
    workflow_name      TEXT NOT NULL DEFAULT '',
    workflow_file      TEXT NOT NULL DEFAULT '',
    selected_job_id    TEXT NOT NULL DEFAULT '',
    event_name         TEXT NOT NULL DEFAULT '',
    input_values       JSONB NOT NULL DEFAULT '{}'::jsonb,
    input_archive_path TEXT NOT NULL DEFAULT '',
    provider_connection_id UUID REFERENCES org_provider_connections(id) ON DELETE SET NULL,
    external_repo      TEXT NOT NULL DEFAULT '',
    external_installation_id BIGINT NOT NULL DEFAULT 0,
    external_workflow_job_id BIGINT NOT NULL DEFAULT 0,
    external_workflow_run_id BIGINT NOT NULL DEFAULT 0,
    external_run_attempt INT NOT NULL DEFAULT 0,
    external_html_url  TEXT NOT NULL DEFAULT '',
    runner_labels      JSONB NOT NULL DEFAULT '[]'::jsonb,
    runner_name        TEXT NOT NULL DEFAULT '',
    require_microvm    BOOLEAN NOT NULL DEFAULT true,
    execution_backend  TEXT NOT NULL DEFAULT '',
    workspace_dir      TEXT NOT NULL DEFAULT '',
    processing_by      UUID REFERENCES servers(id),
    exit_code          INT,
    error_message      TEXT NOT NULL DEFAULT '',
    started_at         TIMESTAMPTZ,
    finished_at        TIMESTAMPTZ,
    canceled_at        TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE ci_jobs
    ADD COLUMN IF NOT EXISTS require_microvm BOOLEAN NOT NULL DEFAULT true;

ALTER TABLE ci_jobs
    ADD COLUMN IF NOT EXISTS execution_backend TEXT NOT NULL DEFAULT '';

ALTER TABLE ci_jobs
    ADD COLUMN IF NOT EXISTS provider_connection_id UUID REFERENCES org_provider_connections(id) ON DELETE SET NULL;

ALTER TABLE ci_jobs
    ADD COLUMN IF NOT EXISTS external_repo TEXT NOT NULL DEFAULT '';

ALTER TABLE ci_jobs
    ADD COLUMN IF NOT EXISTS external_installation_id BIGINT NOT NULL DEFAULT 0;

ALTER TABLE ci_jobs
    ADD COLUMN IF NOT EXISTS external_workflow_job_id BIGINT NOT NULL DEFAULT 0;

ALTER TABLE ci_jobs
    ADD COLUMN IF NOT EXISTS external_workflow_run_id BIGINT NOT NULL DEFAULT 0;

ALTER TABLE ci_jobs
    ADD COLUMN IF NOT EXISTS external_run_attempt INT NOT NULL DEFAULT 0;

ALTER TABLE ci_jobs
    ADD COLUMN IF NOT EXISTS external_html_url TEXT NOT NULL DEFAULT '';

ALTER TABLE ci_jobs
    ADD COLUMN IF NOT EXISTS runner_labels JSONB NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE ci_jobs
    ADD COLUMN IF NOT EXISTS runner_name TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS ci_job_logs (
    id          UUID PRIMARY KEY,
    ci_job_id   UUID NOT NULL REFERENCES ci_jobs(id) ON DELETE CASCADE,
    message     TEXT NOT NULL,
    level       TEXT NOT NULL DEFAULT 'info',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS ci_job_artifacts (
    id          UUID PRIMARY KEY,
    ci_job_id   UUID NOT NULL REFERENCES ci_jobs(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    path        TEXT NOT NULL DEFAULT '',
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
    internal_api_port               INT NOT NULL DEFAULT 0
        CHECK (internal_api_port >= 0 AND internal_api_port <= 65535),
    cloud_hypervisor_bin            TEXT NOT NULL DEFAULT '',
    cloud_hypervisor_kernel_path    TEXT NOT NULL DEFAULT '',
    cloud_hypervisor_initramfs_path TEXT NOT NULL DEFAULT '',
    cloud_hypervisor_state_dir      TEXT NOT NULL DEFAULT '',
    updated_at                       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE server_settings
    ADD COLUMN IF NOT EXISTS cloud_hypervisor_state_dir TEXT NOT NULL DEFAULT '';

ALTER TABLE server_settings
    ADD COLUMN IF NOT EXISTS internal_api_port INT NOT NULL DEFAULT 0;

-- Latest control-plane component snapshots per server.
CREATE TABLE IF NOT EXISTS server_component_statuses (
    server_id            UUID NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    component            TEXT NOT NULL CHECK (component IN ('api', 'edge', 'worker', 'usage_poller')),
    status               TEXT NOT NULL DEFAULT 'healthy' CHECK (status IN ('healthy', 'degraded')),
    observed_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_success_at      TIMESTAMPTZ,
    last_error_at        TIMESTAMPTZ,
    last_error_message   TEXT NOT NULL DEFAULT '',
    metadata             JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (server_id, component)
);

-- Cluster-wide secrets (AES-GCM ciphertext, see internal/config/crypto.go)
CREATE TABLE IF NOT EXISTS cluster_secrets (
    key         TEXT PRIMARY KEY,
    ciphertext   BYTEA NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Instance live migrations (Cloud Hypervisor Phase 2).
CREATE TABLE IF NOT EXISTS instance_migrations (
    id                      UUID PRIMARY KEY,
    deployment_instance_id  UUID NOT NULL REFERENCES deployment_instances(id) ON DELETE CASCADE,
    source_server_id        UUID NOT NULL REFERENCES servers(id),
    destination_server_id   UUID NOT NULL REFERENCES servers(id),
    source_vm_id            UUID NOT NULL REFERENCES vms(id),
    state                   TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'destination_prepared', 'sending', 'received', 'completed', 'failed', 'aborted', 'fallback_evacuating')),
    mode                    TEXT NOT NULL DEFAULT 'stop_and_copy'
        CHECK (mode IN ('stop_and_copy')),
    receive_addr            TEXT NOT NULL DEFAULT '',
    receive_token_hash      BYTEA,
    destination_runtime_url TEXT NOT NULL DEFAULT '',
    failure_code            TEXT NOT NULL DEFAULT '',
    failure_message         TEXT NOT NULL DEFAULT '',
    cutover_deadline_at     TIMESTAMPTZ,
    started_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at            TIMESTAMPTZ,
    failed_at               TIMESTAMPTZ,
    aborted_at              TIMESTAMPTZ,
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_instance_migrations_instance_id
    ON instance_migrations(deployment_instance_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_instance_migrations_source_server
    ON instance_migrations(source_server_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_instance_migrations_destination_server
    ON instance_migrations(destination_server_id, started_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_instance_migrations_one_active_per_instance
    ON instance_migrations(deployment_instance_id)
    WHERE state NOT IN ('completed', 'failed', 'aborted', 'fallback_evacuating');

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
CREATE INDEX IF NOT EXISTS idx_server_component_statuses_server
    ON server_component_statuses (server_id, component);

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
CREATE INDEX IF NOT EXISTS idx_ci_jobs_project_id ON ci_jobs(project_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ci_jobs_status ON ci_jobs(status);
CREATE INDEX IF NOT EXISTS idx_ci_jobs_external_workflow_job_id ON ci_jobs(external_workflow_job_id) WHERE external_workflow_job_id <> 0;
CREATE UNIQUE INDEX IF NOT EXISTS idx_ci_jobs_github_external_job_unique
    ON ci_jobs(source, external_workflow_job_id)
    WHERE source = 'github_actions_runner' AND external_workflow_job_id <> 0;
CREATE INDEX IF NOT EXISTS idx_deployments_project_id ON deployments(project_id);
CREATE INDEX IF NOT EXISTS idx_build_logs_build_id ON build_logs(build_id);
CREATE INDEX IF NOT EXISTS idx_ci_job_logs_job_id ON ci_job_logs(ci_job_id, created_at);
CREATE INDEX IF NOT EXISTS idx_ci_job_artifacts_job_id ON ci_job_artifacts(ci_job_id, created_at);
CREATE INDEX IF NOT EXISTS idx_vm_logs_vm_id ON vm_logs(vm_id);
CREATE INDEX IF NOT EXISTS idx_domains_project_id ON domains(project_id);
CREATE INDEX IF NOT EXISTS idx_domains_deployment_id ON domains(deployment_id);
CREATE INDEX IF NOT EXISTS idx_environment_variables_project_id ON environment_variables(project_id);
CREATE INDEX IF NOT EXISTS idx_projects_org_id ON projects(org_id);
CREATE INDEX IF NOT EXISTS idx_instance_usage_samples_server_time
    ON instance_usage_samples (server_id, sampled_at DESC);
