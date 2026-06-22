"""SMS verification code service (Redis-backed, dev mode fallback)."""

from __future__ import annotations

import random
import secrets
import time

import redis.asyncio as aioredis
from fastapi import HTTPException
from loguru import logger

from app.config import Settings, get_settings


class SmsService:
  """Send and verify SMS codes."""

  def __init__(self, redis_url: str | None, settings: Settings) -> None:
    self._redis_url = redis_url
    self._settings = settings
    self._redis: aioredis.Redis | None = None
    self._memory: dict[str, tuple[str, float]] = {}
    self._mem_send_phone: dict[str, tuple[int, float]] = {}
    self._mem_send_ip: dict[str, tuple[int, float]] = {}
    self._mem_fail_count: dict[str, tuple[int, float]] = {}

  @staticmethod
  def _normalize_phone(phone: str) -> str:
    return phone.strip()

  @staticmethod
  def _normalize_code(code: str) -> str:
    return code.strip()

  async def connect(self) -> None:
    if not self._redis_url:
      logger.info("SMS codes stored in memory (set REDIS_URL for multi-worker deployments)")
      return
    try:
      self._redis = aioredis.from_url(self._redis_url, decode_responses=True)
      await self._redis.ping()
      logger.info("Redis connected for SMS verification codes")
    except Exception as exc:  # noqa: BLE001
      logger.warning("Redis unavailable for SMS ({}); using in-memory codes", exc)
      self._redis = None

  async def close(self) -> None:
    if self._redis:
      await self._redis.aclose()

  def _generate_code(self) -> str:
    return f"{random.randint(100000, 999999)}"

  async def _incr_window(self, key: str, limit: int, window_seconds: int = 3600) -> int:
    if self._redis:
      count = await self._redis.incr(key)
      if count == 1:
        await self._redis.expire(key, window_seconds)
      return int(count)
    now = time.time()
    store = self._mem_send_phone if key.startswith("sms:send:phone:") else self._mem_send_ip
    count, expires = store.get(key, (0, now + window_seconds))
    if now > expires:
      count, expires = 0, now + window_seconds
    count += 1
    store[key] = (count, expires)
    return count

  async def check_send_allowed(self, phone: str, client_ip: str) -> None:
    phone = self._normalize_phone(phone)
    phone_key = f"sms:send:phone:{phone}"
    ip_key = f"sms:send:ip:{client_ip or 'unknown'}"
    phone_count = await self._incr_window(phone_key, self._settings.sms_send_limit_per_phone)
    if phone_count > self._settings.sms_send_limit_per_phone:
      raise HTTPException(status_code=429, detail="验证码发送过于频繁，请稍后再试")
    ip_count = await self._incr_window(ip_key, self._settings.sms_send_limit_per_ip)
    if ip_count > self._settings.sms_send_limit_per_ip:
      raise HTTPException(status_code=429, detail="请求过于频繁，请稍后再试")

  async def _record_failed_verify(self, phone: str) -> None:
    key = f"sms:fail:{phone}"
    if self._redis:
      count = await self._redis.incr(key)
      if count == 1:
        await self._redis.expire(key, 3600)
    else:
      now = time.time()
      count, expires = self._mem_fail_count.get(key, (0, now + 3600))
      if now > expires:
        count, expires = 0, now + 3600
      count += 1
      self._mem_fail_count[key] = (count, expires)
    if count > self._settings.sms_verify_max_attempts:
      raise HTTPException(status_code=429, detail="验证码尝试次数过多，请重新获取")

  async def _clear_failed_verify(self, phone: str) -> None:
    key = f"sms:fail:{phone}"
    if self._redis:
      await self._redis.delete(key)
    else:
      self._mem_fail_count.pop(key, None)

  async def send_code(self, phone: str) -> str:
    phone = self._normalize_phone(phone)
    code = self._generate_code()
    key = f"sms:code:{phone}"
    if self._redis:
      await self._redis.setex(key, 300, code)
    else:
      self._memory[key] = (code, time.time() + 300)
    logger.info("SMS code sent to {} (dev={})", phone[:3] + "****" + phone[-4:], get_settings().sms_dev_mode)
    return code

  async def verify_code(self, phone: str, code: str) -> bool:
    phone = self._normalize_phone(phone)
    code = self._normalize_code(code)
    key = f"sms:code:{phone}"
    stored: str | None = None
    if self._redis:
      stored = await self._redis.get(key)
      if stored and secrets.compare_digest(stored.strip(), code):
        await self._redis.delete(key)
        await self._clear_failed_verify(phone)
        return True
      await self._record_failed_verify(phone)
      return False

    entry = self._memory.get(key)
    if entry and entry[1] > time.time() and secrets.compare_digest(entry[0].strip(), code):
      del self._memory[key]
      await self._clear_failed_verify(phone)
      return True
    await self._record_failed_verify(phone)
    return False
