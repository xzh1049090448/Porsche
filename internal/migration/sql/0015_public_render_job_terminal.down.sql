ALTER TABLE public_render_jobs
  DROP CHECK chk_public_render_jobs_terminal,
  DROP COLUMN last_terminal_state,
  DROP COLUMN last_terminal_operation,
  DROP COLUMN last_terminal_fence,
  DROP COLUMN last_terminal_owner_hmac;
