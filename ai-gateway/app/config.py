"""Application settings loaded from environment variables (never hard-code secrets)."""

from __future__ import annotations

import os
from functools import lru_cache
from typing import Literal

from pydantic import Field, model_validator
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    """Runtime configuration. Sensitive values must come from env or .env file."""

    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
    )

    app_env: Literal["development", "staging", "production"] = "development"
    host: str = "0.0.0.0"
    port: int = 8000

    admin_token: str = Field(
        default="change-me-for-dev-only",
        description="Bearer token for /admin/*; override in production via ADMIN_TOKEN.",
    )

    models_config_path: str = "config/models.yaml"
    clients_config_path: str = "config/clients.yaml"

    redis_url: str | None = None

    log_level: str = "INFO"

    upstream_timeout_seconds: float = 120.0
    circuit_failure_threshold: int = 5
    circuit_open_seconds: int = 60

    # Comma or newline separated upstream keys (overrides per-route env if set globally)
    openai_api_keys: str | None = None
    anthropic_api_keys: str | None = None
    google_api_keys: str | None = None
    mistral_api_keys: str | None = None

    # 国内大模型 API 密钥
    qwen_api_keys: str | None = None
    ernie_api_keys: str | None = None
    hunyuan_api_keys: str | None = None
    doubao_api_keys: str | None = None
    deepseek_api_keys: str | None = None
    glm_api_keys: str | None = None
    moonshot_api_keys: str | None = None
    yi_api_keys: str | None = None

    # Platform database
    database_url: str = Field(
        default="sqlite+aiosqlite:///./data/platform.db",
        description="Async SQLAlchemy URL (MySQL: mysql+aiomysql://user:pass@host/db)",
    )

    # JWT auth
    jwt_secret_key: str = Field(
        default="change-me-jwt-secret-for-dev-only",
        description="JWT signing secret; override via JWT_SECRET_KEY in production.",
    )
    jwt_expire_minutes: int = 60 * 24 * 7  # 7 days

    # SMS verification (dev mode returns code in response)
    sms_dev_mode: bool = True
    sms_send_limit_per_phone: int = Field(
        default=5,
        description="Max send-code requests per phone per hour.",
    )
    sms_send_limit_per_ip: int = Field(
        default=20,
        description="Max send-code requests per client IP per hour.",
    )
    sms_verify_max_attempts: int = Field(
        default=5,
        description="Max failed verify attempts per phone before lockout.",
    )

    # Billing: mock pay endpoint (must be false in production)
    billing_allow_mock_payment: bool = False

    # Metrics: when set, require Authorization: Bearer <token> on /metrics
    metrics_token: str | None = None

    # Only trust X-Forwarded-For when behind a trusted reverse proxy
    trust_proxy_headers: bool = False

    # Real-name: auto-verify after format check (disable in production)
    real_name_auto_verify: bool = True

    # Dataset uploads
    dataset_upload_max_bytes: int = 50 * 1024 * 1024

    # Chroma vector store
    chroma_persist_dir: str = "./data/chroma"
    rag_top_k: int = 5
    rag_chunk_size: int = 512
    rag_chunk_overlap: int = 64

    # Dataset file storage
    dataset_upload_dir: str = "./data/uploads"

    # Platform internal client key for gateway routing
    platform_client_secret: str = Field(
        default="sk-platform-internal",
        description="Internal client secret used by platform chat to call gateway.",
    )

    # Plan pricing (CNY)
    plan_professional_price: float = 99.0
    plan_enterprise_price: float = 999.0

    @model_validator(mode="after")
    def _development_defaults(self) -> Settings:
        """Enable safe local-dev defaults only when not explicitly set in environment."""
        if self.app_env == "development":
            if "BILLING_ALLOW_MOCK_PAYMENT" not in os.environ:
                self.billing_allow_mock_payment = True
        return self


@lru_cache
def get_settings() -> Settings:
    """Cached settings singleton."""
    return Settings()


def clear_settings_cache() -> None:
    """Clear settings cache (e.g. after tests)."""
    get_settings.cache_clear()
