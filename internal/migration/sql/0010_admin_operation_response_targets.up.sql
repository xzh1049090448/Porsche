ALTER TABLE admin_operation_responses
  ADD COLUMN target_guid BIGINT NULL AFTER operation_id;

UPDATE admin_operation_responses AS response
JOIN admin_operations AS operation ON operation.id = response.operation_id
LEFT JOIN admin_action_outbox AS outbox ON outbox.operation_id = response.operation_id
SET response.target_guid = COALESCE(operation.result_guid, outbox.result_guid)
WHERE response.target_guid IS NULL
  AND operation.action IN (1, 9)
  AND COALESCE(operation.result_guid, outbox.result_guid) > 0
  AND (operation.result_guid IS NULL OR outbox.result_guid IS NULL OR operation.result_guid = outbox.result_guid);

UPDATE admin_operation_responses AS response
JOIN admin_operations AS operation ON operation.id = response.operation_id
SET response.lifecycle_state = 2,
    response.integrity_version = 1,
    response.response_hmac = REPEAT('0', 64),
    response.response_body=X'7B7D',
    response.body_sha256 = '44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a',
    response.is_deleted = 1,
    response.updated_at = GREATEST(response.updated_at, response.created_at),
    response.updated_by = COALESCE(response.updated_by, response.created_by, operation.actor_user_id)
WHERE (response.lifecycle_state = 1
       AND response.is_deleted = 0
       AND (response.target_guid IS NULL OR response.target_guid <= 0 OR response.integrity_version <> 1 OR response.response_hmac IS NULL))
   OR (response.lifecycle_state = 2
       AND response.is_deleted = 1
       AND (response.target_guid IS NULL OR response.target_guid <= 0));

ALTER TABLE admin_operation_responses
  DROP CHECK chk_admin_operation_responses_lifecycle,
  ADD KEY idx_admin_operation_responses_target (target_guid, lifecycle_state, is_deleted, id),
  ADD CONSTRAINT fk_admin_operation_responses_target FOREIGN KEY (target_guid) REFERENCES users(guid) ON DELETE RESTRICT ON UPDATE RESTRICT,
  ADD CONSTRAINT chk_admin_operation_responses_lifecycle CHECK (
    (lifecycle_state = 1 AND target_guid IS NOT NULL AND target_guid > 0 AND is_deleted = 0 AND updated_at = created_at AND
      integrity_version = 1 AND OCTET_LENGTH(response_hmac) = 64 AND
      ((created_by IS NULL AND updated_by IS NULL) OR (created_by IS NOT NULL AND updated_by = created_by))) OR
    (lifecycle_state = 2 AND (target_guid IS NULL OR target_guid > 0) AND is_deleted = 1 AND integrity_version = 1 AND OCTET_LENGTH(response_hmac) = 64 AND
      response_body = X'7B7D' AND body_sha256 = '44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a' AND
      updated_at >= created_at AND updated_by IS NOT NULL)
  );
