-- name: UpdateMattermostInstallationContext :one
UPDATE channel_installation
SET config = jsonb_set(config, '{context}', @context::jsonb),
    updated_at = now()
WHERE id = @id AND workspace_id = @workspace_id
  AND channel_type = 'mattermost' AND status = 'active'
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

-- name: ListPendingMattermostReplies :many
-- The committed assistant message is the outbox; an event only wakes its worker.
SELECT delivery.task_id
FROM channel_task_delivery delivery
JOIN agent_task_queue task ON task.id = delivery.task_id
JOIN chat_message message ON message.task_id = task.id AND message.role = 'assistant'
JOIN channel_installation installation ON installation.id = delivery.installation_id
LEFT JOIN channel_reply_delivery reply ON reply.task_id = task.id
WHERE delivery.channel_type = 'mattermost'
  AND installation.channel_type = 'mattermost' AND installation.status = 'active'
  AND task.status = 'completed'
  AND (task.chat_input_task_id IS NULL OR EXISTS (
      SELECT 1 FROM chat_message input_message
      WHERE input_message.task_id = task.chat_input_task_id AND input_message.role = 'user' AND input_message.channel_ingested
  ))
  AND message.content <> ''
  AND (reply.turn_id IS NULL OR (reply.phase <> 'settled' AND reply.updated_at < now() - interval '30 seconds'))
ORDER BY COALESCE(reply.updated_at, message.created_at), delivery.task_id
LIMIT 32;
