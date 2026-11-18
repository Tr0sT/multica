CREATE TABLE agent_marketplace (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    source_url TEXT NOT NULL,
    source_ref TEXT NOT NULL DEFAULT '',
    fetch_runtime_id UUID NOT NULL,
    created_by UUID NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    revision TEXT NOT NULL DEFAULT '',
    sync_status TEXT NOT NULL DEFAULT 'pending',
    sync_request_id UUID NOT NULL DEFAULT gen_random_uuid(),
    sync_requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    sync_started_at TIMESTAMPTZ,
    sync_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE agent_plugin_version (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    marketplace_id UUID NOT NULL,
    plugin_name TEXT NOT NULL,
    revision TEXT NOT NULL,
    digest TEXT NOT NULL,
    metadata JSONB NOT NULL,
    files JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE agent_plugin_snapshot (
    workspace_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    context_key TEXT NOT NULL,
    configured BOOLEAN NOT NULL,
    version_ids UUID[] NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE agent ADD COLUMN plugin_selection JSONB;
