"""Model analytics API (admin-only).

Prefix: ``/api/v1/billing/analytics``
"""

from __future__ import annotations

from datetime import datetime
from typing import Annotated

from fastapi import APIRouter, Depends, Query
from fastapi.responses import Response
from sqlalchemy.ext.asyncio import AsyncSession

from app.api.deps import get_current_user, get_state, require_analytics_admin
from app.repository.models import User
from app.repository.session import get_db
from app.common.schemas.model_analytics import (
    AnalyticsAccessResponse,
    AnalyticsSummaryResponse,
    ChartResponse,
    ModelsListResponse,
)
from app.service.model_analytics_service import (
    ModelAnalyticsService,
    is_analytics_admin,
    parse_filters,
)
from app.state import AppState

router = APIRouter(prefix="/api/v1/billing/analytics", tags=["model-analytics"])


def _analytics_service(state: AppState) -> ModelAnalyticsService:
    return ModelAnalyticsService(state.settings)


def _common_query(
    range_preset: str = Query("24h", alias="range"),
    start_at: datetime | None = None,
    end_at: datetime | None = None,
    granularity: str = "2h",
    models: str = "",
    top_n: int = 10,
    user_id: int | None = None,
    metric: str = "cost",
):
    return parse_filters(
        range_preset=range_preset,
        start_at=start_at,
        end_at=end_at,
        granularity=granularity,
        models=models,
        top_n=top_n,
        user_id=user_id,
        metric=metric,
    )


@router.get("/access", response_model=AnalyticsAccessResponse)
async def check_access(
    user: Annotated[User, Depends(get_current_user)],
    state: Annotated[AppState, Depends(get_state)],
):
    """Check whether the current user may access analytics."""
    return AnalyticsAccessResponse(allowed=is_analytics_admin(user.phone, state.settings))


@router.get("/summary", response_model=AnalyticsSummaryResponse)
async def get_summary(
    user: Annotated[User, Depends(require_analytics_admin)],
    db: Annotated[AsyncSession, Depends(get_db)],
    state: Annotated[AppState, Depends(get_state)],
    filters: Annotated[object, Depends(_common_query)],
):
    svc = _analytics_service(state)
    return await svc.get_summary(db, filters)  # type: ignore[arg-type]


@router.get("/models", response_model=ModelsListResponse)
async def list_models(
    user: Annotated[User, Depends(require_analytics_admin)],
    db: Annotated[AsyncSession, Depends(get_db)],
    state: Annotated[AppState, Depends(get_state)],
    filters: Annotated[object, Depends(_common_query)],
):
    svc = _analytics_service(state)
    return await svc.list_models(db, filters)  # type: ignore[arg-type]


@router.get("/charts/{view}", response_model=ChartResponse)
async def get_chart(
    view: str,
    user: Annotated[User, Depends(require_analytics_admin)],
    db: Annotated[AsyncSession, Depends(get_db)],
    state: Annotated[AppState, Depends(get_state)],
    filters: Annotated[object, Depends(_common_query)],
):
    svc = _analytics_service(state)
    return await svc.get_chart(db, view, filters)  # type: ignore[arg-type]


@router.get("/export")
async def export_chart(
    user: Annotated[User, Depends(require_analytics_admin)],
    db: Annotated[AsyncSession, Depends(get_db)],
    state: Annotated[AppState, Depends(get_state)],
    filters: Annotated[object, Depends(_common_query)],
    view: str = Query(...),
):
    svc = _analytics_service(state)
    content, filename = await svc.export_chart(db, view, filters)  # type: ignore[arg-type]
    return Response(
        content=content,
        media_type="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
        headers={"Content-Disposition": f'attachment; filename="{filename}"'},
    )
