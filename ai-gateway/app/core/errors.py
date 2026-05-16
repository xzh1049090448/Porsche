"""Unified API error payloads (OpenAI-style compatible envelope)."""

from __future__ import annotations

from typing import Any

from pydantic import BaseModel


class ErrorDetail(BaseModel):
    message: str
    type: str = "gateway_error"
    code: str | None = None
    param: str | None = None


class ErrorResponse(BaseModel):
    error: ErrorDetail


def error_body(
    message: str,
    *,
    err_type: str = "gateway_error",
    code: str | None = None,
    param: str | None = None,
) -> dict[str, Any]:
    return ErrorResponse(
        error=ErrorDetail(message=message, type=err_type, code=code, param=param)
    ).model_dump()
