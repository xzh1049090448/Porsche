"""Admin audit logs and alerts."""

from __future__ import annotations

from datetime import datetime
from typing import Annotated

from fastapi import APIRouter, Depends, Query
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app.api.routes.admin import verify_admin
from app.db.models import AuditLog
from app.db.session import get_db
from app.schemas.admin_platform import AuditLogResponse

router = APIRouter(prefix="/admin/logs", tags=["admin-logs"], dependencies=[Depends(verify_admin)])

_alert_configs: list[dict] = [
    {"alert_type": "cost_overrun", "threshold": 10000.0, "enabled": True},
    {"alert_type": "abnormal_access", "threshold": 100.0, "enabled": True},
    {"alert_type": "service_down", "threshold": 1.0, "enabled": True},
]


@router.get("", response_model=list[AuditLogResponse])
async def list_logs(
    db: Annotated[AsyncSession, Depends(get_db)],
    action: str | None = None,
    user_id: int | None = None,
    skip: int = Query(0, ge=0),
    limit: int = Query(50, ge=1, le=500),
    start: datetime | None = None,
    end: datetime | None = None,
):
    q = select(AuditLog).order_by(AuditLog.created_at.desc()).offset(skip).limit(limit)
    if action:
        q = q.where(AuditLog.action == action)
    if user_id:
        q = q.where(AuditLog.user_id == user_id)
    if start:
        q = q.where(AuditLog.created_at >= start)
    if end:
        q = q.where(AuditLog.created_at <= end)
    rows = await db.scalars(q)
    return [AuditLogResponse.model_validate(log) for log in rows.all()]


@router.get("/alerts")
async def list_alerts():
    return {"alerts": _alert_configs}


@router.put("/alerts/{alert_type}")
async def update_alert(alert_type: str, threshold: float, enabled: bool = True):
    for cfg in _alert_configs:
        if cfg["alert_type"] == alert_type:
            cfg["threshold"] = threshold
            cfg["enabled"] = enabled
            return cfg
    new_cfg = {"alert_type": alert_type, "threshold": threshold, "enabled": enabled}
    _alert_configs.append(new_cfg)
    return new_cfg
