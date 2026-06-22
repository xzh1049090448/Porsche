"""Async SQLAlchemy session management."""

from __future__ import annotations

from collections.abc import AsyncGenerator

from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker, create_async_engine

from app.config import get_settings
from app.repository.models import Base

_engine = None
async_session_factory: async_sessionmaker[AsyncSession] | None = None


def _build_engine():
    settings = get_settings()
    url = settings.database_url
    # aiomysql/asyncmy: pool_pre_ping triggers ping() without `reconnect` (SQLAlchemy #13306)
    pool_pre_ping = "aiomysql" not in url and "asyncmy" not in url
    return create_async_engine(
        url,
        echo=settings.app_env == "development",
        pool_pre_ping=pool_pre_ping,
        pool_size=10,
        max_overflow=20,
        pool_recycle=1800,
        pool_timeout=30,
    )


async def init_db() -> None:
    global _engine, async_session_factory
    _engine = _build_engine()
    async_session_factory = async_sessionmaker(_engine, expire_on_commit=False)
    async with _engine.begin() as conn:
        await conn.run_sync(Base.metadata.create_all)


async def get_db() -> AsyncGenerator[AsyncSession, None]:
    if async_session_factory is None:
        raise RuntimeError("Database not initialized")
    async with async_session_factory() as session:
        try:
            yield session
            await session.commit()
        except Exception:
            await session.rollback()
            raise


async def shutdown_db() -> None:
    global _engine, async_session_factory
    if _engine is not None:
        await _engine.dispose()
    _engine = None
    async_session_factory = None
