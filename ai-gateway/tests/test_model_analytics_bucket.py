"""Unit tests for cross-dialect time bucket SQL."""

from sqlalchemy import select
from sqlalchemy.dialects import mysql, sqlite

from app.db.models import UsageRecord
from app.services.model_analytics_service import _align_series_to_buckets, _bucket_expr


def test_bucket_expr_sqlite_uses_strftime():
    bucket = _bucket_expr("sqlite", "2h")
    sql = str(select(bucket).compile(dialect=sqlite.dialect()))
    assert "strftime" in sql
    assert "unixepoch" in sql


def test_bucket_expr_mysql_uses_unix_timestamp():
    bucket = _bucket_expr("mysql", "2h")
    sql = str(select(bucket).compile(dialect=mysql.dialect()))
    upper = sql.upper()
    assert "UNIX_TIMESTAMP" in upper
    assert "FROM_UNIXTIME" in upper


def test_align_series_to_buckets_fills_gaps():
    from datetime import datetime, timezone

    t1 = datetime(2026, 6, 6, 10, 0, tzinfo=timezone.utc)
    t2 = datetime(2026, 6, 6, 12, 0, tzinfo=timezone.utc)
    by_model = {
        "glm-5.1": [{"time": t1, "tokens": 100, "cost": 0.1, "calls": 1, "ratio": 0.0}],
        "deepseek-v4-flash": [
            {"time": t2, "tokens": 200, "cost": 0.2, "calls": 2, "ratio": 0.0}
        ],
    }
    series = _align_series_to_buckets(by_model, [t1, t2])
    assert len(series) == 2
    assert len(series[0]["data"]) == 2
    assert series[0]["data"][1]["tokens"] == 0
    assert series[1]["data"][0]["tokens"] == 0
