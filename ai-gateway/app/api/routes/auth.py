"""用户认证接口。

前缀: ``/api/v1/auth``

| 接口 | 鉴权 |
|------|------|
| 发送验证码 / 注册 / 验证码登录 | 公开（固定账号模式下部分接口禁用） |
| 密码登录 | 公开 |
"""

from __future__ import annotations

from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException, Request
from sqlalchemy.ext.asyncio import AsyncSession

from app.api.deps import get_client_ip, get_state
from app.repository.enum_utils import enum_value
from app.repository.session import get_db
from app.common.schemas.auth import (
    LoginCodeRequest,
    LoginPasswordRequest,
    RegisterRequest,
    SendCodeRequest,
    SendCodeResponse,
    TokenResponse,
)
from app.service.audit_service import AuditService
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
    """发送短信验证码。

    - 按手机号与 IP 限流
    - ``SMS_DEV_MODE=true`` 时响应体返回 ``dev_code``（仅开发/测试）
    - ``FIXED_LOGIN_ENABLED=true`` 时返回 403
    """
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
    """手机号注册（验证码 + 密码）。

    - 手机号不可重复
    - 成功后直接返回 JWT，等同登录
    - ``FIXED_LOGIN_ENABLED=true`` 时返回 403
    """
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
    """手机号 + 密码登录。

    - 返回 ``access_token``、``user_id``、``plan_type``
    - ``FIXED_LOGIN_ENABLED=true`` 时仅允许配置的固定账号密码
    """
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
    """手机号 + 短信验证码登录。

    - 验证码一次性有效，约 5 分钟过期
    - 用户不存在时自动注册
    - ``FIXED_LOGIN_ENABLED=true`` 时返回 403
    """
    _reject_if_password_only(state)
    user, token = await state.auth.login_code(db, phone=body.phone, code=body.code)
    await AuditService.log(
        db, action="user.login", user_id=user.id, detail={"method": "code"}, ip=get_client_ip(request)
    )
    return TokenResponse(
        access_token=token, user_id=user.id, plan_type=enum_value(user.plan_type)
    )
