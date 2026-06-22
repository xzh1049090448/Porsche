"""ORM → API response builders (avoid Pydantic/from_attributes edge cases)."""

from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy.orm import attributes

from app.repository.enum_utils import enum_value
from app.repository.models import Conversation, Message, User
from app.common.schemas.conversation import ConversationResponse, MessageResponse
from app.common.schemas.user import UserProfileResponse


def _as_utc(dt: datetime) -> datetime:
    if dt.tzinfo is None:
        return dt.replace(tzinfo=timezone.utc)
    return dt


def message_response(msg: Message) -> MessageResponse:
    return MessageResponse(
        id=msg.id,
        role=msg.role,
        content=msg.content,
        model=msg.model,
        dataset_used=bool(msg.dataset_used),
        dataset_attribution=msg.dataset_attribution,
        tokens=int(msg.tokens or 0),
        created_at=_as_utc(msg.created_at),
    )


def conversation_response(conv: Conversation, *, include_messages: bool = False) -> ConversationResponse:
    messages: list[MessageResponse] = []
    if include_messages:
        state = attributes.instance_state(conv)
        if "messages" not in state.unloaded:
            messages = [message_response(m) for m in conv.messages]
    return ConversationResponse(
        id=conv.id,
        title=conv.title,
        model=conv.model,
        dataset_enabled=bool(conv.dataset_enabled),
        dataset_ids=conv.dataset_ids,
        created_at=_as_utc(conv.created_at),
        updated_at=_as_utc(conv.updated_at),
        messages=messages,
    )


def user_profile_response(user: User) -> UserProfileResponse:
    return UserProfileResponse(
        id=user.id,
        phone=user.phone,
        nickname=user.nickname,
        is_verified=bool(user.is_verified),
        plan_type=enum_value(user.plan_type),
        total_tokens_used=int(user.total_tokens_used or 0),
        dataset_calls=int(user.dataset_calls or 0),
        daily_calls_used=int(user.daily_calls_used or 0),
        daily_call_limit=int(user.daily_call_limit or 0),
        created_at=_as_utc(user.created_at),
    )
