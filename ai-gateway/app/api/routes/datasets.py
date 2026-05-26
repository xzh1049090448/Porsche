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
    plan = user.plan_type.value
    allowed_ids = user.allowed_datasets or []
    for ds in rows.all():
        if allowed_ids and ds.id not in allowed_ids:
            continue
        plans = ds.access_plans or []
        if plans and plan not in plans:
            continue
        items.append(DatasetResponse.model_validate(ds))
    return DatasetListResponse(items=items)
