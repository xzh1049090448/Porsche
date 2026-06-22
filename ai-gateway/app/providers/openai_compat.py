"""OpenAI-compatible upstream proxy (OpenAI, Mistral, and other compatible vendors)."""

from __future__ import annotations

from typing import Any, AsyncIterator

import httpx
from loguru import logger

from app.common.exceptions import UpstreamError


async def forward_chat_completions_stream(
    client: httpx.AsyncClient,
    *,
    base_url: str,
    api_key: str,
    payload: dict[str, Any],
    timeout: float,
) -> AsyncIterator[bytes]:
    """Stream raw SSE bytes from upstream (OpenAI-compatible)."""
    url = f"{base_url.rstrip('/')}/chat/completions"
    headers = {
        "Authorization": f"Bearer {api_key}",
        "Accept": "text/event-stream",
    }
    async with client.stream(
        "POST",
        url,
        headers=headers,
        json=payload,
        timeout=httpx.Timeout(timeout),
    ) as resp:
        if resp.status_code >= 400:
            text = (await resp.aread()).decode(errors="replace")
            logger.warning("Upstream stream error {}: {}", resp.status_code, text[:500])
            raise UpstreamError(resp.status_code, text)
        async for chunk in resp.aiter_bytes():
            if chunk:
                yield chunk


async def forward_chat_completions_json(
    client: httpx.AsyncClient,
    *,
    base_url: str,
    api_key: str,
    payload: dict[str, Any],
    timeout: float,
) -> tuple[int, dict[str, Any] | list[Any] | str]:
    """Non-streaming JSON response from upstream."""
    url = f"{base_url.rstrip('/')}/chat/completions"
    headers = {"Authorization": f"Bearer {api_key}"}
    resp = await client.post(
        url,
        headers=headers,
        json=payload,
        timeout=httpx.Timeout(timeout),
    )
    try:
        data = resp.json()
    except Exception:  # noqa: BLE001
        data = {"raw": resp.text}
    return resp.status_code, data
