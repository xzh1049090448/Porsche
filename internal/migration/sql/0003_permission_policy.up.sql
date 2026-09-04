CREATE TABLE IF NOT EXISTS user_permission_heads (
 id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, guid BIGINT NOT NULL, user_id BIGINT NOT NULL, policy_version BIGINT NOT NULL, catalog_version INT NOT NULL, rule_count INT NOT NULL,
 created_at BIGINT NOT NULL, created_by BIGINT NULL, updated_at BIGINT NOT NULL, updated_by BIGINT NULL, is_deleted INT NOT NULL DEFAULT 0,
 UNIQUE KEY uk_permission_heads_guid (guid), UNIQUE KEY uk_permission_heads_user (user_id), KEY idx_permission_heads_active (is_deleted, updated_at),
 CONSTRAINT fk_permission_heads_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE IF NOT EXISTS user_permission_overrides (
 id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, guid BIGINT NOT NULL, user_id BIGINT NOT NULL, policy_version BIGINT NOT NULL, capability INT NOT NULL, effect INT NOT NULL,
 created_at BIGINT NOT NULL, created_by BIGINT NULL, updated_at BIGINT NOT NULL, updated_by BIGINT NULL, is_deleted INT NOT NULL DEFAULT 0,
 UNIQUE KEY uk_permission_overrides_guid (guid), UNIQUE KEY uk_permission_overrides_version_cap (user_id, policy_version, capability), KEY idx_permission_overrides_active (user_id, is_deleted, policy_version),
 CONSTRAINT fk_permission_overrides_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
