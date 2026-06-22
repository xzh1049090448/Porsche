"""管理端仪表盘与模型健康检查接口。

前缀: ``/admin/dashboard``

需 Admin Token 鉴权。
"""

from __future__ import annotations

import time
from datetime import datetime, timezone
from typing import Annotated

from fastapi import APIRouter, Depends
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app.api.deps import get_state
from app.api.routes.admin import verify_admin
from app.repository.models import ModelHealth
from app.repository.session import get_db
from app.common.schemas.admin_platform import DashboardResponse, ModelHealthResponse
from app.service.dashboard_service import DashboardService
from app.state import AppState

router = APIRouter(
    prefix="/admin/dashboard", tags=["admin-dashboard"], dependencies=[Depends(verify_admin)]
)


@router.get("", response_model=DashboardResponse)
async def get_dashboard(db: Annotated[AsyncSession, Depends(get_db)]):
    """获取运营仪表盘数据（用户总数、套餐分布、调用量等汇总指标）。"""
    data = await DashboardService.get_dashboard(db)
    return DashboardResponse(**data)


@router.get("/models/health", response_model=list[ModelHealthResponse])
async def model_health(
    state: Annotated[AppState, Depends(get_state)],
    db: Annotated[AsyncSession, Depends(get_db)],
):
    """获取各上游模型的健康状态（可用性、平均延迟、错误率）。"""
    results = []
    for name, route in state.models.routes.items():
        health = await db.scalar(select(ModelHealth).where(ModelHealth.model_name == name))
        if not health:
            health = ModelHealth(
                model_name=name,
                provider=route.provider,
                is_available=True,
            )
            db.add(health)
            await db.flush()
        results.append(ModelHealthResponse.model_validate(health))
    return results


@router.post("/models/health/check")
async def check_model_health(
    state: Annotated[AppState, Depends(get_state)],
    db: Annotated[AsyncSession, Depends(get_db)],
):
    """主动探测所有模型路由可用性并更新健康指标。

    - 向每个模型发送简短 ping 请求
    - 更新延迟、可用性、错误率
    """
    client = state.clients.get_by_secret(state.settings.platform_client_secret)
    if not client:
        return {"message": "Platform client not configured"}

    updated = []
    for name, route in state.models.routes.items():
        health = await db.scalar(select(ModelHealth).where(ModelHealth.model_name == name))
        if not health:
            health = ModelHealth(model_name=name, provider=route.provider)
            db.add(health)

        t0 = time.perf_counter()
        is_ok = True
        try:
            from app.common.schemas.openai import ChatCompletionRequest, ChatMessage
            from app.service.gateway import GatewayService

            gw = GatewayService(state)
            body = ChatCompletionRequest(
                model=name,
                messages=[ChatMessage(role="user", content="ping")],
                max_tokens=5,
            )
            await gw.complete(client=client, body=body)
            latency = (time.perf_counter() - t0) * 1000
            health.avg_latency_ms = latency
            health.is_available = True
        except Exception:
            is_ok = False
            health.is_available = False
            health.error_rate = min(1.0, health.error_rate + 0.1)

        health.last_checked_at = datetime.now(timezone.utc)
        updated.append({"model": name, "available": is_ok})
    return {"checked": updated}
