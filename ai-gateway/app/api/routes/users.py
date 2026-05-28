"""User profile and usage endpoints."""

from __future__ import annotations

from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException
from sqlalchemy.ext.asyncio import AsyncSession

from app.api.deps import get_current_user
from app.core.security import hash_password, verify_password
from app.db.models import User
from app.db.session import get_db
from app.schemas.serializers import user_profile_response
from app.schemas.user import (
    ChangePasswordRequest,
    RealNameVerifyRequest,
    UpdateProfileRequest,
    UsageStatsResponse,
    UserProfileResponse,
)
from app.config import get_settings
from app.core.id_card import is_valid_id_card
from app.services.auth_service import AuthService
from app.services.billing_service import BillingService

router = APIRouter(prefix="/api/v1/users", tags=["users"])


@router.get("/me", response_model=UserProfileResponse)
async def get_profile(user: Annotated[User, Depends(get_current_user)]):
    return user_profile_response(user)


@router.put("/me", response_model=UserProfileResponse)
async def update_profile(
    body: UpdateProfileRequest,
    user: Annotated[User, Depends(get_current_user)],
):
    if body.nickname is not None:
        user.nickname = body.nickname
    return user_profile_response(user)


@router.post("/me/password")
async def change_password(
    body: ChangePasswordRequest,
    user: Annotated[User, Depends(get_current_user)],
):
    if not user.password_hash or not verify_password(body.old_password, user.password_hash):
        raise HTTPException(status_code=400, detail="原密码错误")
    user.password_hash = hash_password(body.new_password)
    return {"message": "密码修改成功"}


@router.post("/me/verify")
async def real_name_verify(
    body: RealNameVerifyRequest,
    user: Annotated[User, Depends(get_current_user)],
):
    if not is_valid_id_card(body.id_card):
        raise HTTPException(status_code=400, detail="身份证号格式无效")
    settings = get_settings()
    if not settings.real_name_auto_verify:
        raise HTTPException(
            status_code=501,
            detail="实名认证需对接第三方核验服务，暂未开通",
        )
    user.real_name = body.real_name
    user.id_card_hash = AuthService.hash_id_card(body.id_card)
    user.is_verified = True
    return {"message": "实名认证成功", "is_verified": True}


@router.get("/me/usage", response_model=UsageStatsResponse)
async def get_usage(user: Annotated[User, Depends(get_current_user)]):
    stats = await BillingService.get_usage_stats(user)
    return UsageStatsResponse(**stats)
