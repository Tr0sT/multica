CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS agent_plugin_snapshot_context_idx ON agent_plugin_snapshot (workspace_id, agent_id, context_key);
