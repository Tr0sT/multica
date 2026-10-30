CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS agent_plugin_version_source_idx ON agent_plugin_version (marketplace_id, revision, plugin_name);
