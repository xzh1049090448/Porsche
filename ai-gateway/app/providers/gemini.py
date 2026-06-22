"""Google Gemini REST adapter (generateContent)."""

from __future__ import annotations

import json
import time
import uuid
from typing import Any, AsyncIterator

import httpx

from app.common.exceptions import UpstreamError


def _contents_from_openai(messages: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Map OpenAI-style messages to Gemini contents."""
    contents: list[dict[str, Any]] = []
    for m in messages:
        role = str(m.get("role", "user"))
        content = m.get("content")
        if isinstance(content, list):
            text = json.dumps(content, ensure_ascii=False)
        else:
            text = str(content or "")
        if role == "system":
            contents.append({"role": "user", "parts": [{"text": f"[system]\n{text}"}]})
            continue
        g_role = "model" if role == "assistant" else "user"
        contents.append({"role": g_role, "parts": [{"text": text}]})
    return contents


def _gemini_to_openai(data: dict[str, Any], *, logical_model: str) -> dict[str, Any]:
    candidates = data.get("candidates") or []
    text = ""
    finish = "stop"
    if candidates:
        c0 = candidates[0] or {}
        finish = str(c0.get("finishReason", "stop")).lower()
        parts = ((c0.get("content") or {}).get("parts")) or []
        for p in parts:
            if isinstance(p, dict) and "text" in p:
                text += str(p.get("text", ""))
    usage_meta = data.get("usageMetadata") or {}
    prompt_t = int(usage_meta.get("promptTokenCount", 0))
    out_t = int(usage_meta.get("candidatesTokenCount", 0))
    total = int(usage_meta.get("totalTokenCount", prompt_t + out_t))
    created = int(time.time())
    return {
        "id": f"chatcmpl-{uuid.uuid4().hex[:24]}",
        "object": "chat.completion",
        "created": created,
        "model": logical_model,
        "choices": [
            {
                "index": 0,
                "message": {"role": "assistant", "content": text},
                "finish_reason": finish,
            }
        ],
        "usage": {
            "prompt_tokens": prompt_t,
            "completion_tokens": out_t,
            "total_tokens": total,
        },
    }


def _sse(obj: dict[str, Any]) -> bytes:
    return f"data: {json.dumps(obj, ensure_ascii=False)}\n\n".encode("utf-8")


async def gemini_chat_completion_json(
    client: httpx.AsyncClient,
    *,
    api_key: str,
    upstream_model: str,
    openai_body: dict[str, Any],
    logical_model: str,
    timeout: float,
) -> dict[str, Any]:
    contents = _contents_from_openai(openai_body.get("messages") or [])
    url = (
        "https://generativelanguage.googleapis.com/v1beta/models/"
        f"{upstream_model}:generateContent?key={api_key}"
    )
    payload: dict[str, Any] = {"contents": contents}
    temperature = openai_body.get("temperature")
    if temperature is not None:
        payload["generationConfig"] = {"temperature": float(temperature)}
    top_p = openai_body.get("top_p")
    if top_p is not None:
        payload.setdefault("generationConfig", {})
        payload["generationConfig"]["topP"] = float(top_p)
    max_tokens = openai_body.get("max_tokens")
    if max_tokens is not None:
        payload.setdefault("generationConfig", {})
        payload["generationConfig"]["maxOutputTokens"] = int(max_tokens)

    resp = await client.post(
        url,
        json=payload,
        timeout=httpx.Timeout(timeout),
    )
    if resp.status_code >= 400:
        raise UpstreamError(resp.status_code, resp.text)
    data = resp.json()
    return _gemini_to_openai(data, logical_model=logical_model)


async def gemini_fake_stream(
    completion: dict[str, Any],
) -> AsyncIterator[bytes]:
    """Emit a minimal OpenAI-style SSE stream from a non-stream Gemini completion."""
    completion_id = completion["id"]
    created = completion["created"]
    model = completion["model"]
    text = completion["choices"][0]["message"]["content"] or ""
    yield _sse(
        {
            "id": completion_id,
            "object": "chat.completion.chunk",
            "created": created,
            "model": model,
            "choices": [
                {
                    "index": 0,
                    "delta": {"role": "assistant", "content": ""},
                    "finish_reason": None,
                }
            ],
        }
    )
    yield _sse(
        {
            "id": completion_id,
            "object": "chat.completion.chunk",
            "created": created,
            "model": model,
            "choices": [
                {
                    "index": 0,
                    "delta": {"content": text},
                    "finish_reason": None,
                }
            ],
        }
    )
    yield _sse(
        {
            "id": completion_id,
            "object": "chat.completion.chunk",
            "created": created,
            "model": model,
            "choices": [
                {
                    "index": 0,
                    "delta": {},
                    "finish_reason": "stop",
                }
            ],
            "usage": completion.get("usage"),
        }
    )
    yield b"data: [DONE]\n\n"
