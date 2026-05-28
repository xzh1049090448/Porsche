import os
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[1]
os.chdir(ROOT)

os.environ.setdefault("ADMIN_TOKEN", "test-admin-token")
os.environ.setdefault("APP_ENV", "development")
os.environ.setdefault("MODELS_CONFIG_PATH", "config/models.yaml")
os.environ.setdefault("CLIENTS_CONFIG_PATH", "config/clients.test.yaml")
os.environ.setdefault("BILLING_ALLOW_MOCK_PAYMENT", "true")
os.environ.setdefault("REDIS_URL", "")
os.environ.setdefault("DATABASE_URL", "sqlite+aiosqlite:///./data/test_platform.db")
os.environ.setdefault("JWT_SECRET_KEY", "test-jwt-secret")
os.environ.setdefault("CHROMA_PERSIST_DIR", "./data/test_chroma")
os.environ.setdefault("DATASET_UPLOAD_DIR", "./data/test_uploads")
os.environ.setdefault("SMS_DEV_MODE", "true")
os.environ.setdefault("FIXED_LOGIN_ENABLED", "false")


@pytest.fixture(scope="session", autouse=True)
def _session_env():
    yield
