"""Distributed or in-memory rate limiting (RPM) and coarse TPM checks."""

from __future__ import annotations

import asyncio
import time
from dataclasses import dataclass

import redis.asyncio as redis
from loguru import logger

from app.services.client_registry import ClientConfig


def _minute_bucket() -> int:
    return int(time.time() // 60)


def estimate_request_tokens(messages: list[dict]) -> int:
    """Very rough token proxy from serialized message size."""
    total = 0
    for m in messages:
        total += len(repr(m))
    return max(1, int(total * 0.25))


@dataclass
class RateLimitResult:
    allowed: bool
    reason: str | None = None


class RateLimiter:
    """
    Per-client RPM using Redis when ``redis_url`` is set, else asyncio-locked memory.

    TPM uses same minute bucket with estimated request cost added to a counter.
    """

    def __init__(self, redis_url: str | None) -> None:
        self._redis_url = redis_url
        self._redis: redis.Redis | None = None
        self._mem_lock = asyncio.Lock()
        self._mem_rpm: dict[tuple[str, int], int] = {}
        self._mem_tpm: dict[tuple[str, int], int] = {}

    async def connect(self) -> None:
        if self._redis_url:
            try:
                self._redis = redis.from_url(self._redis_url, decode_responses=True)
                await self._redis.ping()
                logger.info("Redis connected for rate limiting")
            except Exception as exc:  # noqa: BLE001
                logger.warning("Redis unavailable ({}); falling back to in-memory limiter", exc)
                self._redis = None

    async def close(self) -> None:
        if self._redis is not None:
            await self._redis.aclose()
            self._redis = None

    async def check_and_consume(
        self,
        client: ClientConfig,
        *,
        estimated_tokens: int,
    ) -> RateLimitResult:
        minute = _minute_bucket()
        key_rpm = f"gw:rpm:{client.name}:{minute}"
        key_tpm = f"gw:tpm:{client.name}:{minute}"

        if self._redis is not None:
            try:
                pipe = self._redis.pipeline()
                pipe.incr(key_rpm, 1)
                pipe.expire(key_rpm, 120)
                pipe.incr(key_tpm, estimated_tokens)
                pipe.expire(key_tpm, 120)
                rpm_count, _, tpm_count, _ = await pipe.execute()
                if rpm_count > client.rpm:
                    return RateLimitResult(False, "rpm_limit_exceeded")
                if tpm_count > client.tpm:
                    return RateLimitResult(False, "tpm_limit_exceeded")
                return RateLimitResult(True)
            except Exception as exc:  # noqa: BLE001
                logger.warning("Redis rate limit error {}; using memory fallback", exc)

        async with self._mem_lock:
            rk = (client.name, minute)
            tk = (client.name, minute)
            self._mem_rpm[rk] = self._mem_rpm.get(rk, 0) + 1
            self._mem_tpm[tk] = self._mem_tpm.get(tk, 0) + estimated_tokens
            if self._mem_rpm[rk] > client.rpm:
                return RateLimitResult(False, "rpm_limit_exceeded")
            if self._mem_tpm[tk] > client.tpm:
                return RateLimitResult(False, "tpm_limit_exceeded")
            return RateLimitResult(True)
