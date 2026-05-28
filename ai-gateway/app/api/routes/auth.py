"""User authentication endpoints."""

from __future__ import annotations

from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException, Request
from sqlalchemy.ext.asyncio import AsyncSession

from app.api.deps import get_client_ip, get_state
from app.db.session import get_db
from app.schemas.auth import (
    LoginCodeRequest,
    LoginPasswordRequest,
    RegisterRequest,
    SendCodeRequest,
    SendCodeResponse,
    TokenResponse,
)
from app.db.enum_utils import enum_value
from app.services.audit_service import AuditService
from app.state import AppState

router = APIRouter(prefix="/api/v1/auth", tags=["auth"])

_PASSWORD_ONLY_MSG = "当前仅支持固定账号密码登录"


def _reject_if_password_only(state: AppState) -> None:
    if state.settings.fixed_login_enabled:
        raise HTTPException(status_code=403, detail=_PASSWORD_ONLY_MSG)


@router.post("/send-code", response_model=SendCodeResponse)
async def send_code(
    body: SendCodeRequest,
    request: Request,
    state: Annotated[AppState, Depends(get_state)],
):
    _reject_if_password_only(state)
    await state.sms.check_send_allowed(body.phone, get_client_ip(request))
    code = await state.sms.send_code(body.phone)
    resp = SendCodeResponse()
    if state.settings.sms_dev_mode:
        resp.dev_code = code
    return resp


@router.post("/register", response_model=TokenResponse)
async def register(
    body: RegisterRequest,
    request: Request,
    state: Annotated[AppState, Depends(get_state)],
    db: Annotated[AsyncSession, Depends(get_db)],
):
    _reject_if_password_only(state)
    user, token = await state.auth.register(
        db, phone=body.phone, code=body.code, password=body.password, nickname=body.nickname
    )
    await AuditService.log(
        db, action="user.register", user_id=user.id, ip=get_client_ip(request)
    )
    return TokenResponse(
        access_token=token, user_id=user.id, plan_type=enum_value(user.plan_type)
    )


@router.post("/login/password", response_model=TokenResponse)
async def login_password(
    body: LoginPasswordRequest,
    request: Request,
    state: Annotated[AppState, Depends(get_state)],
    db: Annotated[AsyncSession, Depends(get_db)],
):
    user, token = await state.auth.login_password(db, phone=body.phone, password=body.password)
    await AuditService.log(
        db, action="user.login", user_id=user.id, detail={"method": "password"}, ip=get_client_ip(request)
    )
    return TokenResponse(
        access_token=token, user_id=user.id, plan_type=enum_value(user.plan_type)
    )


@router.post("/login/code", response_model=TokenResponse)
async def login_code(
    body: LoginCodeRequest,
    request: Request,
    state: Annotated[AppState, Depends(get_state)],
    db: Annotated[AsyncSession, Depends(get_db)],
):
    _reject_if_password_only(state)
    user, token = await state.auth.login_code(db, phone=body.phone, code=body.code)
    await AuditService.log(
        db, action="user.login", user_id=user.id, detail={"method": "code"}, ip=get_client_ip(request)
    )
    return TokenResponse(
        access_token=token, user_id=user.id, plan_type=enum_value(user.plan_type)
    )
