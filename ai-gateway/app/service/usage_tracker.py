"""Token usage accounting (daily/monthly) with optional Redis."""

from __future__ import annotations

import asyncio
import calendar
import time
from dataclasses import dataclass

import redis.asyncio as redis
from loguru import logger

from app.service.client_registry import ClientConfig


def _day_key() -> str:
    return time.strftime("%Y%m%d", time.gmtime())


def _month_key() -> str:
    return time.strftime("%Y%m", time.gmtime())


@dataclass
class UsageCheckResult:
    allowed: bool
    reason: str | None = None


class UsageTracker:
    def __init__(self, redis_url: str | None) -> None:
        self._redis_url = redis_url
        self._redis: redis.Redis | None = None
        self._lock = asyncio.Lock()
        self._daily_mem: dict[tuple[str, str], int] = {}
        self._monthly_mem: dict[tuple[str, str], int] = {}

    async def connect(self) -> None:
        if self._redis_url:
            try:
                self._redis = redis.from_url(self._redis_url, decode_responses=True)
                await self._redis.ping()
                logger.info("Redis connected for usage tracking")
            except Exception as exc:  # noqa: BLE001
                logger.warning("Redis unavailable for usage ({}); using memory", exc)
                self._redis = None

    async def close(self) -> None:
        if self._redis is not None:
            await self._redis.aclose()
            self._redis = None

    async def check_before_request(self, client: ClientConfig) -> UsageCheckResult:
        """Optional pre-check when limits are set without increment."""
        if client.daily_token_limit <= 0 and client.monthly_token_limit <= 0:
            return UsageCheckResult(True)
        if self._redis is None:
            return UsageCheckResult(True)
        day = _day_key()
        month = _month_key()
        try:
            if client.daily_token_limit > 0:
                cur = int(await self._redis.get(f"gw:usage:daily:{client.name}:{day}") or 0)
                if cur >= client.daily_token_limit:
                    return UsageCheckResult(False, "daily_token_limit_exceeded")
            if client.monthly_token_limit > 0:
                cur_m = int(await self._redis.get(f"gw:usage:monthly:{client.name}:{month}") or 0)
                if cur_m >= client.monthly_token_limit:
                    return UsageCheckResult(False, "monthly_token_limit_exceeded")
        except Exception as exc:  # noqa: BLE001
            logger.warning("Usage pre-check redis error: {}", exc)
        return UsageCheckResult(True)

    async def record_completion(self, client: ClientConfig, total_tokens: int) -> UsageCheckResult:
        """Increment counters after completion; may signal overage for monitoring."""
        if total_tokens <= 0:
            return UsageCheckResult(True)
        day = _day_key()
        month = _month_key()

        if self._redis is not None:
            try:
                pipe = self._redis.pipeline()
                pipe.incrby(f"gw:usage:daily:{client.name}:{day}", total_tokens)
                pipe.expire(f"gw:usage:daily:{client.name}:{day}", 3 * 24 * 3600)
                pipe.incrby(f"gw:usage:monthly:{client.name}:{month}", total_tokens)
                days_in_month = calendar.monthrange(time.gmtime().tm_year, time.gmtime().tm_mon)[1]
                pipe.expire(f"gw:usage:monthly:{client.name}:{month}", days_in_month * 24 * 3600)
                await pipe.execute()
                if client.daily_token_limit > 0:
                    cur = int(await self._redis.get(f"gw:usage:daily:{client.name}:{day}") or 0)
                    if cur > client.daily_token_limit:
                        return UsageCheckResult(False, "daily_token_limit_exceeded")
                if client.monthly_token_limit > 0:
                    cur_m = int(await self._redis.get(f"gw:usage:monthly:{client.name}:{month}") or 0)
                    if cur_m > client.monthly_token_limit:
                        return UsageCheckResult(False, "monthly_token_limit_exceeded")
                return UsageCheckResult(True)
            except Exception as exc:  # noqa: BLE001
                logger.warning("Usage record redis error: {}", exc)

        async with self._lock:
            dk = (client.name, day)
            mk = (client.name, month)
            self._daily_mem[dk] = self._daily_mem.get(dk, 0) + total_tokens
            self._monthly_mem[mk] = self._monthly_mem.get(mk, 0) + total_tokens
            if client.daily_token_limit > 0 and self._daily_mem[dk] > client.daily_token_limit:
                return UsageCheckResult(False, "daily_token_limit_exceeded")
            if client.monthly_token_limit > 0 and self._monthly_mem[mk] > client.monthly_token_limit:
                return UsageCheckResult(False, "monthly_token_limit_exceeded")
            return UsageCheckResult(True)
