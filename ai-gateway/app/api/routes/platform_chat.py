"""平台对话与模型对比接口（Web 前端使用）。

前缀: ``/api/v1/platform``

需 JWT 鉴权；支持 RAG 数据集增强、流式输出、多模型对比。
"""

from __future__ import annotations

from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException, Request
from fastapi.responses import StreamingResponse
from sqlalchemy.ext.asyncio import AsyncSession

from app.api.deps import get_client_ip, get_current_user, get_state, require_authenticated_user
from app.db.models import User
from app.db.session import get_db
from app.constants.platform_models import PLATFORM_MODEL_IDS
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
    """获取平台可用的大模型列表（仅 glm-5.1 / glm-4.5-air / glm-4.7-flash）。"""
    models = []
    for name in PLATFORM_MODEL_IDS:
        route = state.models.routes.get(name)
        if not route:
            continue
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
    """平台对话补全（支持流式 / 非流式）。

    - ``stream=true`` 时返回 SSE，首包为 ``type: meta``（含 conversation_id、数据集归因）
    - 可关联 ``conversation_id`` 自动持久化消息
    - ``dataset_enabled=true`` 时按 ``dataset_ids`` 做 RAG 检索增强
    - 受用户每日调用配额限制
    """
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
    """多模型并行对比（2～3 个模型）。

    - 同一输入分别调用各模型，返回内容、耗时、Token 数
    - 可选启用 RAG 数据集，响应含 ``dataset_attribution``
    - 按模型数量消耗每日调用次数
    """
    if state.platform_chat is None:
        raise HTTPException(status_code=503, detail="Platform chat not ready")

    messages = [m.model_dump(exclude_none=True) for m in body.messages]
    payload = await state.platform_chat.compare(
        db,
        user,
        models=body.models,
        messages=messages,
        conversation_id=body.conversation_id,
        temperature=body.temperature,
        max_tokens=body.max_tokens,
        context_window=body.context_window,
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
    return PlatformCompareResponse(
        results=payload["results"],
        dataset_attribution=attribution,
        conversation_id=payload.get("conversation_id"),
    )
