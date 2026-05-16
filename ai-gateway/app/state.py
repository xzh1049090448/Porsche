"""Application runtime state (registries, HTTP client, limiters)."""

from __future__ import annotations

import httpx

from app.config import Settings
from app.services.client_registry import ClientRegistry
from app.services.model_registry import ModelRegistry
from app.services.rate_limiter import RateLimiter
from app.services.upstream_pool import UpstreamKeyPool
from app.services.usage_tracker import UsageTracker


class AppState:
    """Mutable application state created in FastAPI lifespan."""

    def __init__(self, settings: Settings) -> None:
        self.settings = settings
        self.models = ModelRegistry(settings.models_config_path)
        self.clients = ClientRegistry(settings.clients_config_path)
        self.pool = UpstreamKeyPool(
            failure_threshold=settings.circuit_failure_threshold,
            open_seconds=settings.circuit_open_seconds,
        )
        self.rate_limiter = RateLimiter(settings.redis_url)
        self.usage_tracker = UsageTracker(settings.redis_url)
        limits = httpx.Limits(max_connections=200, max_keepalive_connections=50)
        self.http = httpx.AsyncClient(http2=True, limits=limits)

    def rebuild_upstream_pool(self) -> None:
        self.pool.rebuild(self.models.routes)

    async def shutdown(self) -> None:
        await self.rate_limiter.close()
        await self.usage_tracker.close()
        await self.http.aclose()
