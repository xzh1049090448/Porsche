"""Platform backend integration tests."""

from __future__ import annotations

import pytest
from httpx import ASGITransport, AsyncClient

from app.main import app


@pytest.fixture
async def client():
    transport = ASGITransport(app=app)
    async with AsyncClient(transport=transport, base_url="http://test") as ac:
        yield ac


@pytest.mark.asyncio
async def test_auth_register_and_login(client: AsyncClient):
    phone = "13800138001"
    send_resp = await client.post("/api/v1/auth/send-code", json={"phone": phone})
    assert send_resp.status_code == 200
    code = send_resp.json()["dev_code"]

    reg_resp = await client.post(
        "/api/v1/auth/register",
        json={"phone": phone, "code": code, "password": "test1234", "nickname": "测试用户"},
    )
    assert reg_resp.status_code == 200
    token = reg_resp.json()["access_token"]

    login_resp = await client.post(
        "/api/v1/auth/login/password",
        json={"phone": phone, "password": "test1234"},
    )
    assert login_resp.status_code == 200

    me_resp = await client.get(
        "/api/v1/users/me",
        headers={"Authorization": f"Bearer {token}"},
    )
    assert me_resp.status_code == 200
    assert me_resp.json()["phone"] == phone


@pytest.mark.asyncio
async def test_list_datasets(client: AsyncClient):
    phone = "13800138002"
    send_resp = await client.post("/api/v1/auth/send-code", json={"phone": phone})
    code = send_resp.json()["dev_code"]
    reg_resp = await client.post(
        "/api/v1/auth/register",
        json={"phone": phone, "code": code, "password": "test1234"},
    )
    token = reg_resp.json()["access_token"]

    ds_resp = await client.get(
        "/api/v1/datasets",
        headers={"Authorization": f"Bearer {token}"},
    )
    assert ds_resp.status_code == 200
    items = ds_resp.json()["items"]
    assert len(items) >= 2
    slugs = {d["slug"] for d in items}
    assert "product-knowledge" in slugs
    assert "customer-service" in slugs


@pytest.mark.asyncio
async def test_list_models(client: AsyncClient):
    phone = "13800138003"
    send_resp = await client.post("/api/v1/auth/send-code", json={"phone": phone})
    code = send_resp.json()["dev_code"]
    reg_resp = await client.post(
        "/api/v1/auth/register",
        json={"phone": phone, "code": code, "password": "test1234"},
    )
    token = reg_resp.json()["access_token"]

    models_resp = await client.get(
        "/api/v1/platform/models",
        headers={"Authorization": f"Bearer {token}"},
    )
    assert models_resp.status_code == 200
    models = models_resp.json()["models"]
    assert len(models) >= 10
    model_ids = {m["id"] for m in models}
    assert "qwen-turbo" in model_ids
    assert "deepseek-chat" in model_ids


@pytest.mark.asyncio
async def test_admin_dashboard(client: AsyncClient):
    resp = await client.get(
        "/admin/dashboard",
        headers={"Authorization": "Bearer test-admin-token"},
    )
    assert resp.status_code == 200
    data = resp.json()
    assert "total_users" in data
    assert "plan_distribution" in data
