CREATE TABLE IF NOT EXISTS admin_action_verifications (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  guid BIGINT NOT NULL,
  actor_user_id BIGINT NOT NULL,
  actor_auth_version INT NOT NULL,
  session_id BIGINT NOT NULL,
  action INT NOT NULL,
  target_kind INT NOT NULL,
  target_guid BIGINT NULL,
  intent_hmac CHAR(64) NOT NULL,
  ticket_hmac CHAR(64) NOT NULL,
  expires_at BIGINT NOT NULL,
  consumed_at BIGINT NULL,
  created_at BIGINT NOT NULL,
  created_by BIGINT NULL,
  updated_at BIGINT NOT NULL,
  updated_by BIGINT NULL,
  is_deleted INT NOT NULL DEFAULT 0,
  UNIQUE KEY uk_admin_action_verifications_guid (guid),
  UNIQUE KEY uk_admin_action_verifications_ticket_hmac (ticket_hmac),
  KEY idx_admin_action_verifications_actor_session_active (actor_user_id, session_id, is_deleted, expires_at),
  KEY idx_admin_action_verifications_action_target_active (action, target_kind, target_guid, is_deleted),
  KEY idx_admin_action_verifications_expiry (is_deleted, expires_at),
  KEY fk_admin_action_verifications_session (session_id),
  CONSTRAINT fk_admin_action_verifications_actor FOREIGN KEY (actor_user_id) REFERENCES users(id),
  CONSTRAINT fk_admin_action_verifications_session FOREIGN KEY (session_id) REFERENCES user_sessions(id),
  CONSTRAINT chk_admin_action_verifications_target CHECK (
    (target_kind = 1 AND target_guid IS NULL) OR
    (target_kind IN (2, 3) AND target_guid IS NOT NULL)
  ),
  CONSTRAINT chk_admin_action_verifications_times CHECK (
    expires_at >= 0 AND (consumed_at IS NULL OR consumed_at >= 0)
  ),
  CONSTRAINT chk_admin_action_verifications_audit CHECK (
    actor_auth_version >= 0 AND created_at >= 0 AND updated_at >= 0 AND is_deleted IN (0, 1) AND
    (consumed_at IS NULL OR is_deleted = 1)
  )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS admin_operations (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  guid BIGINT NOT NULL,
  actor_user_id BIGINT NOT NULL,
  actor_auth_version INT NOT NULL,
  session_id BIGINT NOT NULL,
  action INT NOT NULL,
  idempotency_key_hmac CHAR(64) NOT NULL,
  request_hmac CHAR(64) NOT NULL,
  verification_id BIGINT NULL,
  state INT NOT NULL,
  public_ref CHAR(46) NOT NULL,
  lease_owner_hmac CHAR(64) NULL,
  lease_expires_at BIGINT NULL,
  finished_at BIGINT NULL,
  query_expires_at BIGINT NOT NULL,
  error_code INT NULL,
  result_kind INT NULL,
  result_guid BIGINT NULL,
  result_http_status INT NULL,
  created_at BIGINT NOT NULL,
  created_by BIGINT NULL,
  updated_at BIGINT NOT NULL,
  updated_by BIGINT NULL,
  is_deleted INT NOT NULL DEFAULT 0,
  UNIQUE KEY uk_admin_operations_guid (guid),
  UNIQUE KEY uk_admin_operations_public_ref (public_ref),
  UNIQUE KEY uk_admin_operations_actor_action_key (actor_user_id, action, idempotency_key_hmac),
  UNIQUE KEY uk_admin_operations_verification (verification_id),
  KEY idx_admin_operations_state_session (state, session_id, is_deleted, lease_expires_at),
  KEY idx_admin_operations_recovery (state, is_deleted, lease_expires_at),
  KEY idx_admin_operations_expiry (is_deleted, query_expires_at),
  KEY fk_admin_operations_session (session_id),
  CONSTRAINT fk_admin_operations_actor FOREIGN KEY (actor_user_id) REFERENCES users(id),
  CONSTRAINT fk_admin_operations_session FOREIGN KEY (session_id) REFERENCES user_sessions(id),
  CONSTRAINT fk_admin_operations_verification FOREIGN KEY (verification_id) REFERENCES admin_action_verifications(id),
  CONSTRAINT chk_admin_operations_state CHECK (state IN (1, 2, 3, 4, 5)),
  CONSTRAINT chk_admin_operations_lease CHECK (
    (lease_owner_hmac IS NULL AND lease_expires_at IS NULL) OR
    (lease_owner_hmac IS NOT NULL AND lease_expires_at IS NOT NULL AND lease_expires_at >= 0)
  ),
  CONSTRAINT chk_admin_operations_result CHECK (
    (result_kind IS NULL AND result_guid IS NULL) OR
    (result_kind = 1 AND result_guid IS NULL) OR
    (result_kind IN (2, 3) AND result_guid IS NOT NULL)
  ),
  CONSTRAINT chk_admin_operations_terminal CHECK (
    (state = 1 AND lease_owner_hmac IS NOT NULL AND finished_at IS NULL AND error_code IS NULL AND result_kind IS NULL AND result_http_status IS NULL) OR
    (state = 2 AND lease_owner_hmac IS NULL AND finished_at IS NOT NULL AND error_code IS NULL AND result_kind IS NOT NULL AND result_http_status IS NOT NULL) OR
    (state = 3 AND lease_owner_hmac IS NULL AND finished_at IS NOT NULL AND error_code IS NOT NULL AND result_kind IS NULL AND result_http_status IS NOT NULL) OR
    (state = 4 AND lease_owner_hmac IS NULL AND finished_at IS NULL AND error_code IS NULL AND result_kind IS NULL AND result_http_status IS NULL) OR
    (state = 5 AND lease_owner_hmac IS NULL AND error_code IS NULL AND result_kind IS NULL AND result_http_status IS NULL)
  ),
  CONSTRAINT chk_admin_operations_failure CHECK (error_code IS NULL OR error_code BETWEEN 1 AND 999),
  CONSTRAINT chk_admin_operations_http_status CHECK (result_http_status IS NULL OR result_http_status BETWEEN 100 AND 599),
  CONSTRAINT chk_admin_operations_times CHECK (
    query_expires_at >= 0 AND (finished_at IS NULL OR finished_at >= 0)
  ),
  CONSTRAINT chk_admin_operations_audit CHECK (
    actor_auth_version >= 0 AND created_at >= 0 AND updated_at >= 0 AND
    ((state = 5 AND is_deleted = 1) OR (state IN (1, 2, 3, 4) AND is_deleted = 0))
  )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
