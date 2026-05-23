"""Liveness / readiness style health endpoint."""

from __future__ import annotations

from fastapi import APIRouter, Depends

from app.api.deps import get_state
from app.state import AppState

router = APIRouter(tags=["health"])


@router.get("/health")
async def health(state: AppState = Depends(get_state)) -> dict:
    if state.settings.app_env == "production":
        return {"status": "ok"}
    return {
        "status": "ok",
        "models_loaded": len(state.models.routes),
        "clients_loaded": state.clients.client_count,
    }
