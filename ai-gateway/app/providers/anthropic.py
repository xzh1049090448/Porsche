"""Anthropic Messages API adapter (OpenAI-compatible surface)."""

from __future__ import annotations

import json
import time
import uuid
from typing import Any, AsyncIterator

import httpx

from app.common.exceptions import UpstreamError


ANTHROPIC_URL = "https://api.anthropic.com/v1/messages"


def _messages_from_openai(openai_messages: list[dict[str, Any]]) -> tuple[str | None, list[dict[str, Any]], int]:
    """Split system prompt and Anthropic-style messages; return max_tokens hint."""
    system_chunks: list[str] = []
    out: list[dict[str, Any]] = []
    for m in openai_messages:
        role = str(m.get("role", "user"))
        content = m.get("content")
        if isinstance(content, list):
            text = json.dumps(content, ensure_ascii=False)
        else:
            text = str(content or "")
        if role == "system":
            system_chunks.append(text)
        elif role in ("user", "assistant"):
            out.append({"role": role, "content": text})
        else:
            out.append({"role": "user", "content": f"[{role}] {text}"})
    system = "\n\n".join(system_chunks) if system_chunks else None
    return system, out, 4096


def _build_body(
    upstream_model: str,
    openai_body: dict[str, Any],
    *,
    stream: bool,
) -> dict[str, Any]:
    messages = openai_body.get("messages") or []
    system, anth_msgs, default_max = _messages_from_openai(messages)
    max_tokens = int(openai_body.get("max_tokens") or default_max)
    body: dict[str, Any] = {
        "model": upstream_model,
        "max_tokens": max_tokens,
        "messages": anth_msgs,
        "stream": stream,
    }
    if system:
        body["system"] = system
    temperature = openai_body.get("temperature")
    if temperature is not None:
        body["temperature"] = temperature
    top_p = openai_body.get("top_p")
    if top_p is not None:
        body["top_p"] = top_p
    return body


def _anthropic_to_openai_completion(
    anth: dict[str, Any],
    *,
    logical_model: str,
) -> dict[str, Any]:
    """Map a non-stream Anthropic message response to OpenAI chat.completion shape."""
    text_parts: list[str] = []
    for block in anth.get("content") or []:
        if isinstance(block, dict) and block.get("type") == "text":
            text_parts.append(str(block.get("text", "")))
    text = "".join(text_parts)
    created = int(time.time())
    usage_in = int(anth.get("usage", {}).get("input_tokens", 0))
    usage_out = int(anth.get("usage", {}).get("output_tokens", 0))
    return {
        "id": f"chatcmpl-{uuid.uuid4().hex[:24]}",
        "object": "chat.completion",
        "created": created,
        "model": logical_model,
        "choices": [
            {
                "index": 0,
                "message": {"role": "assistant", "content": text},
                "finish_reason": anth.get("stop_reason") or "stop",
            }
        ],
        "usage": {
            "prompt_tokens": usage_in,
            "completion_tokens": usage_out,
            "total_tokens": usage_in + usage_out,
        },
    }


def _sse(obj: dict[str, Any]) -> bytes:
    return f"data: {json.dumps(obj, ensure_ascii=False)}\n\n".encode("utf-8")


async def anthropic_chat_completion_json(
    client: httpx.AsyncClient,
    *,
    api_key: str,
    upstream_model: str,
    openai_body: dict[str, Any],
    logical_model: str,
    timeout: float,
) -> dict[str, Any]:
    body = _build_body(upstream_model, openai_body, stream=False)
    headers = {
        "x-api-key": api_key,
        "anthropic-version": "2023-06-01",
        "content-type": "application/json",
    }
    resp = await client.post(
        ANTHROPIC_URL,
        headers=headers,
        json=body,
        timeout=httpx.Timeout(timeout),
    )
    if resp.status_code >= 400:
        raise UpstreamError(resp.status_code, resp.text)
    anth = resp.json()
    return _anthropic_to_openai_completion(anth, logical_model=logical_model)


async def anthropic_chat_completion_stream(
    client: httpx.AsyncClient,
    *,
    api_key: str,
    upstream_model: str,
    openai_body: dict[str, Any],
    logical_model: str,
    timeout: float,
) -> AsyncIterator[bytes]:
    """Stream Anthropic SSE re-encoded as OpenAI-compatible chat.completion.chunk SSE."""
    body = _build_body(upstream_model, openai_body, stream=True)
    headers = {
        "x-api-key": api_key,
        "anthropic-version": "2023-06-01",
        "content-type": "application/json",
        "accept": "text/event-stream",
    }
    completion_id = f"chatcmpl-{uuid.uuid4().hex[:24]}"
    created = int(time.time())

    async with client.stream(
        "POST",
        ANTHROPIC_URL,
        headers=headers,
        json=body,
        timeout=httpx.Timeout(timeout),
    ) as resp:
        if resp.status_code >= 400:
            text = (await resp.aread()).decode(errors="replace")
            raise UpstreamError(resp.status_code, text)

        yield _sse(
            {
                "id": completion_id,
                "object": "chat.completion.chunk",
                "created": created,
                "model": logical_model,
                "choices": [
                    {
                        "index": 0,
                        "delta": {"role": "assistant", "content": ""},
                        "finish_reason": None,
                    }
                ],
            }
        )

        buffer = b""
        async for chunk in resp.aiter_bytes():
            buffer += chunk
            while b"\n\n" in buffer:
                frame, buffer = buffer.split(b"\n\n", 1)
                text_frame = frame.decode("utf-8", errors="ignore")
                event_type = None
                data_line = None
                for line in text_frame.split("\n"):
                    if line.startswith("event:"):
                        event_type = line[len("event:") :].strip()
                    elif line.startswith("data:"):
                        data_line = line[len("data:") :].strip()
                if not data_line or data_line == "[DONE]":
                    continue
                try:
                    payload = json.loads(data_line)
                except json.JSONDecodeError:
                    continue
                if event_type == "content_block_delta":
                    delta = payload.get("delta") or {}
                    if delta.get("type") == "text_delta":
                        piece = str(delta.get("text", ""))
                        if piece:
                            yield _sse(
                                {
                                    "id": completion_id,
                                    "object": "chat.completion.chunk",
                                    "created": created,
                                    "model": logical_model,
                                    "choices": [
                                        {
                                            "index": 0,
                                            "delta": {"content": piece},
                                            "finish_reason": None,
                                        }
                                    ],
                                }
                            )
                elif event_type == "message_delta":
                    usage = payload.get("usage") or {}
                    out_tok = usage.get("output_tokens")
                    if out_tok is not None:
                        yield _sse(
                            {
                                "id": completion_id,
                                "object": "chat.completion.chunk",
                                "created": created,
                                "model": logical_model,
                                "choices": [
                                    {
                                        "index": 0,
                                        "delta": {},
                                        "finish_reason": payload.get("delta", {}).get("stop_reason"),
                                    }
                                ],
                                "usage": {
                                    "completion_tokens": int(out_tok),
                                    "total_tokens": int(out_tok),
                                    "prompt_tokens": 0,
                                },
                            }
                        )

        yield _sse(
            {
                "id": completion_id,
                "object": "chat.completion.chunk",
                "created": created,
                "model": logical_model,
                "choices": [
                    {
                        "index": 0,
                        "delta": {},
                        "finish_reason": "stop",
                    }
                ],
            }
        )
        yield b"data: [DONE]\n\n"
