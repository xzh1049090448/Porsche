"""Validate security-sensitive settings before serving traffic."""

from __future__ import annotations

from loguru import logger

from app.config import Settings
from app.state import AppState

_INSECURE_DEFAULTS = frozenset(
    {
        "change-me-for-dev-only",
        "change-me-jwt-secret-for-dev-only",
        "sk-platform-internal",
        "sk-client-dev-change-me",
    }
)


def validate_settings(settings: Settings) -> None:
    """Raise on unsafe production/staging configuration."""
    if settings.app_env not in ("production", "staging"):
        return

    errors: list[str] = []
    if settings.sms_dev_mode:
        errors.append("SMS_DEV_MODE must be false in production/staging")
    if settings.billing_allow_mock_payment:
        errors.append("BILLING_ALLOW_MOCK_PAYMENT must be false in production/staging")
    if settings.admin_token in _INSECURE_DEFAULTS:
        errors.append("ADMIN_TOKEN must be set to a strong random value")
    if settings.jwt_secret_key in _INSECURE_DEFAULTS:
        errors.append("JWT_SECRET_KEY must be set to a strong random value")
    if settings.platform_client_secret in _INSECURE_DEFAULTS:
        errors.append("PLATFORM_CLIENT_SECRET must be set to a strong random value")
    if settings.real_name_auto_verify:
        errors.append("REAL_NAME_AUTO_VERIFY must be false in production (use a KYC provider)")

    if errors:
        raise RuntimeError("Unsafe production configuration:\n- " + "\n- ".join(errors))


def verify_platform_client_config(state: AppState) -> None:
    """Ensure PLATFORM_CLIENT_SECRET matches a client in clients.yaml."""
    secret = state.settings.platform_client_secret
    client = state.clients.get_by_secret(secret)
    if client is not None:
        return

    path = state.settings.clients_config_path
    msg = (
        f"Platform internal client not configured: PLATFORM_CLIENT_SECRET does not match "
        f"any client secret in {path}. Add a client named platform-internal with the same "
        f"secret, then POST /admin/reload-config or restart the gateway."
    )
    if state.settings.app_env in ("production", "staging"):
        raise RuntimeError(msg)
    logger.warning(msg)
