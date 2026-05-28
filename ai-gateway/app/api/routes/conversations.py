"""Conversation history CRUD and export."""

from __future__ import annotations

from typing import Annotated

from fastapi import APIRouter, Depends, Query
from fastapi.responses import PlainTextResponse
from sqlalchemy.ext.asyncio import AsyncSession

from app.api.deps import get_current_user
from app.db.models import User
from app.db.session import get_db
from app.schemas.conversation import (
    ConversationCreate,
    ConversationListResponse,
    ConversationResponse,
    ConversationUpdate,
)
from app.schemas.serializers import conversation_response
from app.services.conversation_service import ConversationService

router = APIRouter(prefix="/api/v1/conversations", tags=["conversations"])


@router.get("", response_model=ConversationListResponse)
async def list_conversations(
    user: Annotated[User, Depends(get_current_user)],
    db: Annotated[AsyncSession, Depends(get_db)],
    skip: int = Query(0, ge=0),
    limit: int = Query(20, ge=1, le=100),
):
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
    conv = await ConversationService.get(db, user, conversation_id)
    return conversation_response(conv, include_messages=True)


@router.put("/{conversation_id}", response_model=ConversationResponse)
async def update_conversation(
    conversation_id: int,
    body: ConversationUpdate,
    user: Annotated[User, Depends(get_current_user)],
    db: Annotated[AsyncSession, Depends(get_db)],
):
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
    await ConversationService.delete(db, user, conversation_id)
    return {"message": "对话已删除"}


@router.get("/{conversation_id}/export/markdown", response_class=PlainTextResponse)
async def export_markdown(
    conversation_id: int,
    user: Annotated[User, Depends(get_current_user)],
    db: Annotated[AsyncSession, Depends(get_db)],
):
    conv = await ConversationService.get(db, user, conversation_id)
    return ConversationService.export_markdown(conv)
