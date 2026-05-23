"""Pydantic schemas for datasets."""

from __future__ import annotations

from datetime import datetime

from pydantic import BaseModel, Field


class DatasetResponse(BaseModel):
    id: int
    name: str
    slug: str
    category: str
    description: str | None
    status: str
    current_version: str
    token_count: int
    vector_status: str
    access_plans: list | None
    created_at: datetime

    model_config = {"from_attributes": True}


class DatasetListResponse(BaseModel):
    items: list[DatasetResponse]


class DatasetCreateRequest(BaseModel):
    name: str = Field(..., max_length=128)
    slug: str = Field(..., max_length=64, pattern=r"^[a-z0-9_-]+$")
    category: str
    description: str | None = None
    access_plans: list[str] | None = None
    asset_id: str | None = None


class DatasetVersionResponse(BaseModel):
    id: int
    version: str
    token_count: int
    record_count: int
    is_active: bool
    compliance_report: dict | None
    created_at: datetime

    model_config = {"from_attributes": True}


class DatasetProcessResponse(BaseModel):
    dataset_id: int
    status: str
    message: str
