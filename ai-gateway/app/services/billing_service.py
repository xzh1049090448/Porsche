"""Billing, quota, and order management."""

from __future__ import annotations

import uuid
from datetime import datetime, timezone

from fastapi import HTTPException
from sqlalchemy import func, select
from sqlalchemy.ext.asyncio import AsyncSession

from app.config import Settings
from app.db.models import Order, OrderStatus, PlanType, User


PLAN_LIMITS = {
  PlanType.FREE: 100,
  PlanType.PROFESSIONAL: None,
  PlanType.ENTERPRISE: None,
}

PLAN_FEATURES = {
  PlanType.FREE: ["基础模型", "基础数据集", "每日100次调用"],
  PlanType.PROFESSIONAL: ["全模型", "全数据集", "无限次调用", "模型对比"],
  PlanType.ENTERPRISE: ["专属数据集部署", "API授权", "定制化服务", "专属客服"],
}


class BillingService:
  def __init__(self, settings: Settings) -> None:
    self._settings = settings

  def get_plans(self, current_plan: PlanType) -> list[dict]:
    return [
      {
        "plan_type": PlanType.FREE.value,
        "name": "免费版",
        "price": 0.0,
        "daily_call_limit": 100,
        "description": "每日100次调用，基础模型与基础数据集",
        "features": PLAN_FEATURES[PlanType.FREE],
      },
      {
        "plan_type": PlanType.PROFESSIONAL.value,
        "name": "专业版",
        "price": self._settings.plan_professional_price,
        "daily_call_limit": None,
        "description": "无限次调用，全模型与全数据集",
        "features": PLAN_FEATURES[PlanType.PROFESSIONAL],
      },
      {
        "plan_type": PlanType.ENTERPRISE.value,
        "name": "企业版",
        "price": self._settings.plan_enterprise_price,
        "daily_call_limit": None,
        "description": "定制化需求，专属数据集部署与API授权",
        "features": PLAN_FEATURES[PlanType.ENTERPRISE],
      },
    ]

  async def check_and_consume_call(self, db: AsyncSession, user: User, *, count: int = 1) -> None:
    if count < 1:
      return
    self._reset_daily_if_needed(user)
    limit = user.daily_call_limit
    if user.plan_type in (PlanType.PROFESSIONAL, PlanType.ENTERPRISE):
      user.daily_calls_used += count
      return
    if user.daily_calls_used + count > limit:
      raise HTTPException(status_code=429, detail="今日调用次数已达上限，请升级套餐")
    user.daily_calls_used += count

  async def create_order(self, db: AsyncSession, user: User, plan_type: str) -> Order:
    try:
      plan = PlanType(plan_type)
    except ValueError as exc:
      raise HTTPException(status_code=400, detail="无效的套餐类型") from exc
    if plan == PlanType.FREE:
      raise HTTPException(status_code=400, detail="免费版无需购买")
    price_map = {
      PlanType.PROFESSIONAL: self._settings.plan_professional_price,
      PlanType.ENTERPRISE: self._settings.plan_enterprise_price,
    }
    order = Order(
      order_no=f"ORD{uuid.uuid4().hex[:16].upper()}",
      user_id=user.id,
      plan_type=plan,
      amount=price_map[plan],
      status=OrderStatus.PENDING,
    )
    db.add(order)
    await db.flush()
    return order

  async def pay_order(self, db: AsyncSession, user: User, order_id: int) -> Order:
    if not self._settings.billing_allow_mock_payment:
      raise HTTPException(
        status_code=403,
        detail="在线支付未开通，请通过支付渠道完成付款后由系统确认",
      )
    order = await db.scalar(
      select(Order).where(Order.id == order_id, Order.user_id == user.id)
    )
    if not order:
      raise HTTPException(status_code=404, detail="订单不存在")
    if order.status != OrderStatus.PENDING:
      raise HTTPException(status_code=400, detail="订单状态不可支付")
    order.status = OrderStatus.PAID
    order.paid_at = datetime.now(timezone.utc)
    user.plan_type = order.plan_type
    if order.plan_type == PlanType.PROFESSIONAL:
      user.daily_call_limit = 999999
    elif order.plan_type == PlanType.ENTERPRISE:
      user.daily_call_limit = 999999
    return order

  async def request_invoice(self, db: AsyncSession, user: User, order_id: int) -> Order:
    order = await db.scalar(
      select(Order).where(Order.id == order_id, Order.user_id == user.id)
    )
    if not order:
      raise HTTPException(status_code=404, detail="订单不存在")
    if order.status != OrderStatus.PAID:
      raise HTTPException(status_code=400, detail="仅已支付订单可申请发票")
    order.invoice_requested = True
    return order

  @staticmethod
  def _reset_daily_if_needed(user: User) -> None:
    now = datetime.now(timezone.utc)
    if user.daily_calls_reset_at is None or user.daily_calls_reset_at.date() < now.date():
      user.daily_calls_used = 0
      user.daily_calls_reset_at = now

  @staticmethod
  async def get_usage_stats(user: User) -> dict:
    BillingService._reset_daily_if_needed(user)
    limit = user.daily_call_limit
    remaining = max(0, limit - user.daily_calls_used) if user.plan_type == PlanType.FREE else 999999
    return {
      "total_tokens_used": user.total_tokens_used,
      "dataset_calls": user.dataset_calls,
      "daily_calls_used": user.daily_calls_used,
      "daily_call_limit": limit,
      "remaining_daily_calls": remaining,
      "plan_type": user.plan_type.value,
    }

  @staticmethod
  async def plan_distribution(db: AsyncSession) -> dict[str, int]:
    rows = await db.execute(select(User.plan_type, func.count()).group_by(User.plan_type))
    return {plan.value: count for plan, count in rows.all()}
