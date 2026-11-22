-- name: ListAgentMarketplaces :many
SELECT * FROM agent_marketplace
WHERE workspace_id = $1 AND deleted_at IS NULL ORDER BY created_at, id;

-- name: GetAgentMarketplace :one
SELECT * FROM agent_marketplace WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL;

-- name: LockAgentMarketplace :one
SELECT * FROM agent_marketplace WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL FOR UPDATE;

-- name: CreateAgentMarketplace :one
INSERT INTO agent_marketplace (workspace_id, source_url, source_ref, fetch_runtime_id, created_by)
VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: RefreshAgentMarketplace :one
UPDATE agent_marketplace SET fetch_runtime_id = $3, sync_status = 'pending',
    sync_request_id = gen_random_uuid(), sync_requested_at = now(), sync_started_at = NULL,
    sync_error = '', updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL
  AND sync_status NOT IN ('pending', 'running')
RETURNING *;

-- name: ClaimAgentMarketplaceSync :one
WITH pending AS (
    SELECT source.id FROM agent_marketplace source WHERE source.fetch_runtime_id = $1
      AND source.deleted_at IS NULL AND source.sync_status = 'pending'
      AND EXISTS (
          SELECT 1 FROM agent_runtime rt
          WHERE rt.id = source.fetch_runtime_id AND rt.workspace_id = source.workspace_id
            AND ((sqlc.arg('daemon_id')::text <> '' AND rt.daemon_id = sqlc.arg('daemon_id')::text)
              OR (sqlc.arg('daemon_id')::text = '' AND rt.owner_id = sqlc.narg('owner_id')::uuid))
      )
    ORDER BY source.sync_requested_at LIMIT 1 FOR UPDATE SKIP LOCKED
)
UPDATE agent_marketplace AS m SET sync_status = 'running', sync_started_at = now(), updated_at = now()
FROM pending WHERE m.id = pending.id RETURNING m.*;

-- name: ExpireAgentMarketplaceSyncs :exec
UPDATE agent_marketplace SET sync_status = 'failed', sync_error = 'Runtime did not finish synchronizing the marketplace; retry with an online runtime.', updated_at = now()
WHERE workspace_id = $1 AND deleted_at IS NULL
  AND sync_status IN ('pending', 'running') AND sync_requested_at < now() - interval '5 minutes';

-- name: CompleteAgentMarketplaceSync :one
UPDATE agent_marketplace SET name = $4, display_name = $5, revision = $6,
    sync_status = 'ready', sync_error = '', updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND sync_request_id = $3
  AND sync_status = 'running' AND deleted_at IS NULL RETURNING *;

-- name: FailAgentMarketplaceSync :execrows
UPDATE agent_marketplace SET sync_status = 'failed', sync_error = $4, updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND sync_request_id = $3
  AND sync_status = 'running' AND deleted_at IS NULL;

-- name: DeleteAgentMarketplace :execrows
UPDATE agent_marketplace SET deleted_at = now(), updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL;

-- name: CountAgentMarketplaceBindings :one
SELECT count(*) FROM agent
WHERE workspace_id = $1 AND archived_at IS NULL
  AND plugin_selection @> sqlc.arg('selection')::jsonb;

-- name: CreateAgentPluginVersion :one
INSERT INTO agent_plugin_version (workspace_id, marketplace_id, plugin_name, revision, digest, metadata, files)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (marketplace_id, revision, plugin_name) DO NOTHING RETURNING *;

-- name: GetAgentPluginVersionBySource :one
SELECT * FROM agent_plugin_version WHERE workspace_id = $1 AND marketplace_id = $2 AND revision = $3 AND plugin_name = $4;

-- name: ListCurrentAgentPlugins :many
SELECT v.id, v.marketplace_id, v.plugin_name, v.revision, v.digest, v.metadata, m.name AS marketplace_name
FROM agent_plugin_version v JOIN agent_marketplace m ON m.id = v.marketplace_id AND m.workspace_id = v.workspace_id
WHERE v.workspace_id = $1 AND m.deleted_at IS NULL AND v.revision = m.revision
ORDER BY m.name, v.plugin_name;

-- name: ResolveCurrentAgentPlugin :one
SELECT v.id FROM agent_plugin_version v
JOIN agent_marketplace m ON m.id = v.marketplace_id AND m.workspace_id = v.workspace_id
WHERE v.workspace_id = $1 AND m.id = $2 AND v.plugin_name = $3
  AND m.deleted_at IS NULL AND v.revision = m.revision;

-- name: GetAgentPluginVersions :many
SELECT v.*, m.name AS marketplace_name
FROM agent_plugin_version v JOIN agent_marketplace m ON m.id = v.marketplace_id AND m.workspace_id = v.workspace_id
WHERE v.workspace_id = $1 AND v.id = ANY(sqlc.arg('ids')::uuid[])
ORDER BY m.name, v.plugin_name;

-- name: GetAgentPluginVersionsSize :one
SELECT COALESCE(sum(octet_length(decode(file_entry.data->>'content', 'base64'))), 0)::bigint
FROM agent_plugin_version v
CROSS JOIN LATERAL jsonb_array_elements(v.files) AS file_entry(data)
WHERE v.workspace_id = $1 AND v.id = ANY(sqlc.arg('ids')::uuid[]);

-- name: LockAgentPluginSelection :one
SELECT plugin_selection FROM agent WHERE id = $1 AND workspace_id = $2 AND archived_at IS NULL FOR UPDATE;

-- name: SetAgentPluginSelection :one
UPDATE agent SET plugin_selection = $3, updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND archived_at IS NULL RETURNING *;

-- name: PinExistingAgentPluginContexts :exec
INSERT INTO agent_plugin_snapshot (workspace_id, agent_id, context_key, configured)
SELECT DISTINCT sqlc.arg('workspace_id')::uuid, t.agent_id,
    CASE WHEN t.issue_id IS NOT NULL THEN 'issue:' || t.issue_id::text
         WHEN t.chat_session_id IS NOT NULL THEN 'chat:' || t.chat_session_id::text
         ELSE 'task:' || t.id::text END, false
FROM agent_task_queue t
WHERE t.agent_id = sqlc.arg('agent_id')::uuid AND t.dispatched_at IS NOT NULL
ON CONFLICT (workspace_id, agent_id, context_key) DO NOTHING;

-- name: GetAgentPluginSnapshot :one
SELECT * FROM agent_plugin_snapshot WHERE workspace_id = $1 AND agent_id = $2 AND context_key = $3;

-- name: CreateAgentPluginSnapshot :one
INSERT INTO agent_plugin_snapshot (workspace_id, agent_id, context_key, configured, version_ids)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (workspace_id, agent_id, context_key) DO NOTHING RETURNING *;

-- name: DeleteWorkspaceAgentPluginSnapshots :exec
DELETE FROM agent_plugin_snapshot WHERE workspace_id = $1;

-- name: DeleteWorkspaceAgentPluginVersions :exec
DELETE FROM agent_plugin_version WHERE workspace_id = $1;

-- name: DeleteWorkspaceAgentMarketplaces :exec
DELETE FROM agent_marketplace WHERE workspace_id = $1;
