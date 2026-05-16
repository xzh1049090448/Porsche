"""Authenticated admin endpoints (reload config, status)."""

from __future__ import annotations

import secrets
from typing import Annotated

from fastapi import APIRouter, Depends, Header, HTTPException

from app.api.deps import get_state
from app.config import get_settings
from app.state import AppState

router = APIRouter(prefix="/admin", tags=["admin"])


def verify_admin(authorization: Annotated[str | None, Header()] = None) -> None:
    settings = get_settings()
    if not authorization or not authorization.lower().startswith("bearer "):
        raise HTTPException(status_code=401, detail="Missing admin Authorization")
    token = authorization.split(" ", 1)[1].strip()
    if not secrets.compare_digest(token, settings.admin_token):
        raise HTTPException(status_code=403, detail="Forbidden")


@router.get("/status", dependencies=[Depends(verify_admin)])
async def admin_status(state: Annotated[AppState, Depends(get_state)]) -> dict:
    upstream_models = {k: v.provider for k, v in state.models.routes.items()}
    return {
        "models": len(state.models.routes),
        "clients": state.clients.client_count,
        "routes": upstream_models,
    }


@router.post("/reload-config", dependencies=[Depends(verify_admin)])
async def reload_config(state: Annotated[AppState, Depends(get_state)]) -> dict:
    await state.models.reload()
    await state.clients.reload()
    state.rebuild_upstream_pool()
    return {"status": "reloaded", "models": len(state.models.routes), "clients": state.clients.client_count}
