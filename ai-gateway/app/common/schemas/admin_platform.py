"""Pydantic schemas for admin APIs."""

from __future__ import annotations

from datetime import datetime

from pydantic import BaseModel, Field


class AdminUserResponse(BaseModel):
    id: int
    phone: str
    nickname: str | None
    plan_type: str
    status: str
    is_verified: bool
    total_tokens_used: int
    dataset_calls: int
    created_at: datetime

    model_config = {"from_attributes": True}


class AdminUserUpdateRequest(BaseModel):
    status: str | None = None
    plan_type: str | None = None
    allowed_models: list[str] | None = None
    allowed_datasets: list[int] | None = None
    daily_call_limit: int | None = None


class AuditLogResponse(BaseModel):
    id: int
    user_id: int | None
    action: str
    resource: str | None
    detail: dict | None
    ip: str | None
    created_at: datetime

    model_config = {"from_attributes": True}


class DashboardResponse(BaseModel):
    total_users: int
    active_users_today: int
    total_conversations: int
    total_tokens: int
    dataset_calls: int
    model_usage: dict[str, int]
    dataset_usage: dict[str, int]
    plan_distribution: dict[str, int]


class ModelHealthResponse(BaseModel):
    model_name: str
    provider: str
    is_available: bool
    avg_latency_ms: float
    error_rate: float
    last_checked_at: datetime | None

    model_config = {"from_attributes": True}


class AlertConfig(BaseModel):
    alert_type: str
    threshold: float
    enabled: bool = True
    webhook_url: str | None = None
