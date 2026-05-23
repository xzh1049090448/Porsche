"""Pydantic schemas for billing and orders."""

from __future__ import annotations

from datetime import datetime

from pydantic import BaseModel, Field


class PlanInfo(BaseModel):
    plan_type: str
    name: str
    price: float
    daily_call_limit: int | None
    description: str
    features: list[str]


class PlansResponse(BaseModel):
    plans: list[PlanInfo]
    current_plan: str


class CreateOrderRequest(BaseModel):
    plan_type: str = Field(..., pattern=r"^(professional|enterprise)$")


class OrderResponse(BaseModel):
    id: int
    order_no: str
    plan_type: str
    amount: float
    status: str
    invoice_requested: bool
    created_at: datetime
    paid_at: datetime | None

    model_config = {"from_attributes": True}


class OrderListResponse(BaseModel):
    items: list[OrderResponse]


class InvoiceRequest(BaseModel):
    order_id: int
