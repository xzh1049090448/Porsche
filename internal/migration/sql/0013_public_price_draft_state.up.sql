CREATE TABLE IF NOT EXISTS public_price_draft_state (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  guid BIGINT NOT NULL,
  state_key VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  revision BIGINT NOT NULL DEFAULT 1,
  created_at BIGINT NOT NULL,
  created_by BIGINT NULL,
  updated_at BIGINT NOT NULL,
  updated_by BIGINT NULL,
  is_deleted INT NOT NULL DEFAULT 0,
  UNIQUE KEY uk_public_price_draft_state_guid (guid),
  UNIQUE KEY uk_public_price_draft_state_key (state_key),
  KEY idx_public_price_draft_state_revision (revision, is_deleted),
  CONSTRAINT chk_public_price_draft_state_values CHECK (revision > 0 AND is_deleted IN (0, 1))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- porsche:seed-public-price-draft-state
