"""Pydantic schemas for user profile."""

from __future__ import annotations

from datetime import datetime
from enum import Enum

from pydantic import BaseModel, Field, field_validator


class UserProfileResponse(BaseModel):
    id: int
    phone: str
    nickname: str | None
    is_verified: bool
    plan_type: str
    total_tokens_used: int
    dataset_calls: int
    daily_calls_used: int
    daily_call_limit: int
    created_at: datetime

    model_config = {"from_attributes": True}

    @field_validator("plan_type", mode="before")
    @classmethod
    def coerce_plan_type(cls, v: object) -> str:
        if isinstance(v, Enum):
            return v.value
        return str(v)


class UpdateProfileRequest(BaseModel):
    nickname: str | None = Field(None, max_length=64)


class ChangePasswordRequest(BaseModel):
    old_password: str
    new_password: str = Field(..., min_length=6, max_length=64)


class RealNameVerifyRequest(BaseModel):
    real_name: str = Field(..., min_length=2, max_length=64)
    id_card: str = Field(..., min_length=15, max_length=18)


class UsageStatsResponse(BaseModel):
    total_tokens_used: int
    dataset_calls: int
    daily_calls_used: int
    daily_call_limit: int
    remaining_daily_calls: int
    plan_type: str

    @field_validator("plan_type", mode="before")
    @classmethod
    def coerce_plan_type(cls, v: object) -> str:
        if isinstance(v, Enum):
            return v.value
        return str(v)
