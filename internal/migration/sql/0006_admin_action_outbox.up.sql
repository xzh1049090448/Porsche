CREATE TABLE IF NOT EXISTS admin_action_outbox (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  guid BIGINT NOT NULL,
  created_at BIGINT NOT NULL,
  created_by BIGINT NULL,
  updated_at BIGINT NOT NULL,
  updated_by BIGINT NULL,
  is_deleted INT NOT NULL DEFAULT 0,
  operation_id BIGINT NOT NULL,
  public_ref CHAR(46) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  action INT NOT NULL,
  target_kind INT NOT NULL,
  target_guid BIGINT NULL,
  state INT NOT NULL,
  result_guid BIGINT NULL,
  delivery_state INT NOT NULL DEFAULT 1,
  available_at BIGINT NOT NULL,
  delivered_at BIGINT NULL,
  attempt_count INT NOT NULL DEFAULT 0,
  UNIQUE KEY uk_admin_action_outbox_guid (guid),
  UNIQUE KEY uk_admin_action_outbox_operation (operation_id),
  UNIQUE KEY uk_admin_action_outbox_public_ref (public_ref),
  KEY idx_admin_action_outbox_delivery (delivery_state, is_deleted, available_at, id),
  KEY idx_admin_action_outbox_target (target_kind, target_guid, is_deleted, created_at),
  CONSTRAINT fk_admin_action_outbox_operation FOREIGN KEY (operation_id) REFERENCES admin_operations(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT chk_admin_action_outbox_action CHECK (action BETWEEN 1 AND 2147483647),
  CONSTRAINT chk_admin_action_outbox_target_kind CHECK (target_kind IN (1, 2, 3)),
  CONSTRAINT chk_admin_action_outbox_state CHECK (state IN (2, 3, 4)),
  CONSTRAINT chk_admin_action_outbox_delivery_state CHECK (delivery_state IN (1, 2, 3)),
  CONSTRAINT chk_admin_action_outbox_attempt_count CHECK (attempt_count BETWEEN 0 AND 2147483647),
  CONSTRAINT chk_admin_action_outbox_delivery CHECK (
    (delivery_state = 1 AND delivered_at IS NULL) OR
    (delivery_state IN (2, 3) AND delivered_at IS NOT NULL)
  ),
  CONSTRAINT chk_admin_action_outbox_times CHECK (
    available_at >= 0 AND (delivered_at IS NULL OR delivered_at >= 0)
  ),
  CONSTRAINT chk_admin_action_outbox_audit CHECK (
    created_at >= 0 AND updated_at >= 0 AND is_deleted IN (0, 1)
  )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
