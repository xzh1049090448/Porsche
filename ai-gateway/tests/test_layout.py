"""Verify ai-gateway follows project module layering conventions."""

from __future__ import annotations

from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[1]
APP = ROOT / "app"


@pytest.mark.parametrize(
    "relative",
    [
        "api/routes",
        "common/schemas",
        "common/constants",
        "service",
        "repository",
        "task",
        "tool",
    ],
)
def test_required_modules_exist(relative: str) -> None:
    assert (APP / relative).is_dir(), f"missing module directory: app/{relative}"


def test_legacy_packages_removed() -> None:
    for legacy in ("schemas", "services", "db", "core", "constants"):
        assert not (APP / legacy).exists(), f"legacy package still exists: app/{legacy}"


def test_repository_exports_db_session() -> None:
    from app.repository import Base, get_db, init_db

    assert Base is not None
    assert callable(get_db)
    assert callable(init_db)


def test_common_schemas_importable() -> None:
    from app.common.schemas.auth import TokenResponse

    assert TokenResponse is not None


def test_service_layer_importable() -> None:
    from app.service.gateway import GatewayService

    assert GatewayService is not None
