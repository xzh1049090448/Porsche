"""Audit logging service."""

from __future__ import annotations

from sqlalchemy.ext.asyncio import AsyncSession

from app.db.models import AuditLog


class AuditService:
  @staticmethod
  async def log(
    db: AsyncSession,
    *,
    action: str,
    user_id: int | None = None,
    resource: str | None = None,
    detail: dict | None = None,
    ip: str | None = None,
  ) -> None:
    entry = AuditLog(
      user_id=user_id,
      action=action,
      resource=resource,
      detail=detail,
      ip=ip,
    )
    db.add(entry)
