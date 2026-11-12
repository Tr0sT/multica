CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS agent_marketplace_name_idx ON agent_marketplace (workspace_id, name) WHERE deleted_at IS NULL AND name <> '';
