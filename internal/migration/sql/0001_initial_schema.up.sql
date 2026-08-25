CREATE TABLE IF NOT EXISTS users (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  guid BIGINT NOT NULL,
  phone VARCHAR(20) NOT NULL,
  password_hash VARCHAR(255) NULL,
  nickname VARCHAR(64) NULL,
  real_name VARCHAR(64) NULL,
  id_card_hash VARCHAR(128) NULL,
  is_verified TINYINT(1) NOT NULL DEFAULT 0,
  plan_type INT NOT NULL DEFAULT 1,
  status INT NOT NULL DEFAULT 1,
  allowed_models JSON NOT NULL,
  daily_call_limit INT NOT NULL DEFAULT 100,
  daily_calls_used INT NOT NULL DEFAULT 0,
  daily_calls_reset_at BIGINT NULL,
  total_tokens_used BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL,
  created_by BIGINT NULL,
  updated_at BIGINT NOT NULL,
  updated_by BIGINT NULL,
  is_deleted INT NOT NULL DEFAULT 0,
  UNIQUE KEY uk_users_guid (guid),
  UNIQUE KEY uk_users_phone_active (phone, is_deleted),
  KEY idx_users_active_updated (is_deleted, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS conversations (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  guid BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  title VARCHAR(256) NOT NULL DEFAULT '新对话',
  model VARCHAR(128) NULL,
  created_at BIGINT NOT NULL,
  created_by BIGINT NULL,
  updated_at BIGINT NOT NULL,
  updated_by BIGINT NULL,
  is_deleted INT NOT NULL DEFAULT 0,
  UNIQUE KEY uk_conversations_guid (guid),
  KEY idx_conversations_user_active_updated (user_id, is_deleted, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS messages (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  guid BIGINT NOT NULL,
  conversation_id BIGINT NOT NULL,
  role INT NOT NULL,
  content TEXT NOT NULL,
  model VARCHAR(128) NULL,
  tokens INT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL,
  created_by BIGINT NULL,
  updated_at BIGINT NOT NULL,
  updated_by BIGINT NULL,
  is_deleted INT NOT NULL DEFAULT 0,
  UNIQUE KEY uk_messages_guid (guid),
  KEY idx_messages_conversation_active_created (conversation_id, is_deleted, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS usage_records (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  guid BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  record_type INT NOT NULL,
  tokens INT NOT NULL DEFAULT 0,
  model VARCHAR(128) NULL,
  created_at BIGINT NOT NULL,
  created_by BIGINT NULL,
  updated_at BIGINT NOT NULL,
  updated_by BIGINT NULL,
  is_deleted INT NOT NULL DEFAULT 0,
  UNIQUE KEY uk_usage_records_guid (guid),
  KEY idx_usage_records_user_active_created (user_id, is_deleted, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS orders (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  guid BIGINT NOT NULL,
  order_no VARCHAR(64) NOT NULL,
  user_id BIGINT NOT NULL,
  plan_type INT NOT NULL,
  amount DECIMAL(12,2) NOT NULL DEFAULT 0,
  status INT NOT NULL DEFAULT 1,
  invoice_requested TINYINT(1) NOT NULL DEFAULT 0,
  paid_at BIGINT NULL,
  created_at BIGINT NOT NULL,
  created_by BIGINT NULL,
  updated_at BIGINT NOT NULL,
  updated_by BIGINT NULL,
  is_deleted INT NOT NULL DEFAULT 0,
  UNIQUE KEY uk_orders_guid (guid),
  UNIQUE KEY uk_orders_order_no_active (order_no, is_deleted),
  KEY idx_orders_user_active_created (user_id, is_deleted, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS audit_logs (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  guid BIGINT NOT NULL,
  user_id BIGINT NULL,
  action VARCHAR(64) NOT NULL,
  resource VARCHAR(128) NULL,
  detail JSON NOT NULL,
  ip VARCHAR(64) NULL,
  created_at BIGINT NOT NULL,
  created_by BIGINT NULL,
  updated_at BIGINT NOT NULL,
  updated_by BIGINT NULL,
  is_deleted INT NOT NULL DEFAULT 0,
  UNIQUE KEY uk_audit_logs_guid (guid),
  KEY idx_audit_logs_user_active_created (user_id, is_deleted, created_at),
  KEY idx_audit_logs_action_active_created (action, is_deleted, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS model_health (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  guid BIGINT NOT NULL,
  model_name VARCHAR(128) NOT NULL,
  provider VARCHAR(64) NOT NULL,
  is_available TINYINT(1) NOT NULL DEFAULT 1,
  avg_latency_ms DOUBLE NOT NULL DEFAULT 0,
  error_rate DOUBLE NOT NULL DEFAULT 0,
  last_checked_at BIGINT NULL,
  created_at BIGINT NOT NULL,
  created_by BIGINT NULL,
  updated_at BIGINT NOT NULL,
  updated_by BIGINT NULL,
  is_deleted INT NOT NULL DEFAULT 0,
  UNIQUE KEY uk_model_health_guid (guid),
  UNIQUE KEY uk_model_health_name_active (model_name, is_deleted),
  KEY idx_model_health_active_updated (is_deleted, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS gateway_api_tokens (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  guid BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  name VARCHAR(128) NOT NULL,
  token_hash VARCHAR(64) NOT NULL,
  token_prefix VARCHAR(24) NOT NULL,
  status INT NOT NULL DEFAULT 1,
  allowed_models JSON NOT NULL,
  ip_allowlist JSON NOT NULL,
  expires_at BIGINT NULL,
  last_used_at BIGINT NULL,
  created_at BIGINT NOT NULL,
  created_by BIGINT NULL,
  updated_at BIGINT NOT NULL,
  updated_by BIGINT NULL,
  is_deleted INT NOT NULL DEFAULT 0,
  UNIQUE KEY uk_gateway_api_tokens_guid (guid),
  UNIQUE KEY uk_gateway_api_tokens_token_hash (token_hash),
  KEY idx_gateway_api_tokens_user_active (user_id, is_deleted),
  KEY idx_gateway_api_tokens_prefix_active (token_prefix, is_deleted),
  KEY idx_gateway_api_tokens_status_active (status, is_deleted),
  KEY idx_gateway_api_tokens_expiry_active (expires_at, is_deleted)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
