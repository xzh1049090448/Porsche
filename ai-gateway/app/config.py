"""Application settings loaded from environment variables (never hard-code secrets)."""

from functools import lru_cache
from typing import Literal

from pydantic import Field
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


@lru_cache
def get_settings() -> Settings:
    """Cached settings singleton."""
    return Settings()


def clear_settings_cache() -> None:
    """Clear settings cache (e.g. after tests)."""
    get_settings.cache_clear()
