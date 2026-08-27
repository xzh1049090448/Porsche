-- 0002 is additive. Existing users keep username NULL until an approved
-- account migration or registration flow assigns a username.
ALTER TABLE users
  MODIFY COLUMN phone VARCHAR(20) NULL,
  ADD COLUMN username VARCHAR(20) NULL,
  ADD COLUMN role INT NOT NULL DEFAULT 1,
  ADD COLUMN auth_version INT NOT NULL DEFAULT 1,
  ADD COLUMN last_login_at BIGINT NULL,
  ADD UNIQUE KEY uk_users_username (username);

CREATE TABLE IF NOT EXISTS user_sessions (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  guid BIGINT NOT NULL,
  sid CHAR(36) NOT NULL,
  user_id BIGINT NOT NULL,
  login_method INT NOT NULL,
  ip VARCHAR(64) NULL,
  user_agent VARCHAR(512) NULL,
  session_version INT NOT NULL DEFAULT 1,
  refresh_hmac CHAR(64) NOT NULL,
  previous_refresh_hmac CHAR(64) NULL,
  previous_refresh_expires_at BIGINT NULL,
  last_active_at BIGINT NOT NULL,
  expires_at BIGINT NOT NULL,
  revoked_at BIGINT NULL,
  created_at BIGINT NOT NULL,
  created_by BIGINT NULL,
  updated_at BIGINT NOT NULL,
  updated_by BIGINT NULL,
  is_deleted INT NOT NULL DEFAULT 0,
  UNIQUE KEY uk_user_sessions_guid (guid),
  UNIQUE KEY uk_user_sessions_sid (sid),
  KEY idx_user_sessions_user_active_expires (user_id, is_deleted, expires_at),
  KEY idx_user_sessions_active_expiry (is_deleted, expires_at),
  CONSTRAINT fk_user_sessions_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS auth_audit_events (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  guid BIGINT NOT NULL,
  user_id BIGINT NULL,
  session_guid BIGINT NULL,
  event_type INT NOT NULL,
  login_method INT NULL,
  ip VARCHAR(64) NULL,
  user_agent VARCHAR(512) NULL,
  created_at BIGINT NOT NULL,
  created_by BIGINT NULL,
  updated_at BIGINT NOT NULL,
  updated_by BIGINT NULL,
  is_deleted INT NOT NULL DEFAULT 0,
  UNIQUE KEY uk_auth_audit_events_guid (guid),
  KEY idx_auth_audit_events_user_active_created (user_id, is_deleted, created_at),
  KEY idx_auth_audit_events_event_active_created (event_type, is_deleted, created_at),
  CONSTRAINT fk_auth_audit_events_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
