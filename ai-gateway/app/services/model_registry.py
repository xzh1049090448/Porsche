"""Load and hot-reload YAML model routing configuration."""

from __future__ import annotations

import asyncio
import os
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import yaml
from loguru import logger

from app.config import Settings


@dataclass(frozen=True)
class ModelRoute:
    """Maps a logical client-facing model name to an upstream provider."""

    logical_name: str
    provider: str
    upstream_model: str
    base_url: str | None
    api_keys_env: str


class ModelRegistry:
    """Thread-safe model route registry with file-based hot reload."""

    def __init__(self, config_path: str) -> None:
        self._config_path = Path(config_path)
        self._lock = asyncio.Lock()
        self._routes: dict[str, ModelRoute] = {}

    @property
    def routes(self) -> dict[str, ModelRoute]:
        return self._routes

    def load_sync(self) -> None:
        """Load routes from disk (blocking); call from threadpool or startup."""
        if not self._config_path.is_file():
            logger.warning("Model config not found: {}", self._config_path)
            self._routes = {}
            return
        raw = yaml.safe_load(self._config_path.read_text(encoding="utf-8")) or {}
        routes_raw: dict[str, Any] = raw.get("routes") or {}
        loaded: dict[str, ModelRoute] = {}
        for logical, cfg in routes_raw.items():
            logical_name = str(logical).strip()
            if not logical_name:
                continue
            if not isinstance(cfg, dict):
                continue
            provider = str(cfg.get("provider", "")).strip()
            upstream_model = str(cfg.get("upstream_model", "")).strip()
            api_keys_env = str(cfg.get("api_keys_env", "")).strip()
            base_url = cfg.get("base_url")
            base_url_s = str(base_url).strip() if base_url else None
            if not provider or not upstream_model or not api_keys_env:
                logger.warning("Skipping invalid route for model {}", logical)
                continue
            loaded[logical_name] = ModelRoute(
                logical_name=logical_name,
                provider=provider,
                upstream_model=upstream_model,
                base_url=base_url_s,
                api_keys_env=api_keys_env,
            )
        self._routes = loaded
        logger.info("Loaded {} model routes from {}", len(loaded), self._config_path)

    async def reload(self) -> None:
        async with self._lock:
            await asyncio.to_thread(self.load_sync)

    def get(self, logical_model: str) -> ModelRoute | None:
        return self._routes.get(logical_model)


# models.yaml api_keys_env -> Settings 字段（与 .env 变量名一致）
_API_KEYS_ENV_TO_SETTINGS: dict[str, str] = {
    "OPENAI_API_KEYS": "openai_api_keys",
    "ANTHROPIC_API_KEYS": "anthropic_api_keys",
    "GOOGLE_API_KEYS": "google_api_keys",
    "MISTRAL_API_KEYS": "mistral_api_keys",
    "QWEN_API_KEYS": "qwen_api_keys",
    "ERNIE_API_KEYS": "ernie_api_keys",
    "HUNYUAN_API_KEYS": "hunyuan_api_keys",
    "DOUBAO_API_KEYS": "doubao_api_keys",
    "DEEPSEEK_API_KEYS": "deepseek_api_keys",
    "GLM_API_KEYS": "glm_api_keys",
    "MOONSHOT_API_KEYS": "moonshot_api_keys",
    "YI_API_KEYS": "yi_api_keys",
}


def read_keys_from_env(env_name: str, settings: Settings | None = None) -> list[str]:
    """Read comma/newline separated API keys from env or Settings (.env)."""
    val = os.environ.get(env_name, "") or ""
    if not val.strip() and settings is not None:
        attr = _API_KEYS_ENV_TO_SETTINGS.get(env_name)
        if attr:
            fallback = getattr(settings, attr, None)
            if fallback:
                val = str(fallback)
    parts = []
    for chunk in val.replace("\n", ",").split(","):
        s = chunk.strip()
        if s:
            parts.append(s)
    return parts
