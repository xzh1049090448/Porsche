"""Pydantic schemas for platform chat and model comparison."""

from __future__ import annotations

from typing import Any

from pydantic import BaseModel, Field


class PlatformChatMessage(BaseModel):
    role: str
    content: str | list[dict[str, Any]] | None = None


class PlatformChatRequest(BaseModel):
    model: str
    messages: list[PlatformChatMessage]
    conversation_id: int | None = None
    temperature: float | None = Field(None, ge=0, le=1)
    max_tokens: int | None = Field(None, ge=1, le=128000)
    context_window: int | None = Field(None, ge=1, le=128)
    stream: bool = False
    dataset_enabled: bool = False
    dataset_ids: list[int] | None = None


class PlatformCompareRequest(BaseModel):
    models: list[str] = Field(..., min_length=2, max_length=5)
    messages: list[PlatformChatMessage]
    temperature: float | None = Field(None, ge=0, le=1)
    max_tokens: int | None = Field(None, ge=1, le=128000)
    dataset_enabled: bool = False
    dataset_ids: list[int] | None = None


class CompareResultItem(BaseModel):
    model: str
    content: str | None = None
    error: str | None = None
    tokens: int = 0
    latency_ms: float = 0


class PlatformCompareResponse(BaseModel):
    results: list[CompareResultItem]
    dataset_attribution: str | None = None
