"""管理端用户管理接口。

前缀: ``/admin/users``

需 Admin Token 鉴权。
"""

from __future__ import annotations

from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException, Query
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app.api.routes.admin import verify_admin
from app.db.models import PlanType, User, UserStatus
from app.db.session import get_db
from app.schemas.admin_platform import AdminUserResponse, AdminUserUpdateRequest
from app.services.dashboard_service import DashboardService

router = APIRouter(prefix="/admin/users", tags=["admin-users"], dependencies=[Depends(verify_admin)])


@router.get("", response_model=list[AdminUserResponse])
async def list_users(
    db: Annotated[AsyncSession, Depends(get_db)],
    skip: int = Query(0, ge=0),
    limit: int = Query(50, ge=1, le=200),
    status: str | None = None,
):
    """分页查询用户列表，可按状态（``active`` / ``disabled``）筛选。"""
    q = select(User).order_by(User.created_at.desc()).offset(skip).limit(limit)
    if status:
        try:
            q = q.where(User.status == UserStatus(status))
        except ValueError:
            pass
    rows = await db.scalars(q)
    return [AdminUserResponse.model_validate(u) for u in rows.all()]


@router.get("/{user_id}", response_model=AdminUserResponse)
async def get_user(user_id: int, db: Annotated[AsyncSession, Depends(get_db)]):
    """获取指定用户详情。"""
    user = await db.get(User, user_id)
    if not user:
        raise HTTPException(status_code=404, detail="用户不存在")
    return user


@router.put("/{user_id}", response_model=AdminUserResponse)
async def update_user(
    user_id: int,
    body: AdminUserUpdateRequest,
    db: Annotated[AsyncSession, Depends(get_db)],
):
    """更新用户状态、套餐、可用模型/数据集、每日调用上限等。"""
    user = await db.get(User, user_id)
    if not user:
        raise HTTPException(status_code=404, detail="用户不存在")
    if body.status:
        user.status = UserStatus(body.status)
    if body.plan_type:
        user.plan_type = PlanType(body.plan_type)
    if body.allowed_models is not None:
        user.allowed_models = body.allowed_models
    if body.allowed_datasets is not None:
        user.allowed_datasets = body.allowed_datasets
    if body.daily_call_limit is not None:
        user.daily_call_limit = body.daily_call_limit
    return user


@router.get("/{user_id}/behavior")
async def user_behavior(user_id: int, db: Annotated[AsyncSession, Depends(get_db)]):
    """获取用户行为分析（模型偏好、数据集使用统计等）。"""
    user = await db.get(User, user_id)
    if not user:
        raise HTTPException(status_code=404, detail="用户不存在")
    return await DashboardService.user_behavior(db, user_id)
