ALTER TABLE admin_operations
  ADD COLUMN result_auth_version INT NULL AFTER result_guid,
  ADD CONSTRAINT chk_admin_operations_result_auth_version CHECK (
    (state = 2 AND action = 2 AND result_kind = 2 AND result_guid IS NOT NULL AND result_auth_version IS NOT NULL AND result_auth_version > 0) OR
    ((state IN (1, 3, 4, 5) OR (state = 2 AND (action < 2 OR action > 2))) AND result_auth_version IS NULL)
  );
