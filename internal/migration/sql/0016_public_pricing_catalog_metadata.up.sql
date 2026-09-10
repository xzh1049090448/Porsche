ALTER TABLE public_model_configs
  ADD COLUMN public_display_group VARCHAR(128) NOT NULL DEFAULT '' AFTER ever_published,
  ADD COLUMN endpoint_types JSON NOT NULL DEFAULT (JSON_ARRAY()) AFTER public_display_group,
  ADD COLUMN public_restrictions JSON NOT NULL DEFAULT (JSON_ARRAY()) AFTER endpoint_types,
  ADD COLUMN price_source VARCHAR(255) NOT NULL DEFAULT '' AFTER public_restrictions,
  ADD COLUMN price_reviewer VARCHAR(128) NOT NULL DEFAULT '' AFTER price_source,
  ADD COLUMN price_effective_at BIGINT NULL AFTER price_reviewer;


ALTER TABLE public_price_snapshot_items
  ADD COLUMN pricing_type VARCHAR(32) NOT NULL DEFAULT 'token' AFTER upstream_checked_at,
  ADD COLUMN public_display_group VARCHAR(128) NOT NULL DEFAULT '' AFTER pricing_type,
  ADD COLUMN endpoint_types JSON NOT NULL DEFAULT (JSON_ARRAY()) AFTER public_display_group,
  ADD COLUMN public_restrictions JSON NOT NULL DEFAULT (JSON_ARRAY()) AFTER endpoint_types,
  ADD COLUMN price_source VARCHAR(255) NOT NULL DEFAULT '' AFTER public_restrictions,
  ADD COLUMN price_reviewer VARCHAR(128) NOT NULL DEFAULT '' AFTER price_source,
  ADD COLUMN effective_at BIGINT NULL AFTER price_reviewer;

