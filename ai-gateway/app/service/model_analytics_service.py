"""Model analytics queries over UsageRecord."""

from __future__ import annotations

import csv
import io
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from typing import Any

from fastapi import HTTPException
from sqlalchemy import Integer, cast, func, select
from sqlalchemy.ext.asyncio import AsyncSession

from app.config import Settings
from app.repository.models import UsageRecord, User

GRANULARITY_SECONDS = {"1h": 3600, "2h": 7200, "4h": 14400, "1d": 86400}

RANGE_PRESETS = {
    "1h": (timedelta(hours=1), "近1小时"),
    "6h": (timedelta(hours=6), "近6小时"),
    "24h": (timedelta(hours=24), "近24小时"),
    "yesterday": None,
    "7d": (timedelta(days=7), "近7天"),
}

CHART_VIEWS = frozenset(
    {
        "consumption_distribution",
        "call_trend",
        "call_distribution",
        "call_ranking",
        "user_consumption_ranking",
        "user_consumption_trend",
    }
)


@dataclass
class AnalyticsFilters:
    start_at: datetime
    end_at: datetime
    range_label: str
    granularity: str
    models: list[str]
    top_n: int
    user_id: int | None
    metric: str


def resolve_analytics_admin_phones(settings: Settings) -> set[str]:
    raw = settings.analytics_admin_phones.strip()
    if raw:
        return {p.strip() for p in raw.split(",") if p.strip()}
    if settings.app_env == "development":
        return {settings.fixed_login_phone.strip()}
    return set()


def is_analytics_admin(phone: str, settings: Settings) -> bool:
    return phone.strip() in resolve_analytics_admin_phones(settings)


def mask_phone(phone: str) -> str:
    p = phone.strip()
    if len(p) < 7:
        return p
    return f"{p[:3]}****{p[-4:]}"


def tokens_to_cost(tokens: int, price_per_1k: float) -> float:
    return round(tokens * price_per_1k / 1000, 2)


def parse_filters(
    *,
    range_preset: str,
    start_at: datetime | None,
    end_at: datetime | None,
    granularity: str,
    models: str,
    top_n: int,
    user_id: int | None,
    metric: str,
) -> AnalyticsFilters:
    if granularity not in GRANULARITY_SECONDS:
        raise HTTPException(status_code=400, detail="无效的 granularity")
    if metric not in ("tokens", "cost"):
        raise HTTPException(status_code=400, detail="无效的 metric")
    if top_n < 1 or top_n > 100:
        raise HTTPException(status_code=400, detail="top_n 须在 1–100 之间")

    now = datetime.now(timezone.utc)
    if start_at is not None and end_at is not None:
        if start_at >= end_at:
            raise HTTPException(status_code=400, detail="start_at 须早于 end_at")
        return AnalyticsFilters(
            start_at=start_at,
            end_at=end_at,
            range_label="自定义",
            granularity=granularity,
            models=_parse_models(models),
            top_n=top_n,
            user_id=user_id,
            metric=metric,
        )

    if range_preset not in RANGE_PRESETS:
        raise HTTPException(status_code=400, detail="无效的 range")

    if range_preset == "yesterday":
        today_start = now.replace(hour=0, minute=0, second=0, microsecond=0)
        start = today_start - timedelta(days=1)
        end = today_start
        label = "昨日"
    else:
        delta, label = RANGE_PRESETS[range_preset]  # type: ignore[misc]
        start = now - delta
        end = now

    return AnalyticsFilters(
        start_at=start,
        end_at=end,
        range_label=label,
        granularity=granularity,
        models=_parse_models(models),
        top_n=top_n,
        user_id=user_id,
        metric=metric,
    )


def _parse_models(models: str) -> list[str]:
    if not models.strip():
        return []
    return [m.strip() for m in models.split(",") if m.strip()]


def _dialect_name(db: AsyncSession) -> str:
    bind = db.get_bind()
    return bind.dialect.name if bind is not None else "sqlite"


def _bucket_expr(dialect_name: str, granularity: str):
    """Time bucket expression compatible with SQLite and MySQL/MariaDB."""
    secs = GRANULARITY_SECONDS[granularity]
    if dialect_name == "sqlite":
        ts = cast(func.strftime("%s", UsageRecord.created_at), Integer)
        bucket_ts = cast(ts / secs, Integer) * secs
        return func.datetime(bucket_ts, "unixepoch")
    # mysql, mariadb, and other unix-timestamp dialects
    ts = func.unix_timestamp(UsageRecord.created_at)
    bucket_ts = func.floor(ts / secs) * secs
    return func.from_unixtime(bucket_ts)


