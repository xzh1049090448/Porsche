ALTER TABLE admin_operations
  DROP CHECK chk_admin_operations_result_auth_version,
  DROP COLUMN result_auth_version;
