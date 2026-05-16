"""Smoke tests for public endpoints."""

from fastapi.testclient import TestClient

from app.main import app


def test_health_ok():
    with TestClient(app) as client:
        r = client.get("/health")
        assert r.status_code == 200
        data = r.json()
        assert data["status"] == "ok"
        assert data["models_loaded"] >= 1


def test_metrics_exposed():
    with TestClient(app) as client:
        r = client.get("/metrics")
        assert r.status_code == 200
        assert b"gateway_requests_total" in r.content
