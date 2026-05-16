"""Round-robin upstream key pool with simple circuit breaker."""

from __future__ import annotations

import itertools
import time
from dataclasses import dataclass, field

from loguru import logger

from app.services.model_registry import ModelRoute, read_keys_from_env


@dataclass
class KeyState:
    failures: int = 0
    open_until: float = 0.0


@dataclass
class UpstreamKeyEntry:
    key_id: str
    secret: str
    state: KeyState = field(default_factory=KeyState)


class UpstreamKeyPool:
    """
    Per-route key rotation with circuit breaker.

    When a key is marked bad (rate limit, auth error), failures increment.
    After ``threshold`` consecutive failures, the key is temporarily skipped.
    """

    def __init__(
        self,
        *,
        failure_threshold: int,
        open_seconds: int,
    ) -> None:
        self._failure_threshold = failure_threshold
        self._open_seconds = open_seconds
        self._cycles: dict[str, itertools.cycle] = {}
        self._entries: dict[str, list[UpstreamKeyEntry]] = {}

    def rebuild(self, routes: dict[str, ModelRoute]) -> None:
        """Rebuild pools from current routes and environment keys."""
        self._cycles.clear()
        self._entries.clear()
        for logical, route in routes.items():
            keys = read_keys_from_env(route.api_keys_env)
            if not keys:
                logger.warning("No upstream keys for model {} (env {})", logical, route.api_keys_env)
                continue
            entries = [
                UpstreamKeyEntry(key_id=f"{logical}:{i}", secret=k) for i, k in enumerate(keys)
            ]
            self._entries[logical] = entries
            self._cycles[logical] = itertools.cycle(range(len(entries)))

    def next_key(self, logical_model: str) -> UpstreamKeyEntry | None:
        entries = self._entries.get(logical_model)
        cycle = self._cycles.get(logical_model)
        if not entries or not cycle:
            return None
        now = time.monotonic()
        n = len(entries)
        for _ in range(n):
            idx = next(cycle)
            entry = entries[idx]
            if entry.state.open_until and entry.state.open_until > now:
                continue
            return entry
        return None

    def report_success(self, entry: UpstreamKeyEntry) -> None:
        entry.state.failures = 0
        entry.state.open_until = 0.0

    def report_failure(self, entry: UpstreamKeyEntry, *, tripped: bool) -> None:
        if tripped:
            entry.state.failures += 1
            if entry.state.failures >= self._failure_threshold:
                entry.state.open_until = time.monotonic() + float(self._open_seconds)
                logger.warning(
                    "Circuit opened for upstream key {} ({}s)",
                    entry.key_id,
                    self._open_seconds,
                )
                entry.state.failures = 0
