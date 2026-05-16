"""Downstream client registry (API keys, quotas, IP allowlist)."""

from __future__ import annotations

import asyncio
import ipaddress
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import yaml
from loguru import logger


@dataclass(frozen=True)
class ClientConfig:
    name: str
    secret: str
    allowed_models: frozenset[str] | None
    rpm: int
    tpm: int
    daily_token_limit: int
    monthly_token_limit: int
    ip_allowlist: frozenset[str]


class ClientRegistry:
    def __init__(self, config_path: str) -> None:
        self._path = Path(config_path)
        self._lock = asyncio.Lock()
        self._by_secret: dict[str, ClientConfig] = {}

    def load_sync(self) -> None:
        if not self._path.is_file():
            logger.warning("Clients config not found: {}", self._path)
            self._by_secret = {}
            return
        raw = yaml.safe_load(self._path.read_text(encoding="utf-8")) or {}
        clients_raw: list[Any] = raw.get("clients") or []
        by_secret: dict[str, ClientConfig] = {}
        for c in clients_raw:
            if not isinstance(c, dict):
                continue
            name = str(c.get("name", "unnamed"))
            secret = str(c.get("secret", "")).strip()
            if not secret:
                continue
            models = c.get("allowed_models")
            allowed: frozenset[str] | None
            if isinstance(models, list) and models:
                allowed = frozenset(str(m) for m in models)
            else:
                allowed = None
            rpm = int(c.get("rpm") or 0) or 10_000
            tpm = int(c.get("tpm") or 0) or 1_000_000
            daily = int(c.get("daily_token_limit") or 0)
            monthly = int(c.get("monthly_token_limit") or 0)
            ips_raw = c.get("ip_allowlist") or []
            ip_allow = frozenset(str(x).strip() for x in ips_raw if str(x).strip())
            by_secret[secret] = ClientConfig(
                name=name,
                secret=secret,
                allowed_models=allowed,
                rpm=rpm,
                tpm=tpm,
                daily_token_limit=daily,
                monthly_token_limit=monthly,
                ip_allowlist=ip_allow,
            )
        self._by_secret = by_secret
        logger.info("Loaded {} downstream clients from {}", len(by_secret), self._path)

    @property
    def client_count(self) -> int:
        return len(self._by_secret)

    async def reload(self) -> None:
        async with self._lock:
            await asyncio.to_thread(self.load_sync)

    def get_by_secret(self, secret: str) -> ClientConfig | None:
        return self._by_secret.get(secret)

    @staticmethod
    def ip_allowed(client: ClientConfig, client_host: str) -> bool:
        if not client.ip_allowlist:
            return True
        host = client_host.split("%")[0]
        try:
            ip_obj = ipaddress.ip_address(host)
        except ValueError:
            return False
        for rule in client.ip_allowlist:
            try:
                if "/" in rule:
                    if ip_obj in ipaddress.ip_network(rule, strict=False):
                        return True
                elif ip_obj == ipaddress.ip_address(rule):
                    return True
            except ValueError:
                continue
        return False
