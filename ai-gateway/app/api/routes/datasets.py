"""User-facing dataset listing."""

from __future__ import annotations

from typing import Annotated

from fastapi import APIRouter, Depends
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app.api.deps import get_current_user
from app.db.models import Dataset, DatasetStatus, User
from app.db.session import get_db
from app.schemas.dataset import DatasetListResponse, DatasetResponse

router = APIRouter(prefix="/api/v1/datasets", tags=["datasets"])


@router.get("", response_model=DatasetListResponse)
async def list_datasets(
    user: Annotated[User, Depends(get_current_user)],
    db: Annotated[AsyncSession, Depends(get_db)],
):
    rows = await db.scalars(
        select(Dataset).where(Dataset.status == DatasetStatus.ACTIVE).order_by(Dataset.id)
    )
    items = []
    for ds in rows.all():
        if user.allowed_datasets and ds.id not in user.allowed_datasets:
            continue
        if ds.access_plans and user.plan_type.value not in ds.access_plans:
            continue
        items.append(DatasetResponse.model_validate(ds))
    return DatasetListResponse(items=items)
