ALTER TABLE public_price_snapshot_items
  MODIFY COLUMN input_price_usd_per_million_tokens DECIMAL(20,8) NULL,
  MODIFY COLUMN output_price_usd_per_million_tokens DECIMAL(20,8) NULL;
