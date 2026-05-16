"""FastAPI application entrypoint."""

from __future__ import annotations

from contextlib import asynccontextmanager

from fastapi import FastAPI
from fastapi.responses import Response
from prometheus_client import CONTENT_TYPE_LATEST, generate_latest

from app.api.routes import admin as admin_routes
from app.api.routes import chat as chat_routes
from app.api.routes import health as health_routes
from app.config import clear_settings_cache, get_settings
from app.core.logging_config import setup_logging
from app.state import AppState


@asynccontextmanager
async def lifespan(app: FastAPI):
    setup_logging()
    settings = get_settings()
    state = AppState(settings)
    state.models.load_sync()
    state.clients.load_sync()
    state.rebuild_upstream_pool()
    await state.rate_limiter.connect()
    await state.usage_tracker.connect()
    app.state.app_state = state
    yield
    await state.shutdown()
    clear_settings_cache()


def create_app() -> FastAPI:
    settings = get_settings()
    app = FastAPI(
        title="AI API Gateway",
        version="0.1.0",
        lifespan=lifespan,
        docs_url="/docs" if settings.app_env != "production" else None,
        redoc_url="/redoc" if settings.app_env != "production" else None,
    )

    app.include_router(health_routes.router)
    app.include_router(chat_routes.router)
    app.include_router(admin_routes.router)

    @app.get("/metrics")
    async def metrics_endpoint() -> Response:
        data = generate_latest()
        return Response(content=data, media_type=CONTENT_TYPE_LATEST)

    return app


app = create_app()
