"""Admin route tests."""

from fastapi.testclient import TestClient

from app.main import app


def test_admin_status_requires_auth():
    with TestClient(app) as client:
        r = client.get("/admin/status")
        assert r.status_code == 401


def test_admin_status_ok_with_token():
    with TestClient(app) as client:
        r = client.get(
            "/admin/status",
            headers={"Authorization": "Bearer test-admin-token"},
        )
        assert r.status_code == 200
        body = r.json()
        assert "routes" in body
        assert body["models"] >= 1
