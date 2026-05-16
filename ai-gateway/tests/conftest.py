import os
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[1]
os.chdir(ROOT)

os.environ.setdefault("ADMIN_TOKEN", "test-admin-token")
os.environ.setdefault("APP_ENV", "development")
os.environ.setdefault("MODELS_CONFIG_PATH", "config/models.yaml")
os.environ.setdefault("CLIENTS_CONFIG_PATH", "config/clients.yaml")
os.environ.setdefault("REDIS_URL", "")


@pytest.fixture(scope="session", autouse=True)
def _session_env():
    yield
