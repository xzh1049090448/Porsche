ALTER TABLE admin_operation_responses
  DROP CHECK chk_admin_operation_responses_immutable,
  ADD COLUMN lifecycle_state INT NOT NULL DEFAULT 1 AFTER operation_id,
  ADD COLUMN integrity_version INT NOT NULL DEFAULT 0 AFTER lifecycle_state,
  ADD COLUMN response_hmac CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER integrity_version,
  ADD CONSTRAINT chk_admin_operation_responses_lifecycle CHECK (
    (lifecycle_state = 1 AND is_deleted = 0 AND updated_at = created_at AND
      ((integrity_version = 0 AND response_hmac IS NULL) OR (integrity_version = 1 AND OCTET_LENGTH(response_hmac) = 64)) AND
      ((created_by IS NULL AND updated_by IS NULL) OR (created_by IS NOT NULL AND updated_by = created_by))) OR
    (lifecycle_state = 2 AND is_deleted = 1 AND integrity_version = 1 AND OCTET_LENGTH(response_hmac) = 64 AND
      OCTET_LENGTH(response_body) = 2 AND updated_at >= created_at AND updated_by IS NOT NULL)
  );

ALTER TABLE admin_action_outbox ADD COLUMN failure_code INT NULL AFTER state;
UPDATE admin_action_outbox AS outbox
JOIN admin_operations AS operation ON operation.id = outbox.operation_id
SET outbox.failure_code = operation.error_code
WHERE outbox.state = 3 AND outbox.failure_code IS NULL;
ALTER TABLE admin_action_outbox
  ADD CONSTRAINT chk_admin_action_outbox_outcome CHECK (
    (state = 2 AND failure_code IS NULL AND result_guid IS NOT NULL) OR
    (state = 3 AND failure_code IN (1, 2, 3, 4, 5) AND result_guid IS NULL)
  );
