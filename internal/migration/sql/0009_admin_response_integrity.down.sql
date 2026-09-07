ALTER TABLE admin_action_outbox DROP CHECK chk_admin_action_outbox_outcome, DROP COLUMN failure_code;
ALTER TABLE admin_operation_responses DROP CHECK chk_admin_operation_responses_lifecycle, DROP COLUMN response_hmac, DROP COLUMN integrity_version, DROP COLUMN lifecycle_state,
  ADD CONSTRAINT chk_admin_operation_responses_immutable CHECK (
    created_at >= 0 AND updated_at = created_at AND is_deleted = 0 AND
    ((created_by IS NULL AND updated_by IS NULL) OR (created_by IS NOT NULL AND updated_by = created_by))
  );
