"""OpenAI 兼容的对话网关（下游 API 客户端使用）。

前缀: ``/v1``

需客户端 API Key 鉴权（``clients.yaml`` 中的 secret）；支持 RPM/TPM 限流与用量统计。
"""

from __future__ import annotations

import json
import time
from typing import Annotated, AsyncIterator

from fastapi import APIRouter, Depends, HTTPException, Request
from fastapi.responses import JSONResponse, StreamingResponse
from loguru import logger

from app.api.deps import get_client_config, get_state
from app.core.errors import error_body
from app.observability.metrics import REQUEST_LATENCY, TOKENS_TOTAL
from app.schemas.openai import ChatCompletionRequest
from app.services.client_registry import ClientConfig
from app.services.gateway import GatewayService
from app.services.rate_limiter import estimate_request_tokens
from app.state import AppState

router = APIRouter(prefix="/v1", tags=["openai"])


@router.post("/chat/completions")
async def chat_completions(
    request: Request,
    body: ChatCompletionRequest,
    client: Annotated[ClientConfig, Depends(get_client_config)],
    state: Annotated[AppState, Depends(get_state)],
):
    """OpenAI 兼容 Chat Completions 接口。

    - 请求/响应格式与 OpenAI API 一致
    - ``stream=true`` 时返回 ``text/event-stream`` SSE
    - 按客户端配置做模型白名单、IP 限制、RPM/TPM 与 Token 配额校验
    """
    messages_dict = [m.model_dump(exclude_none=True) for m in body.messages]
    est = estimate_request_tokens(messages_dict)

    usage_pre = await state.usage_tracker.check_before_request(client)
    if not usage_pre.allowed:
        return JSONResponse(
            status_code=429,
            content=error_body("Token quota exceeded", code=usage_pre.reason),
        )

    rl = await state.rate_limiter.check_and_consume(client, estimated_tokens=est)
    if not rl.allowed:
        return JSONResponse(
            status_code=429,
            content=error_body("Rate limit exceeded", code=rl.reason),
        )

    gateway = GatewayService(state)
    request_id = request.headers.get("x-request-id") or ""

    if body.stream:
        async def event_stream() -> AsyncIterator[bytes]:
            t0 = time.perf_counter()
            bytes_seen = 0
            try:
                async for chunk in gateway.stream(client=client, body=body):
                    bytes_seen += len(chunk)
                    yield chunk
            except HTTPException:
                raise
            except Exception as exc:  # noqa: BLE001
                logger.exception("Stream failed: {}", exc)
                payload = error_body("Stream interrupted", code="stream_error")
                yield f"data: {json.dumps(payload)}\n\n".encode("utf-8")
            finally:
                approx_tokens = max(est, bytes_seen // 8)
                over = await state.usage_tracker.record_completion(client, approx_tokens)
                if not over.allowed:
                    logger.warning("Client {} exceeded token limits post-stream", client.name)
                TOKENS_TOTAL.labels(direction="approx", client=client.name).inc(approx_tokens)
                REQUEST_LATENCY.labels(provider="stream").observe(time.perf_counter() - t0)
                if request_id:
                    logger.info("request_id={} client={} stream_bytes={}", request_id, client.name, bytes_seen)

        return StreamingResponse(event_stream(), media_type="text/event-stream")

    t0 = time.perf_counter()
    try:
        data = await gateway.complete(client=client, body=body)
    except HTTPException:
        raise
    except Exception as exc:  # noqa: BLE001
        logger.exception("Completion failed: {}", exc)
        return JSONResponse(
            status_code=502,
            content=error_body(str(exc), code="gateway_error"),
        )

    REQUEST_LATENCY.labels(provider="complete").observe(time.perf_counter() - t0)
    usage = data.get("usage") if isinstance(data, dict) else None
    total = 0
    if isinstance(usage, dict):
        total = int(usage.get("total_tokens") or 0)
    if not total:
        total = est

    over = await state.usage_tracker.record_completion(client, total)
    if not over.allowed:
        logger.warning("Client {} exceeded token limits after response", client.name)

    TOKENS_TOTAL.labels(direction="total", client=client.name).inc(total)
    if request_id:
        logger.info("request_id={} client={} total_tokens={}", request_id, client.name, total)

    return data
