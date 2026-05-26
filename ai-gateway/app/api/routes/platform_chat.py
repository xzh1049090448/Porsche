"""Platform chat with RAG and model comparison."""

from __future__ import annotations

from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException, Request
from fastapi.responses import StreamingResponse
from sqlalchemy.ext.asyncio import AsyncSession

from app.api.deps import get_client_ip, get_current_user, get_state, require_authenticated_user
from app.db.models import User
from app.db.session import get_db
from app.schemas.platform import (
    PlatformChatRequest,
    PlatformCompareRequest,
    PlatformCompareResponse,
)
from app.services.audit_service import AuditService
from app.services.rag_engine import DATASET_ATTRIBUTION
from app.state import AppState

router = APIRouter(prefix="/api/v1/platform", tags=["platform"])


@router.get("/models")
async def list_models(
    state: Annotated[AppState, Depends(get_state)],
    _user_id: Annotated[int, Depends(require_authenticated_user)],
):
    """List available domestic LLM models."""
    models = []
    for name, route in state.models.routes.items():
        models.append(
            {
                "id": name,
                "provider": route.provider,
                "upstream_model": route.upstream_model,
            }
        )
    return {"models": models}


@router.post("/chat/completions")
async def platform_chat(
    body: PlatformChatRequest,
    request: Request,
    state: Annotated[AppState, Depends(get_state)],
    user: Annotated[User, Depends(get_current_user)],
    db: Annotated[AsyncSession, Depends(get_db)],
):
    if state.platform_chat is None:
        raise HTTPException(status_code=503, detail="Platform chat not ready")

    messages = [m.model_dump(exclude_none=True) for m in body.messages]

    if body.stream:
        async def event_stream():
            async for chunk in state.platform_chat.stream(
                db,
                user,
                model=body.model,
                messages=messages,
                conversation_id=body.conversation_id,
                temperature=body.temperature,
                max_tokens=body.max_tokens,
                context_window=body.context_window,
                dataset_enabled=body.dataset_enabled,
                dataset_ids=body.dataset_ids,
            ):
                yield chunk

        await AuditService.log(
            db,
            action="chat.stream",
            user_id=user.id,
            detail={"model": body.model, "dataset_enabled": body.dataset_enabled},
            ip=get_client_ip(request),
        )
        return StreamingResponse(event_stream(), media_type="text/event-stream")

    result = await state.platform_chat.chat(
        db,
        user,
        model=body.model,
        messages=messages,
        conversation_id=body.conversation_id,
        temperature=body.temperature,
        max_tokens=body.max_tokens,
        context_window=body.context_window,
        dataset_enabled=body.dataset_enabled,
        dataset_ids=body.dataset_ids,
    )
    await AuditService.log(
        db,
        action="chat.complete",
        user_id=user.id,
        detail={"model": body.model, "tokens": result.get("usage", {}).get("total_tokens", 0)},
        ip=get_client_ip(request),
    )
    return result


@router.post("/chat/compare", response_model=PlatformCompareResponse)
async def compare_models(
    body: PlatformCompareRequest,
    request: Request,
    state: Annotated[AppState, Depends(get_state)],
    user: Annotated[User, Depends(get_current_user)],
    db: Annotated[AsyncSession, Depends(get_db)],
):
    if state.platform_chat is None:
        raise HTTPException(status_code=503, detail="Platform chat not ready")

    messages = [m.model_dump(exclude_none=True) for m in body.messages]
    results = await state.platform_chat.compare(
        db,
        user,
        models=body.models,
        messages=messages,
        temperature=body.temperature,
        max_tokens=body.max_tokens,
        dataset_enabled=body.dataset_enabled,
        dataset_ids=body.dataset_ids,
    )
    attribution = DATASET_ATTRIBUTION if body.dataset_enabled else None
    await AuditService.log(
        db,
        action="chat.compare",
        user_id=user.id,
        detail={"models": body.models},
        ip=get_client_ip(request),
    )
    return PlatformCompareResponse(results=results, dataset_attribution=attribution)
