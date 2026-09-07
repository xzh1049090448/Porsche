ALTER TABLE admin_operation_responses
  ADD COLUMN target_guid BIGINT NULL AFTER operation_id;

ALTER TABLE admin_action_outbox
  ADD COLUMN result_kind INT NULL AFTER failure_code;

UPDATE admin_action_outbox AS outbox
JOIN admin_operations AS operation ON operation.id = outbox.operation_id
JOIN users AS target ON target.guid = outbox.result_guid
SET outbox.result_kind = 2
WHERE outbox.result_kind IS NULL
  AND outbox.operation_id = operation.id
  AND outbox.action = operation.action
  AND outbox.public_ref = operation.public_ref
  AND outbox.action IN (1, 9)
  AND outbox.public_ref REGEXP '^op_[A-Za-z0-9_-]{43}$'
  AND outbox.target_kind = 1
  AND outbox.target_guid IS NULL
  AND outbox.state = 2
  AND outbox.failure_code IS NULL
  AND outbox.result_guid > 0
  AND outbox.is_deleted = 0
  AND target.guid = outbox.result_guid
  AND (
    (operation.state = 2 AND operation.is_deleted = 0 AND operation.finished_at IS NOT NULL AND operation.finished_at > 0 AND
      operation.error_code IS NULL AND operation.result_kind = 2 AND operation.result_guid = outbox.result_guid AND
      operation.result_http_status = 201 AND operation.lease_owner_hmac IS NULL AND operation.lease_expires_at IS NULL AND
      operation.query_expires_at >= operation.finished_at) OR
    (operation.state = 5 AND operation.is_deleted = 1 AND operation.finished_at IS NOT NULL AND operation.finished_at > 0 AND
      operation.error_code IS NULL AND operation.result_kind IS NULL AND operation.result_guid IS NULL AND
      operation.result_http_status IS NULL AND operation.lease_owner_hmac IS NULL AND operation.lease_expires_at IS NULL)
  );

UPDATE admin_operation_responses AS response
JOIN admin_operations AS operation ON operation.id = response.operation_id
SET response.lifecycle_state = 2,
    response.integrity_version = 1,
    response.response_hmac = REPEAT('0', 64),
    response.http_status = 201,
    response.media_type = 'application/json',
    response.response_body=X'7B7D',
    response.body_sha256 = '44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a',
    response.is_deleted = 1,
    response.updated_at = GREATEST(response.updated_at, response.created_at),
    response.updated_by = COALESCE(response.updated_by, response.created_by, operation.actor_user_id)
WHERE response.lifecycle_state = 2;

UPDATE admin_operation_responses AS response
JOIN admin_operations AS operation ON operation.id = response.operation_id
JOIN users AS target ON target.guid = operation.result_guid
SET response.target_guid = operation.result_guid
WHERE response.target_guid IS NULL
  AND operation.action IN (1, 9)
  AND operation.public_ref REGEXP '^op_[A-Za-z0-9_-]{43}$'
  AND operation.state = 2
  AND operation.is_deleted = 0
  AND operation.finished_at IS NOT NULL
  AND operation.finished_at > 0
  AND operation.error_code IS NULL
  AND operation.result_kind = 2
  AND operation.result_guid > 0
  AND operation.result_http_status = 201
  AND operation.lease_owner_hmac IS NULL
  AND operation.lease_expires_at IS NULL
  AND operation.query_expires_at >= operation.finished_at
  AND target.guid = operation.result_guid;

