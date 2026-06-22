"""对话历史 CRUD 与导出接口。

前缀: ``/api/v1/conversations``

需 JWT 鉴权；用户只能访问自己的对话。
"""

from __future__ import annotations

from typing import Annotated

from fastapi import APIRouter, Depends, Query
from fastapi.responses import PlainTextResponse
from sqlalchemy.ext.asyncio import AsyncSession

from app.api.deps import get_current_user
from app.repository.models import User
from app.repository.session import get_db
from app.common.schemas.conversation import (
    ConversationCreate,
    ConversationListResponse,
    ConversationResponse,
    ConversationUpdate,
)
from app.common.schemas.serializers import conversation_response
from app.service.conversation_service import ConversationService

router = APIRouter(prefix="/api/v1/conversations", tags=["conversations"])


@router.get("", response_model=ConversationListResponse)
async def list_conversations(
    user: Annotated[User, Depends(get_current_user)],
    db: Annotated[AsyncSession, Depends(get_db)],
    skip: int = Query(0, ge=0),
    limit: int = Query(20, ge=1, le=100),
):
    """分页获取当前用户的对话列表（不含消息详情，按更新时间倒序）。"""
    items, total = await ConversationService.list_conversations(db, user, skip=skip, limit=limit)
    return ConversationListResponse(
        items=[conversation_response(c) for c in items],
        total=total,
    )


@router.post("", response_model=ConversationResponse)
async def create_conversation(
    body: ConversationCreate,
    user: Annotated[User, Depends(get_current_user)],
    db: Annotated[AsyncSession, Depends(get_db)],
):
    """创建新对话（可指定标题、默认模型、是否启用数据集）。"""
    conv = await ConversationService.create(
        db,
        user,
        title=body.title,
        model=body.model,
        dataset_enabled=body.dataset_enabled,
        dataset_ids=body.dataset_ids,
    )
    return conversation_response(conv)


@router.get("/{conversation_id}", response_model=ConversationResponse)
async def get_conversation(
    conversation_id: int,
    user: Annotated[User, Depends(get_current_user)],
    db: Annotated[AsyncSession, Depends(get_db)],
):
    """获取单个对话详情（含全部消息记录）。"""
    conv = await ConversationService.get(db, user, conversation_id)
    return conversation_response(conv, include_messages=True)


@router.put("/{conversation_id}", response_model=ConversationResponse)
async def update_conversation(
    conversation_id: int,
    body: ConversationUpdate,
    user: Annotated[User, Depends(get_current_user)],
    db: Annotated[AsyncSession, Depends(get_db)],
):
    """更新对话（目前支持修改标题）。"""
    if body.title:
        conv = await ConversationService.update_title(db, user, conversation_id, body.title)
    else:
        conv = await ConversationService.get(db, user, conversation_id)
    return conversation_response(conv, include_messages=True)


@router.delete("/{conversation_id}")
async def delete_conversation(
    conversation_id: int,
    user: Annotated[User, Depends(get_current_user)],
    db: Annotated[AsyncSession, Depends(get_db)],
):
    """删除指定对话及其全部消息。"""
    await ConversationService.delete(db, user, conversation_id)
    return {"message": "对话已删除"}


@router.get("/{conversation_id}/export/markdown", response_class=PlainTextResponse)
async def export_markdown(
    conversation_id: int,
    user: Annotated[User, Depends(get_current_user)],
    db: Annotated[AsyncSession, Depends(get_db)],
):
    """将对话导出为 Markdown 文本（``text/plain``）。"""
    conv = await ConversationService.get(db, user, conversation_id)
    return ConversationService.export_markdown(conv)
