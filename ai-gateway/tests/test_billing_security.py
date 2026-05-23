"""Unit tests for billing security gates."""

from __future__ import annotations

import pytest
from fastapi import HTTPException

from app.config import Settings
from app.services.billing_service import BillingService


@pytest.mark.asyncio
async def test_pay_order_blocked_when_mock_payment_disabled():
    svc = BillingService(Settings(billing_allow_mock_payment=False))
    with pytest.raises(HTTPException) as exc_info:
        await svc.pay_order(None, None, 1)  # type: ignore[arg-type]
    assert exc_info.value.status_code == 403
