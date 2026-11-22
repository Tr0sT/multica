CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS agent_marketplace_source_idx ON agent_marketplace (workspace_id, source_url, source_ref) WHERE deleted_at IS NULL;
