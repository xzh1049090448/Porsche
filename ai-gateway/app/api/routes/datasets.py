"""用户侧数据集列表接口。

前缀: ``/api/v1/datasets``

需 JWT 鉴权；按用户套餐与授权过滤可见数据集。
"""

from __future__ import annotations

from typing import Annotated

from fastapi import APIRouter, Depends
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app.api.deps import get_current_user
from app.repository.enum_utils import enum_value
from app.repository.models import Dataset, DatasetStatus, User
from app.repository.session import get_db
from app.common.schemas.dataset import DatasetListResponse, DatasetResponse

router = APIRouter(prefix="/api/v1/datasets", tags=["datasets"])


@router.get("", response_model=DatasetListResponse)
async def list_datasets(
    user: Annotated[User, Depends(get_current_user)],
    db: Annotated[AsyncSession, Depends(get_db)],
):
    """获取当前用户可用的 RAG 专属数据集列表。

    - 仅返回 ``ACTIVE`` 状态的数据集
    - 按 ``access_plans`` 与用户 ``allowed_datasets`` 过滤
    """
    rows = await db.scalars(
        select(Dataset).where(Dataset.status == DatasetStatus.ACTIVE).order_by(Dataset.id)
    )
    items = []
    plan = enum_value(user.plan_type)
    allowed_ids = user.allowed_datasets or []
    for ds in rows.all():
        if allowed_ids and ds.id not in allowed_ids:
            continue
        plans = ds.access_plans or []
        if plans and plan not in plans:
            continue
        items.append(DatasetResponse.model_validate(ds))
    return DatasetListResponse(items=items)
