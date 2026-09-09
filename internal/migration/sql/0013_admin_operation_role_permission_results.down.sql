ALTER TABLE admin_operations
  DROP CHECK chk_admin_operations_result_role_permission,
  DROP CHECK chk_admin_operations_result_auth_version;

UPDATE admin_operations
SET state = 4,
    finished_at = NULL,
    error_code = NULL,
    result_kind = NULL,
    result_guid = NULL,
    result_auth_version = NULL,
    result_http_status = NULL
WHERE state = 2 AND action IN (3, 4, 5);

ALTER TABLE admin_operations
  DROP COLUMN result_role,
  DROP COLUMN result_permissions_version,
  ADD CONSTRAINT chk_admin_operations_result_auth_version CHECK (
    (state = 2 AND action = 2 AND result_kind = 2 AND result_guid IS NOT NULL AND result_auth_version IS NOT NULL AND result_auth_version > 0) OR
    ((state IN (1, 3, 4, 5) OR (state = 2 AND (action < 2 OR action > 2))) AND result_auth_version IS NULL)
  );
