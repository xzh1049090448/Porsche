"""User authentication and registration service."""

from __future__ import annotations

import hashlib

from fastapi import HTTPException
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app.core.security import create_access_token, hash_password, verify_password
from app.db.enum_utils import enum_is, enum_value
from app.db.models import PlanType, User, UserStatus
from app.services.sms import SmsService


class AuthService:
  def __init__(
    self,
    sms: SmsService,
    jwt_secret: str,
    jwt_expire_minutes: int,
    *,
    fixed_login_enabled: bool = False,
    fixed_login_phone: str = "",
    fixed_login_password: str = "",
  ) -> None:
    self._sms = sms
    self._jwt_secret = jwt_secret
    self._jwt_expire_minutes = jwt_expire_minutes
    self._fixed_login_enabled = fixed_login_enabled
    self._fixed_phone = fixed_login_phone.strip()
    self._fixed_password = fixed_login_password

  async def register(
    self,
    db: AsyncSession,
    *,
    phone: str,
    code: str,
    password: str,
    nickname: str | None = None,
  ) -> tuple[User, str]:
    if not await self._sms.verify_code(phone, code):
      raise HTTPException(status_code=400, detail="验证码无效或已过期")
    existing = await db.scalar(select(User).where(User.phone == phone))
    if existing:
      raise HTTPException(status_code=409, detail="手机号已注册")
    user = User(
      phone=phone,
      password_hash=hash_password(password),
      nickname=nickname or f"用户{phone[-4:]}",
      plan_type=PlanType.FREE,
      status=UserStatus.ACTIVE,
    )
    db.add(user)
    await db.flush()
    token = self._make_token(user)
    return user, token

  async def login_password(self, db: AsyncSession, *, phone: str, password: str) -> tuple[User, str]:
    phone = phone.strip()
    if self._fixed_login_enabled:
      if phone != self._fixed_phone or password != self._fixed_password:
        raise HTTPException(status_code=401, detail="手机号或密码错误")
      user = await self._get_or_create_fixed_user(db, phone)
      self._ensure_active(user)
      return user, self._make_token(user)

    user = await db.scalar(select(User).where(User.phone == phone))
    if not user or not user.password_hash or not verify_password(password, user.password_hash):
      raise HTTPException(status_code=401, detail="手机号或密码错误")
    self._ensure_active(user)
    return user, self._make_token(user)

  async def _get_or_create_fixed_user(self, db: AsyncSession, phone: str) -> User:
    user = await db.scalar(select(User).where(User.phone == phone))
    if user:
      if not user.password_hash:
        user.password_hash = hash_password(self._fixed_password)
      return user
    user = User(
      phone=phone,
      password_hash=hash_password(self._fixed_password),
      nickname="测试用户",
      plan_type=PlanType.FREE,
      status=UserStatus.ACTIVE,
    )
    db.add(user)
    await db.flush()
    return user

  async def login_code(self, db: AsyncSession, *, phone: str, code: str) -> tuple[User, str]:
    if not await self._sms.verify_code(phone, code):
      raise HTTPException(status_code=400, detail="验证码无效或已过期")
    user = await db.scalar(select(User).where(User.phone == phone))
    if not user:
      user = User(
        phone=phone,
        nickname=f"用户{phone[-4:]}",
        plan_type=PlanType.FREE,
        status=UserStatus.ACTIVE,
      )
      db.add(user)
      await db.flush()
    self._ensure_active(user)
    return user, self._make_token(user)

  def _make_token(self, user: User) -> str:
    return create_access_token(
      subject=str(user.id),
      secret_key=self._jwt_secret,
      expires_minutes=self._jwt_expire_minutes,
      extra={"plan": enum_value(user.plan_type)},
    )

  @staticmethod
  def _ensure_active(user: User) -> None:
    if not enum_is(user.status, UserStatus.ACTIVE):
      raise HTTPException(status_code=403, detail="账号已被禁用")

  @staticmethod
  def hash_id_card(id_card: str) -> str:
    return hashlib.sha256(id_card.encode()).hexdigest()
