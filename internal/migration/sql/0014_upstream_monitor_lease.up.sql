CREATE TABLE IF NOT EXISTS upstream_monitor_leases (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  guid BIGINT NOT NULL,
  lease_key VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  owner_token CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  lease_expires_at BIGINT NOT NULL DEFAULT 0,
  revision BIGINT NOT NULL DEFAULT 1,
  created_at BIGINT NOT NULL,
  created_by BIGINT NULL,
  updated_at BIGINT NOT NULL,
  updated_by BIGINT NULL,
  is_deleted INT NOT NULL DEFAULT 0,
  UNIQUE KEY uk_upstream_monitor_leases_guid (guid),
  UNIQUE KEY uk_upstream_monitor_leases_key (lease_key),
  KEY idx_upstream_monitor_leases_expiry (lease_expires_at, is_deleted),
  CONSTRAINT chk_upstream_monitor_leases_values CHECK (lease_expires_at >= 0 AND revision > 0 AND is_deleted IN (0, 1))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- porsche:seed-upstream-monitor-lease
