"""FastAPI dependencies."""

from __future__ import annotations

from typing import Annotated

from fastapi import Depends, Header, HTTPException, Request

from app.services.client_registry import ClientConfig, ClientRegistry
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
    host = request.client.host if request.client else ""
    forwarded = request.headers.get("x-forwarded-for")
    if forwarded:
        host = forwarded.split(",")[0].strip()
    if not ClientRegistry.ip_allowed(client, host):
        raise HTTPException(status_code=403, detail="IP not allowed")
    return client
