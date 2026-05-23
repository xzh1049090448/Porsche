"""Dashboard and analytics for admin."""

from __future__ import annotations

from datetime import datetime, timedelta, timezone

from sqlalchemy import func, select
from sqlalchemy.ext.asyncio import AsyncSession

from app.db.models import Conversation, UsageRecord, User, UserStatus
from app.services.billing_service import BillingService


class DashboardService:
  @staticmethod
  async def get_dashboard(db: AsyncSession) -> dict:
    now = datetime.now(timezone.utc)
    today_start = now.replace(hour=0, minute=0, second=0, microsecond=0)

    total_users = await db.scalar(select(func.count()).select_from(User)) or 0
    active_today = await db.scalar(
      select(func.count(func.distinct(UsageRecord.user_id))).where(
        UsageRecord.created_at >= today_start
      )
    ) or 0
    total_conversations = await db.scalar(select(func.count()).select_from(Conversation)) or 0
    total_tokens = await db.scalar(select(func.coalesce(func.sum(User.total_tokens_used), 0))) or 0
    dataset_calls = await db.scalar(select(func.coalesce(func.sum(User.dataset_calls), 0))) or 0

    model_rows = await db.execute(
      select(UsageRecord.model, func.count())
      .where(UsageRecord.model.isnot(None))
      .group_by(UsageRecord.model)
    )
    model_usage = {model: count for model, count in model_rows.all() if model}

    dataset_rows = await db.execute(
      select(UsageRecord.dataset_id, func.count())
      .where(UsageRecord.dataset_id.isnot(None))
      .group_by(UsageRecord.dataset_id)
    )
    dataset_usage = {str(ds_id): count for ds_id, count in dataset_rows.all() if ds_id}

    plan_distribution = await BillingService.plan_distribution(db)

    return {
      "total_users": total_users,
      "active_users_today": active_today,
      "total_conversations": total_conversations,
      "total_tokens": int(total_tokens),
      "dataset_calls": int(dataset_calls),
      "model_usage": model_usage,
      "dataset_usage": dataset_usage,
      "plan_distribution": plan_distribution,
    }

  @staticmethod
  async def user_behavior(db: AsyncSession, user_id: int) -> dict:
    model_rows = await db.execute(
      select(UsageRecord.model, func.count(), func.sum(UsageRecord.tokens))
      .where(UsageRecord.user_id == user_id, UsageRecord.model.isnot(None))
      .group_by(UsageRecord.model)
    )
    models = [
      {"model": m, "calls": c, "tokens": int(t or 0)} for m, c, t in model_rows.all() if m
    ]
    dataset_rows = await db.execute(
      select(UsageRecord.dataset_id, func.count())
      .where(UsageRecord.user_id == user_id, UsageRecord.dataset_id.isnot(None))
      .group_by(UsageRecord.dataset_id)
    )
    datasets = {str(ds_id): count for ds_id, count in dataset_rows.all() if ds_id}
    return {"model_preferences": models, "dataset_usage": datasets}
