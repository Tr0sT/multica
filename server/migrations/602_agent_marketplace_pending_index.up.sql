CREATE INDEX CONCURRENTLY IF NOT EXISTS agent_marketplace_pending_idx ON agent_marketplace (fetch_runtime_id, sync_requested_at) WHERE deleted_at IS NULL AND sync_status = 'pending';
