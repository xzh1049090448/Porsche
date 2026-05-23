"""Runtime security checks for ai-gateway (post-hardening verification)."""

from __future__ import annotations

import pytest
from httpx import ASGITransport, AsyncClient

from app.main import app


@pytest.fixture
async def client():
    transport = ASGITransport(app=app)
    async with AsyncClient(transport=transport, base_url="http://test") as ac:
        yield ac


async def _register(client: AsyncClient, phone: str) -> str:
    send = await client.post("/api/v1/auth/send-code", json={"phone": phone})
    assert send.status_code == 200, send.text
    code = send.json()["dev_code"]
    reg = await client.post(
        "/api/v1/auth/register",
        json={"phone": phone, "code": code, "password": "test1234"},
    )
    assert reg.status_code == 200, reg.text
    return reg.json()["access_token"]


@pytest.mark.asyncio
async def test_metrics_requires_auth(client: AsyncClient):
    denied = await client.get("/metrics")
    assert denied.status_code == 401
    ok = await client.get(
        "/metrics",
        headers={"Authorization": "Bearer test-admin-token"},
    )
    assert ok.status_code == 200


@pytest.mark.asyncio
async def test_platform_models_requires_auth(client: AsyncClient):
    denied = await client.get("/api/v1/platform/models")
    assert denied.status_code == 401
    token = await _register(client, "13800138010")
    ok = await client.get(
        "/api/v1/platform/models",
        headers={"Authorization": f"Bearer {token}"},
    )
    assert ok.status_code == 200


@pytest.mark.asyncio
async def test_sms_rate_limit(client: AsyncClient):
  phone = "13800138777"
  limit = 5
  for i in range(limit):
      r = await client.post("/api/v1/auth/send-code", json={"phone": phone})
      assert r.status_code == 200, f"request {i}: {r.text}"
  blocked = await client.post("/api/v1/auth/send-code", json={"phone": phone})
  assert blocked.status_code == 429


@pytest.mark.asyncio
async def test_fake_payment_allowed_when_mock_enabled(client: AsyncClient):
    phone = "13800138997"
    token = await _register(client, phone)
    headers = {"Authorization": f"Bearer {token}"}
    order = await client.post(
        "/api/v1/billing/orders",
        json={"plan_type": "professional"},
        headers=headers,
    )
    pay = await client.post(
        f"/api/v1/billing/orders/{order.json()['id']}/pay",
        headers=headers,
    )
    assert pay.status_code == 200
    me = await client.get("/api/v1/users/me", headers=headers)
    assert me.json()["plan_type"] == "professional"


@pytest.mark.asyncio
async def test_idor_conversation_blocked(client: AsyncClient):
    token_a = await _register(client, "13800138901")
    token_b = await _register(client, "13800138902")
    create = await client.post(
        "/api/v1/conversations",
        json={"title": "secret", "model": "qwen-turbo"},
        headers={"Authorization": f"Bearer {token_a}"},
    )
    conv_id = create.json()["id"]
    leak = await client.get(
        f"/api/v1/conversations/{conv_id}",
        headers={"Authorization": f"Bearer {token_b}"},
    )
    assert leak.status_code == 404


@pytest.mark.asyncio
async def test_real_name_rejects_invalid_id_card(client: AsyncClient):
    token = await _register(client, "13800138903")
    headers = {"Authorization": f"Bearer {token}"}
    verify = await client.post(
        "/api/v1/users/me/verify",
        json={"real_name": "张三", "id_card": "00000000000000000"},
        headers=headers,
    )
    assert verify.status_code == 400