def _align_series_to_buckets(
    by_model: dict[str, list[dict]], buckets: list[datetime]
) -> list[dict[str, Any]]:
    """Ensure every model series has one point per bucket (zeros for gaps)."""
    bucket_keys = [b.isoformat() for b in buckets]
    series = []
    for model in sorted(by_model):
        points_by_key = {p["time"].isoformat(): p for p in by_model[model]}
        aligned = []
        for b, key in zip(buckets, bucket_keys):
            if key in points_by_key:
                aligned.append(points_by_key[key])
            else:
                aligned.append(
                    {
                        "time": b,
                        "tokens": 0,
                        "cost": 0.0,
                        "calls": 0,
                        "ratio": 0.0,
                    }
                )
        series.append({"name": model, "data": aligned})
    return series


def _format_time_label(dt: datetime) -> str:
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.strftime("%m-%d %H:%M")


def _ensure_utc(dt: datetime) -> datetime:
    if dt.tzinfo is None:
        return dt.replace(tzinfo=timezone.utc)
    return dt.astimezone(timezone.utc)


def _parse_bucket(value: object) -> datetime:
    if isinstance(value, datetime):
        return _ensure_utc(value)
    text = str(value).strip()
    if " " in text and "T" not in text:
        text = text.replace(" ", "T", 1)
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    if "+" not in text:
        text += "+00:00"
    return _ensure_utc(datetime.fromisoformat(text))


def _base_where(filters: AnalyticsFilters):
    clauses = [
        UsageRecord.created_at >= filters.start_at,
        UsageRecord.created_at < filters.end_at,
    ]
    if filters.models:
        clauses.append(UsageRecord.model.in_(filters.models))
    return clauses


