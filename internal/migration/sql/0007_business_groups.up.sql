CREATE TABLE IF NOT EXISTS business_groups (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  guid BIGINT NOT NULL,
  group_key VARCHAR(64) NOT NULL,
  display_name VARCHAR(64) NOT NULL,
  status INT NOT NULL,
  created_at BIGINT NOT NULL,
  created_by BIGINT NULL,
  updated_at BIGINT NOT NULL,
  updated_by BIGINT NULL,
  is_deleted INT NOT NULL DEFAULT 0,
  UNIQUE KEY uk_business_groups_guid (guid),
  UNIQUE KEY uk_business_groups_key_deleted (group_key, is_deleted),
  KEY idx_business_groups_active (status, is_deleted, group_key),
  CONSTRAINT chk_business_groups_status CHECK (status IN (1, 2)),
  CONSTRAINT chk_business_groups_audit CHECK (created_at >= 0 AND updated_at >= 0 AND is_deleted IN (0, 1))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE users ADD COLUMN group_id BIGINT NULL;

-- porsche:seed-default-business-group

UPDATE users SET group_id = ? WHERE group_id IS NULL;

ALTER TABLE users MODIFY COLUMN group_id BIGINT NOT NULL;

CREATE INDEX idx_users_group_id ON users (group_id);

ALTER TABLE users ADD CONSTRAINT fk_users_business_group FOREIGN KEY (group_id) REFERENCES business_groups(id) ON DELETE RESTRICT ON UPDATE RESTRICT;
