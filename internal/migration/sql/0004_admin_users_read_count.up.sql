CREATE INDEX idx_users_admin_read_count
  ON users (is_deleted, role, status)
  ALGORITHM=INPLACE LOCK=NONE;
