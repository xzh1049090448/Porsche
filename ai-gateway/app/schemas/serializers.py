"""ORM → API response builders (avoid Pydantic/from_attributes edge cases)."""

from __future__ import annotations

from datetime import datetime, timezone

from app.db.enum_utils import enum_value
from app.db.models import User
from app.schemas.user import UserProfileResponse


def _as_utc(dt: datetime) -> datetime:
    if dt.tzinfo is None:
        return dt.replace(tzinfo=timezone.utc)
    return dt


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
