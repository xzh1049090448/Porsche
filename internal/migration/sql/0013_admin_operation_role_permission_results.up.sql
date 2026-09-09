ALTER TABLE admin_operations
  DROP CHECK chk_admin_operations_result_auth_version,
  ADD COLUMN result_permissions_version BIGINT NULL AFTER result_auth_version,
  ADD COLUMN result_role INT NULL AFTER result_permissions_version,
  ADD CONSTRAINT chk_admin_operations_result_auth_version CHECK (
    (state = 2 AND action = 2 AND result_kind = 2 AND result_guid IS NOT NULL AND result_auth_version IS NOT NULL AND result_auth_version > 0) OR
    (state = 2 AND action IN (3, 4, 5) AND result_kind = 2 AND result_guid IS NOT NULL AND result_auth_version IS NOT NULL AND result_auth_version > 0) OR
    ((state IN (1, 3, 4, 5) OR (state = 2 AND (action < 2 OR action > 5))) AND result_auth_version IS NULL)
  ),
  ADD CONSTRAINT chk_admin_operations_result_role_permission CHECK (
    (state = 2 AND action = 3 AND result_kind = 2 AND result_guid IS NOT NULL AND result_permissions_version IS NOT NULL AND result_permissions_version > 0 AND result_role IS NOT NULL AND result_role = 10) OR
    (state = 2 AND action = 4 AND result_kind = 2 AND result_guid IS NOT NULL AND result_permissions_version IS NOT NULL AND result_permissions_version > 0 AND result_role IS NOT NULL AND result_role = 1) OR
    (state = 2 AND action = 5 AND result_kind = 2 AND result_guid IS NOT NULL AND result_permissions_version IS NOT NULL AND result_permissions_version > 0 AND result_role IS NOT NULL AND result_role = 10) OR
    ((state IN (1, 3, 4, 5) OR (state = 2 AND (action < 3 OR action > 5))) AND result_permissions_version IS NULL AND result_role IS NULL)
  );
