"""Application runtime state (registries, HTTP client, limiters)."""

from __future__ import annotations

from typing import TYPE_CHECKING

import httpx

from app.config import Settings
from app.services.auth_service import AuthService
from app.services.billing_service import BillingService
from app.services.client_registry import ClientRegistry
from app.services.model_registry import ModelRegistry
from app.services.rag_engine import RagEngine
from app.services.rate_limiter import RateLimiter
from app.services.sms import SmsService
from app.services.upstream_pool import UpstreamKeyPool
from app.services.usage_tracker import UsageTracker

if TYPE_CHECKING:
    from app.services.platform_chat import PlatformChatService


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
        self.sms = SmsService(settings.redis_url, settings)
        self.rag = RagEngine(
            persist_dir=settings.chroma_persist_dir,
            chunk_size=settings.rag_chunk_size,
            chunk_overlap=settings.rag_chunk_overlap,
            top_k=settings.rag_top_k,
        )
        self.billing = BillingService(settings)
        self.auth = AuthService(
            self.sms,
            settings.jwt_secret_key,
            settings.jwt_expire_minutes,
            fixed_login_enabled=settings.fixed_login_enabled,
            fixed_login_phone=settings.fixed_login_phone,
            fixed_login_password=settings.fixed_login_password,
        )
        limits = httpx.Limits(max_connections=200, max_keepalive_connections=50)
        self.http = httpx.AsyncClient(http2=True, limits=limits)
        self.platform_chat: PlatformChatService | None = None

    def rebuild_upstream_pool(self) -> None:
        self.pool.rebuild(self.models.routes, self.settings)

    def init_platform_chat(self) -> None:
        from app.services.platform_chat import PlatformChatService

        self.platform_chat = PlatformChatService(self, self.rag, self.billing)

    async def shutdown(self) -> None:
        await self.rate_limiter.close()
        await self.usage_tracker.close()
        await self.sms.close()
        await self.http.aclose()
