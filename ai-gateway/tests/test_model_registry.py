"""Model registry loads GLM platform routes from config/models.yaml."""

from __future__ import annotations

from pathlib import Path

from app.services.model_registry import ModelRegistry

ROOT = Path(__file__).resolve().parents[1]
MODELS_PATH = ROOT / "config" / "models.yaml"


def test_models_yaml_includes_glm_platform_routes():
    registry = ModelRegistry(str(MODELS_PATH))
    registry.load_sync()
    ids = set(registry.routes.keys())
    assert "glm-4" in ids
    assert "glm-4.7-flash" in ids
    assert "glm-5.1" in ids
    assert "glm-4-flash" in ids

    flash = registry.get("glm-4.7-flash")
    assert flash is not None
    assert flash.upstream_model == "glm-4.7-flash"

    legacy = registry.get("glm-4-flash")
    assert legacy is not None
    assert legacy.upstream_model == "glm-4.7-flash"
