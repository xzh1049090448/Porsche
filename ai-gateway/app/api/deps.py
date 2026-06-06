"""FastAPI dependencies."""

from __future__ import annotations

from typing import Annotated

from fastapi import Depends, Header, HTTPException, Request
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app.core.security import decode_access_token
from app.db.enum_utils import enum_is
from app.db.models import User, UserStatus
from app.db.session import get_db
from app.services.client_registry import ClientConfig, ClientRegistry
from app.services.model_analytics_service import is_analytics_admin
from app.state import AppState


def get_state(request: Request) -> AppState:
    state = getattr(request.app.state, "app_state", None)
    if state is None:
        raise HTTPException(status_code=503, detail="Application not ready")
    return state  # type: ignore[return-value]


async def get_client_config(
    request: Request,
    state: Annotated[AppState, Depends(get_state)],
    authorization: Annotated[str | None, Header()] = None,
) -> ClientConfig:
    """Resolve downstream client from Bearer token."""
    if not authorization or not authorization.lower().startswith("bearer "):
        raise HTTPException(status_code=401, detail="Missing or invalid Authorization header")
    secret = authorization.split(" ", 1)[1].strip()
    client = state.clients.get_by_secret(secret)
    if client is None:
        raise HTTPException(status_code=401, detail="Invalid API key")
    host = _resolve_client_host(request, state.settings.trust_proxy_headers)
    if not ClientRegistry.ip_allowed(client, host):
        raise HTTPException(status_code=403, detail="IP not allowed")
    return client


def _decode_user_id_from_header(
    state: AppState, authorization: str | None
) -> int:
    if not authorization or not authorization.lower().startswith("bearer "):
        raise HTTPException(status_code=401, detail="未登录")
    token = authorization.split(" ", 1)[1].strip()
    payload = decode_access_token(token, state.settings.jwt_secret_key)
    if not payload or "sub" not in payload:
        raise HTTPException(status_code=401, detail="Token无效或已过期")
    return int(payload["sub"])


async def require_authenticated_user(
    state: Annotated[AppState, Depends(get_state)],
    authorization: Annotated[str | None, Header()] = None,
) -> int:
    """Validate JWT only (no DB). Use for read-only endpoints to reduce pool pressure."""
    return _decode_user_id_from_header(state, authorization)


async def get_current_user(
    state: Annotated[AppState, Depends(get_state)],
    db: Annotated[AsyncSession, Depends(get_db)],
    authorization: Annotated[str | None, Header()] = None,
) -> User:
    user_id = _decode_user_id_from_header(state, authorization)
    user = await db.scalar(select(User).where(User.id == user_id))
    if not user or not enum_is(user.status, UserStatus.ACTIVE):
        raise HTTPException(status_code=401, detail="用户不存在或已被禁用")
    return user


async def require_analytics_admin(
    user: Annotated[User, Depends(get_current_user)],
    state: Annotated[AppState, Depends(get_state)],
) -> User:
    """Only phones listed in ``analytics_admin_phones`` may access analytics."""
    if not is_analytics_admin(user.phone, state.settings):
        raise HTTPException(status_code=403, detail="无分析权限")
    return user


def _resolve_client_host(request: Request, trust_proxy_headers: bool) -> str:
    if trust_proxy_headers:
        forwarded = request.headers.get("x-forwarded-for")
        if forwarded:
            return forwarded.split(",")[0].strip()
    return request.client.host if request.client else ""


def get_client_ip(request: Request) -> str:
    state = getattr(request.app.state, "app_state", None)
    trust = state.settings.trust_proxy_headers if state is not None else False
    return _resolve_client_host(request, trust)