UPDATE admin_operation_responses AS response
JOIN admin_operations AS operation ON operation.id = response.operation_id
JOIN admin_action_outbox AS outbox ON outbox.operation_id = operation.id
JOIN users AS target ON target.guid = outbox.result_guid
SET response.target_guid = outbox.result_guid
WHERE response.target_guid IS NULL
  AND operation.action IN (1, 9)
  AND operation.state = 5
  AND operation.is_deleted = 1
  AND operation.finished_at IS NOT NULL
  AND operation.finished_at > 0
  AND operation.error_code IS NULL
  AND operation.result_kind IS NULL
  AND operation.result_guid IS NULL
  AND operation.result_http_status IS NULL
  AND operation.lease_owner_hmac IS NULL
  AND operation.lease_expires_at IS NULL
  AND outbox.operation_id = operation.id
  AND outbox.action = operation.action
  AND outbox.public_ref = operation.public_ref
  AND outbox.public_ref REGEXP '^op_[A-Za-z0-9_-]{43}$'
  AND outbox.target_kind = 1
  AND outbox.target_guid IS NULL
  AND outbox.state = 2
  AND outbox.failure_code IS NULL
  AND outbox.result_kind = 2
  AND outbox.result_guid > 0
  AND outbox.is_deleted = 0
  AND target.guid = outbox.result_guid;

UPDATE admin_operation_responses AS response
JOIN admin_operations AS operation ON operation.id = response.operation_id
LEFT JOIN users AS target ON target.guid = response.target_guid
SET response.target_guid = CASE WHEN target.guid IS NULL THEN NULL ELSE response.target_guid END,
    response.lifecycle_state = 2,
    response.integrity_version = 1,
    response.response_hmac = REPEAT('0', 64),
    response.http_status = 201,
    response.media_type = 'application/json',
    response.response_body=X'7B7D',
    response.body_sha256 = '44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a',
    response.is_deleted = 1,
    response.updated_at = GREATEST(response.updated_at, response.created_at),
    response.updated_by = COALESCE(response.updated_by, response.created_by, operation.actor_user_id)
WHERE response.lifecycle_state = 1
  AND response.is_deleted = 0
  AND (
    response.target_guid IS NULL OR response.target_guid <= 0 OR target.guid IS NULL OR
    response.integrity_version <> 1 OR response.response_hmac IS NULL OR
    response.response_hmac = REPEAT('0', 64) OR response.response_hmac NOT REGEXP '^[0-9a-f]{64}$' OR
    response.http_status <> 201 OR response.media_type <> 'application/json' OR
    response.body_sha256 <> LOWER(SHA2(response.response_body, 256))
  );

ALTER TABLE admin_action_outbox
  DROP CHECK chk_admin_action_outbox_outcome,
  ADD CONSTRAINT chk_admin_action_outbox_outcome CHECK (
    (state = 2 AND failure_code IS NULL AND (result_kind IS NULL OR result_kind IN (1, 2, 3)) AND result_guid IS NOT NULL) OR
    (state = 3 AND failure_code IN (1, 2, 3, 4, 5) AND result_kind IS NULL AND result_guid IS NULL)
  );

ALTER TABLE admin_operation_responses
  DROP CHECK chk_admin_operation_responses_lifecycle,
  ADD KEY idx_admin_operation_responses_target (target_guid, lifecycle_state, is_deleted, id),
  ADD CONSTRAINT fk_admin_operation_responses_target FOREIGN KEY (target_guid) REFERENCES users(guid) ON DELETE RESTRICT ON UPDATE RESTRICT,
  ADD CONSTRAINT chk_admin_operation_responses_lifecycle CHECK (
    (lifecycle_state = 1 AND target_guid IS NOT NULL AND target_guid > 0 AND is_deleted = 0 AND updated_at = created_at AND
      integrity_version = 1 AND OCTET_LENGTH(response_hmac) = 64 AND
      ((created_by IS NULL AND updated_by IS NULL) OR (created_by IS NOT NULL AND updated_by = created_by))) OR
    (lifecycle_state = 2 AND (target_guid IS NULL OR target_guid > 0) AND is_deleted = 1 AND integrity_version = 1 AND OCTET_LENGTH(response_hmac) = 64 AND
      http_status = 201 AND media_type = 'application/json' AND
      response_body = X'7B7D' AND body_sha256 = '44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a' AND
      updated_at >= created_at AND updated_by IS NOT NULL)
  );
