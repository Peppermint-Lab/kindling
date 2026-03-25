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

-- Projects: a git-connected application
CREATE TABLE IF NOT EXISTS projects (
    id                      UUID PRIMARY KEY,
    name                    TEXT NOT NULL,
    github_repository       TEXT NOT NULL DEFAULT '',
    github_installation_id  BIGINT NOT NULL DEFAULT 0,
    github_webhook_secret   TEXT NOT NULL DEFAULT '',
    root_directory          TEXT NOT NULL DEFAULT '',
    dockerfile_path         TEXT NOT NULL DEFAULT 'Dockerfile',
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

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
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'starting', 'running', 'stopped', 'failed')),
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
    id              UUID PRIMARY KEY,
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    build_id        UUID REFERENCES builds(id),
    image_id        UUID REFERENCES images(id),
    vm_id           UUID REFERENCES vms(id),
    github_commit   TEXT NOT NULL DEFAULT '',
    running_at      TIMESTAMPTZ,
    stopped_at      TIMESTAMPTZ,
    failed_at       TIMESTAMPTZ,
    deleted_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Domains: hostname routing
CREATE TABLE IF NOT EXISTS domains (
    id                  UUID PRIMARY KEY,
    project_id          UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    deployment_id       UUID REFERENCES deployments(id),
    domain_name         TEXT NOT NULL UNIQUE,
    verified_at         TIMESTAMPTZ,
    redirect_to         TEXT,
    redirect_status_code INT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

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
