"""Pydantic schemas for datasets."""

from __future__ import annotations

from datetime import datetime
from enum import Enum
from typing import Annotated, Any

from pydantic import BaseModel, BeforeValidator, Field


def _coerce_enum_str(value: Any) -> str:
    if isinstance(value, Enum):
        return value.value
    return str(value)


class DatasetResponse(BaseModel):
    id: int
    name: str
    slug: str
    category: Annotated[str, BeforeValidator(_coerce_enum_str)]
    description: str | None
    status: Annotated[str, BeforeValidator(_coerce_enum_str)]
    current_version: str
    token_count: int
    vector_status: Annotated[str, BeforeValidator(_coerce_enum_str)]
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
