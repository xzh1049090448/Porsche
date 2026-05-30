"""Orchestrates authZ, routing, retries, and upstream calls."""

from __future__ import annotations

import asyncio
import json
import random
from typing import Any, AsyncIterator

import httpx
from fastapi import HTTPException
from loguru import logger

from app.core.exceptions import UpstreamError
from app.observability.metrics import REQUESTS_TOTAL
from app.providers import anthropic as anthropic_provider
from app.providers import gemini as gemini_provider
from app.providers import openai_compat
from app.schemas.openai import ChatCompletionRequest
from app.services.client_registry import ClientConfig
from app.services.model_registry import ModelRoute
from app.services.upstream_pool import UpstreamKeyEntry
from app.state import AppState


def _tripped_by_status(status: int) -> bool:
    return status in (401, 403, 429) or status >= 500


class GatewayService:
    """Unified gateway for /v1/chat/completions."""

    def __init__(self, state: AppState) -> None:
        self._state = state

    def _resolve_route(self, logical_model: str) -> ModelRoute:
        route = self._state.models.get(logical_model)
        if route is None:
            raise HTTPException(status_code=404, detail=f"Unknown model: {logical_model}")
        return route

    def _ensure_client_model(self, client: ClientConfig, logical_model: str) -> None:
        if client.allowed_models is not None and logical_model not in client.allowed_models:
            raise HTTPException(status_code=403, detail="Model not allowed for this client key")

    async def _with_retries(
        self,
        logical_model: str,
        call,
        max_attempts: int = 3,
    ):
        last_exc: Exception | None = None
        for attempt in range(1, max_attempts + 1):
            key_entry = self._state.pool.next_key(logical_model)
            if key_entry is None:
                route = self._state.models.get(logical_model)
                env_name = route.api_keys_env if route else "API_KEYS"
                raise HTTPException(
                    status_code=503,
                    detail=(
                        f"No upstream API keys available for model '{logical_model}'. "
                        f"Configure {env_name} in .env (e.g. DEEPSEEK_API_KEYS=sk-xxx) and restart the gateway."
                    ),
                )
            try:
                return await call(key_entry)
            except UpstreamError as exc:
                last_exc = exc
                self._state.pool.report_failure(key_entry, tripped=_tripped_by_status(exc.status_code))
                if attempt >= max_attempts or exc.status_code in (400, 404):
                    raise
                delay = min(2**attempt + random.random(), 10.0)
                logger.warning("Upstream error {}; retry in {:.2f}s", exc, delay)
                await asyncio.sleep(delay)
            except httpx.HTTPError as exc:
                last_exc = exc
                self._state.pool.report_failure(key_entry, tripped=True)
                if attempt >= max_attempts:
                    raise HTTPException(status_code=502, detail="Upstream transport error") from exc
                delay = min(2**attempt + random.random(), 10.0)
                await asyncio.sleep(delay)
        raise HTTPException(status_code=502, detail=str(last_exc))

    async def complete(
        self,
        *,
        client: ClientConfig,
        body: ChatCompletionRequest,
    ) -> dict[str, Any]:
        logical_model = body.model
        self._ensure_client_model(client, logical_model)
        route = self._resolve_route(logical_model)
        timeout = self._state.settings.upstream_timeout_seconds

        async def _call(key_entry: UpstreamKeyEntry) -> dict[str, Any]:
            payload = body.to_upstream_dict()
            payload["model"] = route.upstream_model
            if route.provider == "openai_compatible":
                if not route.base_url:
                    raise HTTPException(status_code=500, detail="Route missing base_url")
                status, data = await openai_compat.forward_chat_completions_json(
                    self._state.http,
                    base_url=route.base_url,
                    api_key=key_entry.secret,
                    payload=payload,
                    timeout=timeout,
                )
                if status >= 400:
                    raise UpstreamError(status, str(data)[:4000])
                if isinstance(data, dict):
                    data.setdefault("model", logical_model)
                self._state.pool.report_success(key_entry)
                REQUESTS_TOTAL.labels(
                    provider="openai_compatible", model=logical_model, status=str(status)
                ).inc()
                return data  # type: ignore[return-value]
            if route.provider == "anthropic":
                data = await anthropic_provider.anthropic_chat_completion_json(
                    self._state.http,
                    api_key=key_entry.secret,
                    upstream_model=route.upstream_model,
                    openai_body=payload,
                    logical_model=logical_model,
                    timeout=timeout,
                )
                self._state.pool.report_success(key_entry)
                REQUESTS_TOTAL.labels(
                    provider="anthropic", model=logical_model, status="200"
                ).inc()
                return data
            if route.provider == "gemini":
                data = await gemini_provider.gemini_chat_completion_json(
                    self._state.http,
                    api_key=key_entry.secret,
                    upstream_model=route.upstream_model,
                    openai_body=payload,
                    logical_model=logical_model,
                    timeout=timeout,
                )
                self._state.pool.report_success(key_entry)
                REQUESTS_TOTAL.labels(provider="gemini", model=logical_model, status="200").inc()
                return data
            raise HTTPException(status_code=500, detail=f"Unsupported provider: {route.provider}")

        return await self._with_retries(logical_model, _call)

    async def stream(
        self,
        *,
        client: ClientConfig,
        body: ChatCompletionRequest,
    ) -> AsyncIterator[bytes]:
        logical_model = body.model
        self._ensure_client_model(client, logical_model)
        route = self._resolve_route(logical_model)
        timeout = self._state.settings.upstream_timeout_seconds

        async def _gen() -> AsyncIterator[bytes]:
            key_entry = self._state.pool.next_key(logical_model)
            if key_entry is None:
                yield b'data: {"error":{"message":"No upstream keys"}}\n\n'
                return
            payload = body.to_upstream_dict()
            payload["model"] = route.upstream_model
            try:
                if route.provider == "openai_compatible":
                    if not route.base_url:
                        raise UpstreamError(500, "missing base_url")
                    async for chunk in openai_compat.forward_chat_completions_stream(
                        self._state.http,
                        base_url=route.base_url,
                        api_key=key_entry.secret,
                        payload=payload,
                        timeout=timeout,
                    ):
                        yield chunk
                    self._state.pool.report_success(key_entry)
                    REQUESTS_TOTAL.labels(
                        provider="openai_compatible", model=logical_model, status="stream"
                    ).inc()
                    return
                if route.provider == "anthropic":
                    async for chunk in anthropic_provider.anthropic_chat_completion_stream(
                        self._state.http,
                        api_key=key_entry.secret,
                        upstream_model=route.upstream_model,
                        openai_body=payload,
                        logical_model=logical_model,
                        timeout=timeout,
                    ):
                        yield chunk
                    self._state.pool.report_success(key_entry)
                    REQUESTS_TOTAL.labels(
                        provider="anthropic", model=logical_model, status="stream"
                    ).inc()
                    return
                if route.provider == "gemini":
                    # Gemini REST streaming is not implemented; emulate OpenAI SSE from full completion.
                    data = await gemini_provider.gemini_chat_completion_json(
                        self._state.http,
                        api_key=key_entry.secret,
                        upstream_model=route.upstream_model,
                        openai_body=payload,
                        logical_model=logical_model,
                        timeout=timeout,
                    )
                    async for chunk in gemini_provider.gemini_fake_stream(data):
                        yield chunk
                    self._state.pool.report_success(key_entry)
                    REQUESTS_TOTAL.labels(provider="gemini", model=logical_model, status="stream").inc()
                    return
                yield b'data: {"error":{"message":"Unsupported provider for stream"}}\n\n'
            except UpstreamError as exc:
                self._state.pool.report_failure(key_entry, tripped=_tripped_by_status(exc.status_code))
                err = {"error": {"message": exc.detail, "type": "upstream_error", "code": str(exc.status_code)}}

                yield f"data: {json.dumps(err)}\n\n".encode("utf-8")

        async for part in _gen():
            yield part
