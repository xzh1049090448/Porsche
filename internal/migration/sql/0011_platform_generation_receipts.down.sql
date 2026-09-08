-- Disposable local/test rollback only. Production migrations remain forward-only.
DROP TABLE IF EXISTS platform_chat_generation_results;
DROP TABLE IF EXISTS platform_chat_generation_receipts;
