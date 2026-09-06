CREATE TABLE IF NOT EXISTS admin_operation_responses (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  guid BIGINT NOT NULL,
  created_at BIGINT NOT NULL,
  created_by BIGINT NULL,
  updated_at BIGINT NOT NULL,
  updated_by BIGINT NULL,
  is_deleted INT NOT NULL DEFAULT 0,
  operation_id BIGINT NOT NULL,
  http_status INT NOT NULL,
  media_type VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  response_body VARBINARY(4096) NOT NULL,
  body_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  UNIQUE KEY uk_admin_operation_responses_guid (guid),
  UNIQUE KEY uk_admin_operation_responses_operation (operation_id),
  KEY idx_admin_operation_responses_active (is_deleted, created_at),
  CONSTRAINT fk_admin_operation_responses_operation FOREIGN KEY (operation_id) REFERENCES admin_operations(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT chk_admin_operation_responses_http CHECK (http_status = 201 AND media_type = 'application/json'),
  CONSTRAINT chk_admin_operation_responses_body CHECK (OCTET_LENGTH(response_body) BETWEEN 2 AND 4096 AND OCTET_LENGTH(body_sha256) = 64),
  CONSTRAINT chk_admin_operation_responses_immutable CHECK (
    created_at >= 0 AND updated_at = created_at AND is_deleted = 0 AND
    ((created_by IS NULL AND updated_by IS NULL) OR (created_by IS NOT NULL AND updated_by = created_by))
  )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
