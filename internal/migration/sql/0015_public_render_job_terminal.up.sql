ALTER TABLE public_render_jobs
  ADD COLUMN last_terminal_owner_hmac CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER completed_at,
  ADD COLUMN last_terminal_fence INT NULL AFTER last_terminal_owner_hmac,
  ADD COLUMN last_terminal_operation INT NULL AFTER last_terminal_fence,
  ADD COLUMN last_terminal_state INT NULL AFTER last_terminal_operation,
  ADD CONSTRAINT chk_public_render_jobs_terminal CHECK (
    (last_terminal_owner_hmac IS NULL AND last_terminal_fence IS NULL AND last_terminal_operation IS NULL AND last_terminal_state IS NULL)
    OR (last_terminal_owner_hmac IS NOT NULL AND last_terminal_fence > 0 AND last_terminal_operation IN (1,2) AND last_terminal_state IN (1,3,4))
  );
