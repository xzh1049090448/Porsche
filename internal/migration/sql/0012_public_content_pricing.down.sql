-- Disposable local/test rollback only. Production migrations remain forward-only.
DROP TABLE IF EXISTS root_alert_receipts;
DROP TABLE IF EXISTS public_render_jobs;
DROP TABLE IF EXISTS public_publication_state;
DROP TABLE IF EXISTS public_content_releases;
DROP TABLE IF EXISTS public_content_drafts;
DROP TABLE IF EXISTS public_price_snapshot_items;
DROP TABLE IF EXISTS public_price_snapshots;
DROP TABLE IF EXISTS root_alerts;
DROP TABLE IF EXISTS upstream_model_observations;
DROP TABLE IF EXISTS public_model_configs;
