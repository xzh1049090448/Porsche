ALTER TABLE admin_operation_responses
  DROP CHECK chk_admin_operation_responses_lifecycle,
  DROP FOREIGN KEY fk_admin_operation_responses_target,
  DROP INDEX idx_admin_operation_responses_target,
  DROP COLUMN target_guid,
  ADD CONSTRAINT chk_admin_operation_responses_lifecycle CHECK (
    (lifecycle_state = 1 AND is_deleted = 0 AND updated_at = created_at AND
      ((integrity_version = 0 AND response_hmac IS NULL) OR (integrity_version = 1 AND OCTET_LENGTH(response_hmac) = 64)) AND
      ((created_by IS NULL AND updated_by IS NULL) OR (created_by IS NOT NULL AND updated_by = created_by))) OR
    (lifecycle_state = 2 AND is_deleted = 1 AND integrity_version = 1 AND OCTET_LENGTH(response_hmac) = 64 AND
      OCTET_LENGTH(response_body) = 2 AND updated_at >= created_at AND updated_by IS NOT NULL)
  );

ALTER TABLE admin_action_outbox
  DROP CHECK chk_admin_action_outbox_outcome,
  DROP COLUMN result_kind,
  ADD CONSTRAINT chk_admin_action_outbox_outcome CHECK (
    (state = 2 AND failure_code IS NULL AND result_guid IS NOT NULL) OR
    (state = 3 AND failure_code IN (1, 2, 3, 4, 5) AND result_guid IS NULL)
  );
