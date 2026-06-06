"""FastAPI application entrypoint."""

from __future__ import annotations

from contextlib import asynccontextmanager
from pathlib import Path

import secrets
from typing import Annotated

from fastapi import Depends, FastAPI, Header, HTTPException
from fastapi.responses import Response
from prometheus_client import CONTENT_TYPE_LATEST, generate_latest

from app.api.routes import admin as admin_routes
from app.api.routes import admin_dashboard as admin_dashboard_routes
from app.api.routes import admin_datasets as admin_datasets_routes
from app.api.routes import admin_logs as admin_logs_routes
from app.api.routes import admin_users as admin_users_routes
from app.api.routes import auth as auth_routes
from app.api.routes import billing as billing_routes
from app.api.routes import model_analytics as model_analytics_routes
from app.api.routes import chat as chat_routes
from app.api.routes import conversations as conversations_routes
from app.api.routes import datasets as datasets_routes
from app.api.routes import health as health_routes
from app.api.routes import platform_chat as platform_chat_routes
from app.api.routes import users as users_routes
from app.config import clear_settings_cache, get_settings
from app.core.logging_config import setup_logging
from app.core.startup_checks import validate_settings, verify_platform_client_config
from app.db.session import async_session_factory, init_db, shutdown_db
from app.services.seed import seed_default_datasets
from app.state import AppState


@asynccontextmanager
async def lifespan(app: FastAPI):
    setup_logging()
    settings = get_settings()
    validate_settings(settings)

    Path(settings.chroma_persist_dir).mkdir(parents=True, exist_ok=True)
    Path(settings.dataset_upload_dir).mkdir(parents=True, exist_ok=True)
    Path("./data").mkdir(parents=True, exist_ok=True)

    await init_db()

    state = AppState(settings)
    state.models.load_sync()
    state.clients.load_sync()
    verify_platform_client_config(state)
    state.rebuild_upstream_pool()
    state.init_platform_chat()
    await state.rate_limiter.connect()
    await state.usage_tracker.connect()
    await state.sms.connect()
    app.state.app_state = state

    if async_session_factory:
        async with async_session_factory() as db:
            await seed_default_datasets(db, state.rag)
            await db.commit()

    yield

    await state.shutdown()
    await shutdown_db()
    clear_settings_cache()


def create_app() -> FastAPI:
    settings = get_settings()
    app = FastAPI(
        title="国内大模型聚合平台 API",
        version="1.0.0",
        description="国内大模型聚合平台后端服务，支持 RAG 专属数据集、用户认证、计费与管理",
        lifespan=lifespan,
        docs_url="/docs" if settings.app_env != "production" else None,
        redoc_url="/redoc" if settings.app_env != "production" else None,
    )

    app.include_router(health_routes.router)
    app.include_router(chat_routes.router)
    app.include_router(admin_routes.router)
    app.include_router(auth_routes.router)
    app.include_router(users_routes.router)
    app.include_router(platform_chat_routes.router)
    app.include_router(conversations_routes.router)
    app.include_router(datasets_routes.router)
    app.include_router(billing_routes.router)
    app.include_router(model_analytics_routes.router)
    app.include_router(admin_datasets_routes.router)
    app.include_router(admin_users_routes.router)
    app.include_router(admin_logs_routes.router)
    app.include_router(admin_dashboard_routes.router)

    def _verify_metrics_token(
        authorization: Annotated[str | None, Header()] = None,
    ) -> None:
        expected = settings.metrics_token or settings.admin_token
        if not authorization or not authorization.lower().startswith("bearer "):
            raise HTTPException(status_code=401, detail="Missing metrics Authorization")
        token = authorization.split(" ", 1)[1].strip()
        if not secrets.compare_digest(token, expected):
            raise HTTPException(status_code=403, detail="Forbidden")

    @app.get("/metrics", dependencies=[Depends(_verify_metrics_token)])
    async def metrics_endpoint() -> Response:
        """Prometheus 指标导出（需 ``METRICS_TOKEN`` 或 ``ADMIN_TOKEN`` 鉴权）。"""
        data = generate_latest()
        return Response(content=data, media_type=CONTENT_TYPE_LATEST)

    return app


app = create_app()
