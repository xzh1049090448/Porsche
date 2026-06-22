"""Pydantic schemas for conversations."""

from __future__ import annotations

from datetime import datetime

from pydantic import BaseModel, Field


class ConversationCreate(BaseModel):
    title: str | None = Field(None, max_length=256)
    model: str | None = None
    dataset_enabled: bool = False
    dataset_ids: list[int] | None = None


class ConversationUpdate(BaseModel):
    title: str | None = Field(None, max_length=256)


class MessageResponse(BaseModel):
    id: int
    role: str
    content: str
    model: str | None
    dataset_used: bool
    dataset_attribution: str | None
    tokens: int
    created_at: datetime

    model_config = {"from_attributes": True}


class ConversationResponse(BaseModel):
    id: int
    title: str
    model: str | None
    dataset_enabled: bool
    dataset_ids: list | None
    created_at: datetime
    updated_at: datetime
    messages: list[MessageResponse] | None = None

    model_config = {"from_attributes": True}


class ConversationListResponse(BaseModel):
    items: list[ConversationResponse]
    total: int
