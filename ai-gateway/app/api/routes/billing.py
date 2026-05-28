"""Billing, plans, and orders."""

from __future__ import annotations

from typing import Annotated

from fastapi import APIRouter, Depends
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app.api.deps import get_current_user, get_state
from app.db.enum_utils import enum_value
from app.db.models import Order, User
from app.db.session import get_db
from app.schemas.billing import (
    CreateOrderRequest,
    InvoiceRequest,
    OrderListResponse,
    OrderResponse,
    PlansResponse,
)
from app.state import AppState

router = APIRouter(prefix="/api/v1/billing", tags=["billing"])


@router.get("/plans", response_model=PlansResponse)
async def get_plans(
    user: Annotated[User, Depends(get_current_user)],
    state: Annotated[AppState, Depends(get_state)],
):
    plans = state.billing.get_plans(enum_value(user.plan_type))
    return PlansResponse(plans=plans, current_plan=enum_value(user.plan_type))


@router.post("/orders", response_model=OrderResponse)
async def create_order(
    body: CreateOrderRequest,
    user: Annotated[User, Depends(get_current_user)],
    state: Annotated[AppState, Depends(get_state)],
    db: Annotated[AsyncSession, Depends(get_db)],
):
    order = await state.billing.create_order(db, user, body.plan_type)
    return order


@router.post("/orders/{order_id}/pay", response_model=OrderResponse)
async def pay_order(
    order_id: int,
    user: Annotated[User, Depends(get_current_user)],
    state: Annotated[AppState, Depends(get_state)],
    db: Annotated[AsyncSession, Depends(get_db)],
):
    order = await state.billing.pay_order(db, user, order_id)
    return order


@router.get("/orders", response_model=OrderListResponse)
async def list_orders(
    user: Annotated[User, Depends(get_current_user)],
    db: Annotated[AsyncSession, Depends(get_db)],
):
    rows = await db.scalars(
        select(Order).where(Order.user_id == user.id).order_by(Order.created_at.desc())
    )
    return OrderListResponse(items=[OrderResponse.model_validate(o) for o in rows.all()])


@router.post("/invoice")
async def request_invoice(
    body: InvoiceRequest,
    user: Annotated[User, Depends(get_current_user)],
    state: Annotated[AppState, Depends(get_state)],
    db: Annotated[AsyncSession, Depends(get_db)],
):
    order = await state.billing.request_invoice(db, user, body.order_id)
    return {"message": "发票申请已提交", "order_no": order.order_no}