class ModelAnalyticsService:
    def __init__(self, settings: Settings) -> None:
        self._settings = settings
        self._price = settings.analytics_token_price_per_1k

    async def get_summary(self, db: AsyncSession, filters: AnalyticsFilters) -> dict[str, Any]:
        where = _base_where(filters)
        row = await db.execute(
            select(
                func.coalesce(func.sum(UsageRecord.tokens), 0),
                func.count(),
            ).where(*where)
        )
        tokens, calls = row.one()
        tokens = int(tokens)
        return {
            "total_tokens": tokens,
            "total_cost": tokens_to_cost(tokens, self._price),
            "total_calls": int(calls),
            "range_label": filters.range_label,
            "start_at": filters.start_at,
            "end_at": filters.end_at,
            "updated_at": datetime.now(timezone.utc),
        }

    async def list_models(self, db: AsyncSession, filters: AnalyticsFilters) -> dict[str, Any]:
        where = _base_where(filters)
        where.append(UsageRecord.model.isnot(None))
        rows = await db.execute(
            select(
                UsageRecord.model,
                func.coalesce(func.sum(UsageRecord.tokens), 0),
                func.count(),
            )
            .where(*where)
            .group_by(UsageRecord.model)
            .order_by(func.count().desc())
        )
        items = []
        for idx, (model, tokens, calls) in enumerate(rows.all()):
            if not model:
                continue
            items.append(
                {
                    "model": model,
                    "total_tokens": int(tokens),
                    "total_calls": int(calls),
                    "is_top5": idx < 5,
                }
            )
        return {"items": items}

    async def get_chart(
        self, db: AsyncSession, view: str, filters: AnalyticsFilters
    ) -> dict[str, Any]:
        if view not in CHART_VIEWS:
            raise HTTPException(status_code=400, detail="无效的 view")
        if view == "user_consumption_trend" and filters.user_id is None:
            raise HTTPException(status_code=400, detail="user_consumption_trend 需要 user_id")

        builders = {
            "consumption_distribution": self._consumption_distribution,
            "call_trend": self._call_trend,
            "call_distribution": self._call_distribution,
            "call_ranking": self._call_ranking,
            "user_consumption_ranking": self._user_consumption_ranking,
            "user_consumption_trend": self._user_consumption_trend,
        }
        payload = await builders[view](db, filters)
        payload.update(
            {
                "view": view,
                "metric": filters.metric,
                "granularity": filters.granularity,
                "start_at": filters.start_at,
                "end_at": filters.end_at,
            }
        )
        return payload

    async def _consumption_distribution(
        self, db: AsyncSession, filters: AnalyticsFilters
    ) -> dict[str, Any]:
        bucket = _bucket_expr(_dialect_name(db), filters.granularity)
        where = _base_where(filters)
        where.append(UsageRecord.model.isnot(None))
        rows = await db.execute(
            select(
                bucket.label("bucket"),
                UsageRecord.model,
                func.coalesce(func.sum(UsageRecord.tokens), 0),
                func.count(),
            )
            .where(*where)
            .group_by(bucket, UsageRecord.model)
            .order_by(bucket)
        )
        by_model: dict[str, list[dict]] = {}
        buckets: list[datetime] = []
        bucket_set: set[str] = set()
        for bkt, model, tokens, calls in rows.all():
            if not model:
                continue
            dt = _parse_bucket(bkt)
            key = dt.isoformat()
            if key not in bucket_set:
                bucket_set.add(key)
                buckets.append(dt)
            point = {
                "time": dt,
                "tokens": int(tokens),
                "cost": tokens_to_cost(int(tokens), self._price),
                "calls": int(calls),
                "ratio": 0.0,
            }
            by_model.setdefault(model, []).append(point)

        time_labels = [_format_time_label(b) for b in buckets]
        series = _align_series_to_buckets(by_model, buckets)
        return {"time_labels": time_labels, "series": series, "ranking": []}

    async def _call_trend(self, db: AsyncSession, filters: AnalyticsFilters) -> dict[str, Any]:
        bucket = _bucket_expr(_dialect_name(db), filters.granularity)
        where = _base_where(filters)
        rows = await db.execute(
            select(bucket.label("bucket"), func.count())
            .where(*where)
            .group_by(bucket)
            .order_by(bucket)
        )
        data = []
        time_labels = []
        for bkt, calls in rows.all():
            dt = _parse_bucket(bkt)
            time_labels.append(_format_time_label(dt))
            data.append(
                {
                    "time": dt,
                    "tokens": 0,
                    "cost": 0.0,
                    "calls": int(calls),
                    "ratio": 0.0,
                }
            )
        return {
            "time_labels": time_labels,
            "series": [{"name": "calls", "data": data}],
            "ranking": [],
        }

    async def _call_distribution(
        self, db: AsyncSession, filters: AnalyticsFilters
    ) -> dict[str, Any]:
        ranking = await self._model_call_ranking(db, filters, limit=None)
        total_calls = sum(r["calls"] for r in ranking) or 1
        for r in ranking:
            r["ratio"] = round(r["calls"] / total_calls, 4)
        return {"time_labels": [], "series": [], "ranking": ranking}

    async def _call_ranking(self, db: AsyncSession, filters: AnalyticsFilters) -> dict[str, Any]:
        ranking = await self._model_call_ranking(db, filters, limit=filters.top_n)
        total_calls = sum(r["calls"] for r in ranking) or 1
        for r in ranking:
            r["ratio"] = round(r["calls"] / total_calls, 4)
        return {"time_labels": [], "series": [], "ranking": ranking}

    async def _model_call_ranking(
        self, db: AsyncSession, filters: AnalyticsFilters, *, limit: int | None
    ) -> list[dict[str, Any]]:
        where = _base_where(filters)
        where.append(UsageRecord.model.isnot(None))
        q = (
            select(
                UsageRecord.model,
                func.coalesce(func.sum(UsageRecord.tokens), 0),
                func.count(),
            )
            .where(*where)
            .group_by(UsageRecord.model)
            .order_by(func.count().desc())
        )
        if limit is not None:
            q = q.limit(limit)
        rows = await db.execute(q)
        result = []
        for model, tokens, calls in rows.all():
            if not model:
                continue
            result.append(
                {
                    "key": model,
                    "label": model,
                    "tokens": int(tokens),
                    "cost": tokens_to_cost(int(tokens), self._price),
                    "calls": int(calls),
                    "ratio": 0.0,
                }
            )
        return result

    async def _user_consumption_ranking(
        self, db: AsyncSession, filters: AnalyticsFilters
    ) -> dict[str, Any]:
        where = _base_where(filters)
        rows = await db.execute(
            select(
                UsageRecord.user_id,
                User.phone,
                func.coalesce(func.sum(UsageRecord.tokens), 0),
                func.count(),
            )
            .join(User, User.id == UsageRecord.user_id)
            .where(*where)
            .group_by(UsageRecord.user_id, User.phone)
            .order_by(func.sum(UsageRecord.tokens).desc())
            .limit(filters.top_n)
        )
        ranking = []
        total_tokens = 0
        raw = []
        for uid, phone, tokens, calls in rows.all():
            t = int(tokens)
            total_tokens += t
            raw.append((uid, phone, t, int(calls)))
        denom = total_tokens or 1
        for uid, phone, tokens, calls in raw:
            ranking.append(
                {
                    "key": str(uid),
                    "label": mask_phone(phone),
                    "tokens": tokens,
                    "cost": tokens_to_cost(tokens, self._price),
                    "calls": calls,
                    "ratio": round(tokens / denom, 4),
                }
            )
        return {"time_labels": [], "series": [], "ranking": ranking}

    async def _user_consumption_trend(
        self, db: AsyncSession, filters: AnalyticsFilters
    ) -> dict[str, Any]:
        bucket = _bucket_expr(_dialect_name(db), filters.granularity)
        where = _base_where(filters)
        where.append(UsageRecord.user_id == filters.user_id)
        rows = await db.execute(
            select(
                bucket.label("bucket"),
                func.coalesce(func.sum(UsageRecord.tokens), 0),
                func.count(),
            )
            .where(*where)
            .group_by(bucket)
            .order_by(bucket)
        )
        data = []
        time_labels = []
        for bkt, tokens, calls in rows.all():
            dt = _parse_bucket(bkt)
            time_labels.append(_format_time_label(dt))
            t = int(tokens)
            data.append(
                {
                    "time": dt,
                    "tokens": t,
                    "cost": tokens_to_cost(t, self._price),
                    "calls": int(calls),
                    "ratio": 0.0,
                }
            )
        user = await db.get(User, filters.user_id)
        name = mask_phone(user.phone) if user else str(filters.user_id)
        return {
            "time_labels": time_labels,
            "series": [{"name": name, "data": data}],
            "ranking": [],
        }

    async def export_chart(
        self, db: AsyncSession, view: str, filters: AnalyticsFilters
    ) -> tuple[bytes, str]:
        chart = await self.get_chart(db, view, filters)
        rows: list[dict[str, Any]] = []

        if chart.get("series"):
            for s in chart["series"]:
                model_name = s["name"]
                for pt in s["data"]:
                    rows.append(
                        {
                            "time_bucket": pt["time"].isoformat()
                            if isinstance(pt["time"], datetime)
                            else str(pt["time"]),
                            "model": model_name,
                            "tokens": pt.get("tokens", 0),
                            "cost": pt.get("cost", 0.0),
                            "calls": pt.get("calls", 0),
                            "ratio": pt.get("ratio", 0.0),
                        }
                    )
        elif chart.get("ranking"):
            for r in chart["ranking"]:
                rows.append(
                    {
                        "time_bucket": "",
                        "model": r["label"],
                        "tokens": r.get("tokens", 0),
                        "cost": r.get("cost", 0.0),
                        "calls": r.get("calls", 0),
                        "ratio": r.get("ratio", 0.0),
                    }
                )

        filename = f"model-analytics-{view}-{datetime.now(timezone.utc).strftime('%Y%m%d%H%M%S')}.xlsx"
        return self._rows_to_xlsx(rows), filename

    def _rows_to_xlsx(self, rows: list[dict[str, Any]]) -> bytes:
        headers = ["time_bucket", "model", "tokens", "cost", "calls", "ratio"]
        try:
            from openpyxl import Workbook

            wb = Workbook()
            ws = wb.active
            ws.append(headers)
            for row in rows:
                ws.append([row[h] for h in headers])
            buf = io.BytesIO()
            wb.save(buf)
            return buf.getvalue()
        except ImportError:
            buf = io.StringIO()
            writer = csv.DictWriter(buf, fieldnames=headers)
            writer.writeheader()
            writer.writerows(rows)
            return buf.getvalue().encode("utf-8-sig")
