"""Pydantic schemas for authentication."""

from __future__ import annotations

from pydantic import BaseModel, Field


class SendCodeRequest(BaseModel):
    phone: str = Field(..., min_length=11, max_length=11, pattern=r"^1\d{10}$")


class SendCodeResponse(BaseModel):
    message: str = "验证码已发送"
    dev_code: str | None = None


class RegisterRequest(BaseModel):
    phone: str = Field(..., min_length=11, max_length=11, pattern=r"^1\d{10}$")
    code: str = Field(..., min_length=4, max_length=8)
    password: str = Field(..., min_length=6, max_length=64)
    nickname: str | None = None


class LoginPasswordRequest(BaseModel):
    phone: str = Field(..., min_length=11, max_length=11)
    password: str


class LoginCodeRequest(BaseModel):
    phone: str = Field(..., min_length=11, max_length=11)
    code: str


class TokenResponse(BaseModel):
    access_token: str
    token_type: str = "bearer"
    user_id: int
    plan_type: str
