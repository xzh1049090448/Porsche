"""Model analytics API tests."""

from __future__ import annotations

from datetime import datetime, timedelta, timezone

import pytest
from httpx import ASGITransport, AsyncClient
from sqlalchemy import delete, select

from app.db.models import UsageRecord, User
from app.db.session import async_session_factory
from app.main import app

ADMIN_PHONE = "13800138000"
NORMAL_PHONE = "13800138999"
PREFIX = "/api/v1/billing/analytics"


@pytest.fixture
async def client():
    transport = ASGITransport(app=app)
    async with AsyncClient(transport=transport, base_url="http://test") as ac:
        yield ac


async def _ensure_user_token(client: AsyncClient, phone: str) -> str:
    login = await client.post(
        "/api/v1/auth/login/password",
        json={"phone": phone, "password": "test1234"},
    )
    if login.status_code == 200:
        return login.json()["access_token"]

    send = await client.post("/api/v1/auth/send-code", json={"phone": phone})
    assert send.status_code == 200, send.text
    code = send.json()["dev_code"]
    reg = await client.post(
        "/api/v1/auth/register",
        json={"phone": phone, "code": code, "password": "test1234"},
    )
    assert reg.status_code == 200, reg.text
    return reg.json()["access_token"]


async def _seed_usage(admin_user_id: int, normal_user_id: int) -> None:
    now = datetime.now(timezone.utc)
    records = [
        UsageRecord(
            user_id=admin_user_id,
            record_type="chat",
            tokens=1000,
            model="glm-5.1",
            created_at=now - timedelta(hours=3),
        ),
        UsageRecord(
            user_id=admin_user_id,
            record_type="chat",
            tokens=2000,
            model="glm-5.1",
            created_at=now - timedelta(hours=1),
        ),
        UsageRecord(
            user_id=normal_user_id,
            record_type="chat",
            tokens=500,
            model="deepseek-v3",
            created_at=now - timedelta(hours=2),
        ),
        UsageRecord(
            user_id=normal_user_id,
            record_type="chat",
            tokens=800,
            model="deepseek-v3",
            created_at=now - timedelta(minutes=30),
        ),
    ]
    assert async_session_factory is not None
    async with async_session_factory() as db:
        await db.execute(delete(UsageRecord))
        db.add_all(records)
        await db.commit()


async def _user_id(phone: str) -> int:
    assert async_session_factory is not None
    async with async_session_factory() as db:
        user = await db.scalar(select(User).where(User.phone == phone))
        assert user is not None
        return user.id


async def _login_token(client: AsyncClient, phone: str) -> str:
    login = await client.post(
        "/api/v1/auth/login/password",
        json={"phone": phone, "password": "test1234"},
    )
    assert login.status_code == 200, login.text
    return login.json()["access_token"]


@pytest.mark.asyncio
async def test_access_denied_for_normal_user(client: AsyncClient):
    admin_token = await _ensure_user_token(client, ADMIN_PHONE)
    normal_token = await _ensure_user_token(client, NORMAL_PHONE)

    access = await client.get(
        f"{PREFIX}/access",
        headers={"Authorization": f"Bearer {normal_token}"},
    )
    assert access.status_code == 200
    assert access.json()["allowed"] is False

    denied = await client.get(
        f"{PREFIX}/summary",
        headers={"Authorization": f"Bearer {normal_token}"},
    )
    assert denied.status_code == 403

    allowed = await client.get(
        f"{PREFIX}/access",
        headers={"Authorization": f"Bearer {admin_token}"},
    )
    assert allowed.status_code == 200
    assert allowed.json()["allowed"] is True


@pytest.mark.asyncio
async def test_summary_and_charts_for_admin(client: AsyncClient):
    await _ensure_user_token(client, ADMIN_PHONE)
    await _ensure_user_token(client, NORMAL_PHONE)
    admin_id = await _user_id(ADMIN_PHONE)
    normal_id = await _user_id(NORMAL_PHONE)
    await _seed_usage(admin_id, normal_id)

    admin_token = await _login_token(client, ADMIN_PHONE)
    headers = {"Authorization": f"Bearer {admin_token}"}

    summary = await client.get(f"{PREFIX}/summary", headers=headers)
    assert summary.status_code == 200, summary.text
    body = summary.json()
    assert body["total_tokens"] == 4300
    assert body["total_calls"] == 4
    assert body["total_cost"] == pytest.approx(4.3, rel=1e-2)
    assert body["range_label"] == "近24小时"

    chart = await client.get(
        f"{PREFIX}/charts/consumption_distribution",
        headers=headers,
        params={"granularity": "2h"},
    )
    assert chart.status_code == 200, chart.text
    chart_body = chart.json()
    assert chart_body["view"] == "consumption_distribution"
    assert len(chart_body["series"]) >= 1
    if chart_body["time_labels"]:
        for s in chart_body["series"]:
            assert len(s["data"]) == len(chart_body["time_labels"])

    ranking = await client.get(
        f"{PREFIX}/charts/call_ranking",
        headers=headers,
        params={"top_n": 5},
    )
    assert ranking.status_code == 200, ranking.text
    assert len(ranking.json()["ranking"]) >= 1

    trend = await client.get(
        f"{PREFIX}/charts/call_trend",
        headers=headers,
        params={"granularity": "2h"},
    )
    assert trend.status_code == 200, trend.text
    trend_body = trend.json()
    assert trend_body["view"] == "call_trend"
    assert len(trend_body["time_labels"]) >= 1
    assert len(trend_body["series"][0]["data"]) == len(trend_body["time_labels"])

    user_trend = await client.get(
        f"{PREFIX}/charts/user_consumption_trend",
        headers=headers,
        params={"granularity": "2h", "user_id": admin_id},
    )
    assert user_trend.status_code == 200, user_trend.text
    user_trend_body = user_trend.json()
    assert user_trend_body["view"] == "user_consumption_trend"
    assert len(user_trend_body["series"][0]["data"]) == len(user_trend_body["time_labels"])


@pytest.mark.asyncio
async def test_export_returns_xlsx(client: AsyncClient):
    await _ensure_user_token(client, ADMIN_PHONE)
    await _ensure_user_token(client, NORMAL_PHONE)
    admin_id = await _user_id(ADMIN_PHONE)
    normal_id = await _user_id(NORMAL_PHONE)
    await _seed_usage(admin_id, normal_id)

    headers = {"Authorization": f"Bearer {await _login_token(client, ADMIN_PHONE)}"}

    resp = await client.get(
        f"{PREFIX}/export",
        headers=headers,
        params={"view": "call_ranking", "range": "24h"},
    )
    assert resp.status_code == 200, resp.text
    assert (
        resp.headers.get("content-type")
        == "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
    )
    assert len(resp.content) > 0
    assert "model-analytics-call_ranking" in resp.headers.get("content-disposition", "")
