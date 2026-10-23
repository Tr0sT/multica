-- name: UpdateMattermostInstallationSettings :one
UPDATE channel_installation
SET config = config || @settings::jsonb,
    updated_at = now()
WHERE id = @id AND workspace_id = @workspace_id
  AND channel_type = 'mattermost' AND status = 'active'
RETURNING *;

-- name: UpdateMattermostInstallationToken :one
-- A credential restore must preserve concurrent settings edits and revocation.
UPDATE channel_installation
SET config = jsonb_set(config, '{access_token_encrypted}', to_jsonb(@ciphertext::text)),
    updated_at = now()
WHERE id = @id AND workspace_id = @workspace_id
  AND channel_type = 'mattermost'
RETURNING *;

-- name: GetLiveMattermostBindingToken :one
SELECT * FROM channel_binding_token
WHERE token_hash = $1
  AND channel_type = 'mattermost'
  AND consumed_at IS NULL
  AND expires_at > now();

-- name: RotateMattermostInstallationIdentity :execrows
-- Old sockets and in-flight requests must never acquire the new bot's identity.
WITH detached_audit AS (
    UPDATE channel_inbound_audit SET installation_id = NULL WHERE installation_id = @old_id
), cleared_replies AS (
    DELETE FROM channel_reply_delivery WHERE installation_id = @old_id
)
UPDATE channel_installation AS installation
SET id = @new_id, ws_lease_token = NULL, ws_lease_expires_at = NULL
WHERE installation.id = @old_id AND installation.channel_type = 'mattermost';

-- name: QueueMattermostFailureReply :exec
-- Only newly finalized failures enter the outbox; historical failures stay silent.
UPDATE channel_task_delivery AS delivery
SET config = delivery.config || '{"failure_notice":true}'::jsonb
WHERE delivery.task_id = $1 AND delivery.channel_type = 'mattermost'
  AND EXISTS (SELECT 1 FROM agent_task_queue WHERE id = delivery.task_id AND status = 'failed')
  -- Retry clones set both links; parent_task_id supplies the existing lookup index.
  AND NOT EXISTS (
      SELECT 1 FROM agent_task_queue
      WHERE parent_task_id = delivery.task_id AND retry_of_task_id = delivery.task_id
  );

-- name: ListPendingMattermostReplies :many
-- Committed answers and failure notices are the outbox; events only wake its worker.
SELECT delivery.task_id
FROM channel_task_delivery delivery
JOIN agent_task_queue task ON task.id = delivery.task_id
LEFT JOIN chat_message message ON message.task_id = task.id AND message.role = 'assistant'
JOIN channel_installation installation ON installation.id = delivery.installation_id
LEFT JOIN channel_reply_delivery reply ON reply.task_id = task.id
WHERE delivery.channel_type = 'mattermost'
  AND installation.channel_type = 'mattermost' AND installation.status = 'active'
  AND (task.chat_input_task_id IS NULL OR EXISTS (
      SELECT 1 FROM chat_message input_message
      WHERE input_message.task_id = task.chat_input_task_id AND input_message.role = 'user' AND input_message.channel_ingested
  ))
  AND ((task.status = 'failed' AND delivery.config->>'failure_notice' = 'true'
        AND NOT EXISTS (
            SELECT 1 FROM agent_task_queue
            WHERE parent_task_id = task.id AND retry_of_task_id = task.id
        ))
    OR (task.status = 'completed' AND (message.content <> '' OR EXISTS (
      SELECT 1 FROM attachment
      WHERE attachment.chat_message_id = message.id
        AND attachment.workspace_id = installation.workspace_id
  ) OR delivery.config ? 'reply_attachment_ids')))
  AND (reply.turn_id IS NULL OR (reply.phase <> 'settled' AND reply.updated_at < now() - interval '30 seconds'))
ORDER BY COALESCE(reply.updated_at, message.created_at, task.completed_at, task.created_at), delivery.task_id
LIMIT 32;

-- name: SnapshotMattermostReplyAttachments :one
-- Keep chunk positions stable across retries and attachment deletion.
UPDATE channel_task_delivery AS delivery
SET config = jsonb_set(delivery.config, '{reply_attachment_ids}',
    COALESCE(delivery.config->'reply_attachment_ids', (
        SELECT COALESCE(jsonb_agg(attachment.id ORDER BY attachment.created_at, attachment.id), '[]'::jsonb)
        FROM attachment
        JOIN chat_message message ON message.id = attachment.chat_message_id
        WHERE message.task_id = delivery.task_id AND message.role = 'assistant'
          AND attachment.workspace_id = @workspace_id
    )))
WHERE delivery.task_id = @task_id AND delivery.channel_type = 'mattermost'
RETURNING config;

-- name: UpdateMattermostChatSessionMetadata :exec
UPDATE channel_chat_session_binding
SET config = config || @config::jsonb
WHERE chat_session_id = @chat_session_id AND installation_id = @installation_id
  AND channel_type = 'mattermost'
  AND config IS DISTINCT FROM config || @config::jsonb;
