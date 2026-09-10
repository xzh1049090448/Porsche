-- Disposable local/test rollback only. Production migrations remain forward-only.
-- The single ALTER is atomic and deliberately fails before changing either column
-- when any snapshot contains NULL; missing prices must never be coerced to zero.
ALTER TABLE public_price_snapshot_items
  MODIFY COLUMN input_price_usd_per_million_tokens DECIMAL(20,8) NOT NULL,
  MODIFY COLUMN output_price_usd_per_million_tokens DECIMAL(20,8) NOT NULL;
