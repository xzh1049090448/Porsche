"""Conversation and message persistence."""

from __future__ import annotations

from fastapi import HTTPException
from sqlalchemy import func, select
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.orm import selectinload

from app.db.models import Conversation, Message, User


class ConversationService:
  @staticmethod
  async def create(
    db: AsyncSession,
    user: User,
    *,
    title: str | None = None,
    model: str | None = None,
    dataset_enabled: bool = False,
    dataset_ids: list[int] | None = None,
  ) -> Conversation:
    conv = Conversation(
      user_id=user.id,
      title=title or "新对话",
      model=model,
      dataset_enabled=dataset_enabled,
      dataset_ids=dataset_ids,
    )
    db.add(conv)
    await db.flush()
    return conv

  @staticmethod
  async def get(db: AsyncSession, user: User, conversation_id: int) -> Conversation:
    conv = await db.scalar(
      select(Conversation)
      .options(selectinload(Conversation.messages))
      .where(Conversation.id == conversation_id, Conversation.user_id == user.id)
    )
    if not conv:
      raise HTTPException(status_code=404, detail="对话不存在")
    return conv

  @staticmethod
  async def list_conversations(
    db: AsyncSession, user: User, *, skip: int = 0, limit: int = 20
  ) -> tuple[list[Conversation], int]:
    total = await db.scalar(
      select(func.count()).select_from(Conversation).where(Conversation.user_id == user.id)
    ) or 0
    rows = await db.scalars(
      select(Conversation)
      .where(Conversation.user_id == user.id)
      .order_by(Conversation.updated_at.desc())
      .offset(skip)
      .limit(limit)
    )
    return list(rows.all()), total

  @staticmethod
  async def update_title(
    db: AsyncSession, user: User, conversation_id: int, title: str
  ) -> Conversation:
    conv = await ConversationService.get(db, user, conversation_id)
    conv.title = title
    return conv

  @staticmethod
  async def delete(db: AsyncSession, user: User, conversation_id: int) -> None:
    conv = await ConversationService.get(db, user, conversation_id)
    await db.delete(conv)

  @staticmethod
  async def add_message(
    db: AsyncSession,
    conversation: Conversation,
    *,
    role: str,
    content: str,
    model: str | None = None,
    dataset_used: bool = False,
    dataset_attribution: str | None = None,
    tokens: int = 0,
  ) -> Message:
    msg = Message(
      conversation_id=conversation.id,
      role=role,
      content=content,
      model=model,
      dataset_used=dataset_used,
      dataset_attribution=dataset_attribution,
      tokens=tokens,
    )
    db.add(msg)
    await db.flush()
    return msg

  @staticmethod
  def trim_messages(messages: list[dict], context_window: int | None) -> list[dict]:
    if not context_window or context_window <= 0:
      return messages
    return messages[-context_window * 2 :]

  @staticmethod
  def export_markdown(conversation: Conversation) -> str:
    lines = [f"# {conversation.title}", ""]
    for msg in conversation.messages:
      role_label = {"user": "用户", "assistant": "助手", "system": "系统"}.get(msg.role, msg.role)
      lines.append(f"## {role_label}")
      lines.append(msg.content)
      if msg.dataset_attribution:
        lines.append(f"> {msg.dataset_attribution}")
      lines.append("")
    return "\n".join(lines)
