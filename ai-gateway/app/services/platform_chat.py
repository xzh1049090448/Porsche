"""Platform chat orchestration: RAG + gateway + persistence."""

from __future__ import annotations

import asyncio
import json
import time
from typing import Any, AsyncIterator
from fastapi import HTTPException
from loguru import logger
from sqlalchemy.ext.asyncio import AsyncSession

from app.db.enum_utils import enum_is, enum_value
from app.db.models import Dataset, DatasetStatus, PlanType, UsageRecord, User
from app.schemas.openai import ChatCompletionRequest, ChatMessage
from app.services.billing_service import BillingService
from app.services.conversation_service import ConversationService
from app.services.gateway import GatewayService
from app.services.multi_model_message import encode_multi_model_replies
from app.services.rag_engine import DATASET_ATTRIBUTION, RagEngine
from app.state import AppState


def _estimate_text_tokens(text: str) -> int:
  """上游未返回 usage 时，按字符数粗估 token。"""
  if not text or not text.strip():
    return 0
  return max(1, len(text) // 2)


def _parse_sse_chunk(chunk: bytes) -> tuple[str, str | None, int]:
  """Extract text delta, optional error, and usage.total_tokens from one SSE chunk."""
  delta_parts: list[str] = []
  error: str | None = None
  usage_tokens = 0
  text = chunk.decode("utf-8", errors="ignore")
  for line in text.split("\n"):
    if not line.startswith("data:"):
      continue
    payload = line[5:].strip()
    if not payload or payload == "[DONE]":
      continue
    try:
      data = json.loads(payload)
    except json.JSONDecodeError:
      continue
    if not isinstance(data, dict):
      continue
    if data.get("type") == "done":
      usage_tokens = int(data.get("tokens") or 0)
      continue
    if data.get("error"):
      err = data["error"]
      error = err if isinstance(err, str) else str(err.get("message", err))
      continue
    usage = data.get("usage")
    if isinstance(usage, dict):
      reported = int(usage.get("total_tokens") or 0)
      if reported:
        usage_tokens = reported
    choices = data.get("choices")
    if choices:
      delta = choices[0].get("delta", {}).get("content")
      if delta:
        delta_parts.append(delta)
  return "".join(delta_parts), error, usage_tokens


def _platform_done_line(tokens: int, total_tokens_used: int) -> bytes:
  payload = {
    "type": "done",
    "tokens": tokens,
    "total_tokens_used": total_tokens_used,
  }
  return f"data: {json.dumps(payload, ensure_ascii=False)}\n\n".encode()


def _compare_sse_line(payload: dict) -> bytes:
  return f"data: {json.dumps(payload, ensure_ascii=False)}\n\n".encode()


class PlatformChatService:
  def __init__(
    self,
    state: AppState,
    rag: RagEngine,
    billing: BillingService,
  ) -> None:
    self._state = state
    self._rag = rag
    self._billing = billing
    self._gateway = GatewayService(state)

  def _get_platform_client(self):
    secret = self._state.settings.platform_client_secret
    client = self._state.clients.get_by_secret(secret)
    if client is None:
      path = self._state.settings.clients_config_path
      raise HTTPException(
        status_code=500,
        detail=(
          "Platform internal client not configured: PLATFORM_CLIENT_SECRET 与 "
          f"{path} 中任一 client 的 secret 不一致，或未加载 clients.yaml。"
          "请确保存在 platform-internal 且 secret 与 .env 相同，然后热加载或重启。"
        ),
      )
    return client

  async def _validate_datasets(
    self, db: AsyncSession, user: User, dataset_ids: list[int] | None
  ) -> list[Dataset]:
    if not dataset_ids:
      return []
    datasets = []
    for ds_id in dataset_ids:
      ds = await db.get(Dataset, ds_id)
      if not ds or ds.status != DatasetStatus.ACTIVE:
        raise HTTPException(status_code=400, detail=f"数据集 {ds_id} 不可用")
      if user.allowed_datasets and ds_id not in user.allowed_datasets:
        raise HTTPException(status_code=403, detail=f"无权访问数据集 {ds_id}")
      if ds.access_plans and enum_value(user.plan_type) not in ds.access_plans:
        if enum_is(user.plan_type, PlanType.FREE) and "free" not in ds.access_plans:
          raise HTTPException(status_code=403, detail=f"当前套餐无法访问数据集 {ds.name}")
      datasets.append(ds)
    return datasets

  async def chat(
    self,
    db: AsyncSession,
    user: User,
    *,
    model: str,
    messages: list[dict],
    conversation_id: int | None = None,
    temperature: float | None = None,
    max_tokens: int | None = None,
    context_window: int | None = None,
    dataset_enabled: bool = False,
    dataset_ids: list[int] | None = None,
  ) -> dict[str, Any]:
    await self._billing.check_and_consume_call(db, user)

    if user.allowed_models and model not in user.allowed_models:
      raise HTTPException(status_code=403, detail="当前账号无权使用该模型")

    datasets: list[Dataset] = []
    dataset_used = False
    if dataset_enabled:
      datasets = await self._validate_datasets(db, user, dataset_ids)
      if not datasets:
        raise HTTPException(status_code=400, detail="启用数据集时必须选择至少一个子数据集")

    trimmed = ConversationService.trim_messages(messages, context_window)
    query = ""
    for m in reversed(trimmed):
      if m.get("role") == "user":
        query = str(m.get("content", ""))
        break

    rag_messages = trimmed
    if dataset_enabled and datasets:
      ds_ids = [d.id for d in datasets]
      rag_messages, dataset_used = self._rag.build_rag_messages(trimmed, ds_ids, query)

    if conversation_id:
      conv = await ConversationService.get(db, user, conversation_id)
    else:
      conv = await ConversationService.create(
        db,
        user,
        model=model,
        dataset_enabled=dataset_enabled,
        dataset_ids=dataset_ids,
      )

    last_user = trimmed[-1] if trimmed else None
    if last_user and last_user.get("role") == "user":
      await ConversationService.add_message(
        db, conv, role="user", content=str(last_user.get("content", ""))
      )

    client = self._get_platform_client()
    chat_messages = [ChatMessage(**m) for m in rag_messages]
    body = ChatCompletionRequest(
      model=model,
      messages=chat_messages,
      temperature=temperature,
      max_tokens=max_tokens,
      stream=False,
    )

    data = await self._gateway.complete(client=client, body=body)
    content = ""
    tokens = 0
    if isinstance(data, dict):
      choices = data.get("choices", [])
      if choices:
        content = choices[0].get("message", {}).get("content", "") or ""
      usage = data.get("usage", {})
      tokens = int(usage.get("total_tokens", 0))

    attribution = DATASET_ATTRIBUTION if dataset_used else None
    await ConversationService.add_message(
      db,
      conv,
      role="assistant",
      content=content,
      model=model,
      dataset_used=dataset_used,
      dataset_attribution=attribution,
      tokens=tokens,
    )

    user.total_tokens_used += tokens
    if dataset_used:
      user.dataset_calls += 1
    db.add(UsageRecord(user_id=user.id, record_type="chat", tokens=tokens, model=model))

    return {
      "conversation_id": conv.id,
      "model": model,
      "content": content,
      "dataset_used": dataset_used,
      "dataset_attribution": attribution,
      "usage": {"total_tokens": tokens},
    }

  async def stream(
    self,
    db: AsyncSession,
    user: User,
    *,
    model: str,
    messages: list[dict],
    conversation_id: int | None = None,
    temperature: float | None = None,
    max_tokens: int | None = None,
    context_window: int | None = None,
    dataset_enabled: bool = False,
    dataset_ids: list[int] | None = None,
  ) -> AsyncIterator[bytes]:
    await self._billing.check_and_consume_call(db, user)

    if user.allowed_models and model not in user.allowed_models:
      raise HTTPException(status_code=403, detail="当前账号无权使用该模型")

    datasets: list[Dataset] = []
    dataset_used = False
    if dataset_enabled:
      datasets = await self._validate_datasets(db, user, dataset_ids)
      if not datasets:
        raise HTTPException(status_code=400, detail="启用数据集时必须选择至少一个子数据集")

    trimmed = ConversationService.trim_messages(messages, context_window)
    query = ""
    for m in reversed(trimmed):
      if m.get("role") == "user":
        query = str(m.get("content", ""))
        break

    rag_messages = trimmed
    if dataset_enabled and datasets:
      ds_ids = [d.id for d in datasets]
      rag_messages, dataset_used = self._rag.build_rag_messages(trimmed, ds_ids, query)

    if conversation_id:
      conv = await ConversationService.get(db, user, conversation_id)
    else:
      conv = await ConversationService.create(
        db,
        user,
        model=model,
        dataset_enabled=dataset_enabled,
        dataset_ids=dataset_ids,
      )

    last_user = trimmed[-1] if trimmed else None
    if last_user and last_user.get("role") == "user":
      user_content = str(last_user.get("content", ""))
      await ConversationService.add_message(
        db, conv, role="user", content=user_content
      )
      if conv.title == "新对话" and user_content.strip():
        conv.title = user_content.strip()[:24] or "新对话"

    await db.flush()

    client = self._get_platform_client()
    chat_messages = [ChatMessage(**m) for m in rag_messages]
    body = ChatCompletionRequest(
      model=model,
      messages=chat_messages,
      temperature=temperature,
      max_tokens=max_tokens,
      stream=True,
    )

    attribution = DATASET_ATTRIBUTION if dataset_used else None
    meta = {
      "type": "meta",
      "conversation_id": conv.id,
      "dataset_used": dataset_used,
      "dataset_attribution": attribution,
    }
    yield f"data: {json.dumps(meta, ensure_ascii=False)}\n\n".encode()

    content_parts: list[str] = []
    stream_error: str | None = None
    stream_tokens = 0

    async for chunk in self._gateway.stream(client=client, body=body):
      delta, err, chunk_tokens = _parse_sse_chunk(chunk)
      if delta:
        content_parts.append(delta)
      if err:
        stream_error = err
      if chunk_tokens:
        stream_tokens = chunk_tokens
      yield chunk

    content = "".join(content_parts)
    if stream_error and not content:
      content = f"[错误] {stream_error}"

    if stream_tokens <= 0 and content and not content.startswith("[错误]"):
      stream_tokens = _estimate_text_tokens(content)

    await ConversationService.add_message(
      db,
      conv,
      role="assistant",
      content=content,
      model=model,
      dataset_used=dataset_used,
      dataset_attribution=attribution,
      tokens=stream_tokens,
    )
    if stream_tokens > 0:
      user.total_tokens_used += stream_tokens
    if dataset_used:
      user.dataset_calls += 1
    db.add(
      UsageRecord(
        user_id=user.id,
        record_type="chat",
        tokens=stream_tokens,
        model=model,
      )
    )
    await db.flush()
    yield _platform_done_line(stream_tokens, int(user.total_tokens_used or 0))

  async def compare(
    self,
    db: AsyncSession,
    user: User,
    *,
    models: list[str],
    messages: list[dict],
    conversation_id: int | None = None,
    temperature: float | None = None,
    max_tokens: int | None = None,
    context_window: int | None = None,
    dataset_enabled: bool = False,
    dataset_ids: list[int] | None = None,
  ) -> dict:
    await self._billing.check_and_consume_call(db, user, count=len(models))

    trimmed = ConversationService.trim_messages(messages, context_window)
    query = ""
    for m in reversed(trimmed):
      if m.get("role") == "user":
        query = str(m.get("content", ""))
        break

    rag_messages = trimmed
    dataset_used = False
    if dataset_enabled:
      datasets = await self._validate_datasets(db, user, dataset_ids)
      if datasets:
        ds_ids = [d.id for d in datasets]
        rag_messages, dataset_used = self._rag.build_rag_messages(trimmed, ds_ids, query)

    client = self._get_platform_client()
    chat_messages = [ChatMessage(**m) for m in rag_messages]

    async def _call_one(model_name: str) -> dict:
      t0 = time.perf_counter()
      try:
        if user.allowed_models and model_name not in user.allowed_models:
          raise HTTPException(status_code=403, detail="当前账号无权使用该模型")
        body = ChatCompletionRequest(
          model=model_name,
          messages=chat_messages,
          temperature=temperature,
          max_tokens=max_tokens,
          stream=False,
        )
        data = await self._gateway.complete(client=client, body=body)
        content = ""
        tokens = 0
        if isinstance(data, dict):
          choices = data.get("choices", [])
          if choices:
            content = choices[0].get("message", {}).get("content", "") or ""
          usage = data.get("usage", {})
          tokens = int(usage.get("total_tokens", 0))
        return {
          "model": model_name,
          "content": content,
          "error": None,
          "tokens": tokens,
          "latency_ms": round((time.perf_counter() - t0) * 1000, 2),
        }
      except HTTPException as exc:
        return {
          "model": model_name,
          "content": None,
          "error": exc.detail,
          "tokens": 0,
          "latency_ms": round((time.perf_counter() - t0) * 1000, 2),
        }
      except Exception as exc:  # noqa: BLE001
        logger.exception("Compare failed for model {}", model_name)
        return {
          "model": model_name,
          "content": None,
          "error": str(exc),
          "tokens": 0,
          "latency_ms": round((time.perf_counter() - t0) * 1000, 2),
        }

    results = await asyncio.gather(*[_call_one(m) for m in models])
    if dataset_used:
      user.dataset_calls += 1

    conv_id = await self._persist_compare_exchange(
      db,
      user,
      models=models,
      trimmed=trimmed,
      results=list(results),
      conversation_id=conversation_id,
      dataset_enabled=dataset_enabled,
      dataset_ids=dataset_ids,
      dataset_used=dataset_used,
    )

    return {"results": list(results), "conversation_id": conv_id}

  async def compare_stream(
    self,
    db: AsyncSession,
    user: User,
    *,
    models: list[str],
    messages: list[dict],
    conversation_id: int | None = None,
    temperature: float | None = None,
    max_tokens: int | None = None,
    context_window: int | None = None,
    dataset_enabled: bool = False,
    dataset_ids: list[int] | None = None,
  ) -> AsyncIterator[bytes]:
    """多模型对比 SSE：各模型并行流式输出，逐段推送 model_chunk。"""
    await self._billing.check_and_consume_call(db, user, count=len(models))

    trimmed = ConversationService.trim_messages(messages, context_window)
    query = ""
    for m in reversed(trimmed):
      if m.get("role") == "user":
        query = str(m.get("content", ""))
        break

    rag_messages = trimmed
    dataset_used = False
    if dataset_enabled:
      datasets = await self._validate_datasets(db, user, dataset_ids)
      if datasets:
        ds_ids = [d.id for d in datasets]
        rag_messages, dataset_used = self._rag.build_rag_messages(trimmed, ds_ids, query)

    client = self._get_platform_client()
    chat_messages = [ChatMessage(**m) for m in rag_messages]
    out_queue: asyncio.Queue = asyncio.Queue()
    results_by_model: dict[str, dict] = {}

    async def _pump_model(model_name: str) -> None:
      t0 = time.perf_counter()
      parts: list[str] = []
      try:
        if user.allowed_models and model_name not in user.allowed_models:
          raise HTTPException(status_code=403, detail="当前账号无权使用该模型")
        body = ChatCompletionRequest(
          model=model_name,
          messages=chat_messages,
          temperature=temperature,
          max_tokens=max_tokens,
          stream=True,
        )
        stream_error: str | None = None
        stream_tokens = 0
        async for chunk in self._gateway.stream(client=client, body=body):
          delta, err, chunk_tokens = _parse_sse_chunk(chunk)
          if delta:
            parts.append(delta)
            await out_queue.put(
              _compare_sse_line(
                {"type": "model_chunk", "model": model_name, "delta": delta}
              )
            )
          if err:
            stream_error = err
          if chunk_tokens:
            stream_tokens = chunk_tokens
        content = "".join(parts)
        if stream_error and not content:
          content = f"[错误] {stream_error}"
        latency = round((time.perf_counter() - t0) * 1000, 2)
        tokens = stream_tokens
        if tokens <= 0 and content and not content.startswith("[错误]"):
          tokens = _estimate_text_tokens(content)
        if content.startswith("[错误]"):
          results_by_model[model_name] = {
            "model": model_name,
            "content": None,
            "error": content[4:].strip(),
            "tokens": 0,
            "latency_ms": latency,
          }
        else:
          results_by_model[model_name] = {
            "model": model_name,
            "content": content,
            "error": None,
            "tokens": tokens,
            "latency_ms": latency,
          }
      except HTTPException as exc:
        detail = exc.detail if isinstance(exc.detail, str) else str(exc.detail)
        results_by_model[model_name] = {
          "model": model_name,
          "content": None,
          "error": detail,
          "tokens": 0,
          "latency_ms": round((time.perf_counter() - t0) * 1000, 2),
        }
        await out_queue.put(
          _compare_sse_line(
            {
              "type": "model_chunk",
              "model": model_name,
              "delta": f"[错误] {detail}",
            }
          )
        )
      except Exception as exc:  # noqa: BLE001
        logger.exception("Compare stream failed for model {}", model_name)
        msg = str(exc)
        results_by_model[model_name] = {
          "model": model_name,
          "content": None,
          "error": msg,
          "tokens": 0,
          "latency_ms": round((time.perf_counter() - t0) * 1000, 2),
        }
        await out_queue.put(
          _compare_sse_line(
            {"type": "model_chunk", "model": model_name, "delta": f"[错误] {msg}"}
          )
        )
      finally:
        await out_queue.put(None)

    for model_name in models:
      asyncio.create_task(_pump_model(model_name))

    finished = 0
    while finished < len(models):
      item = await out_queue.get()
      if item is None:
        finished += 1
        continue
      yield item

    if dataset_used:
      user.dataset_calls += 1

    results = [results_by_model[m] for m in models if m in results_by_model]
    total_tokens = sum(int(r.get("tokens", 0)) for r in results)
    conv_id = await self._persist_compare_exchange(
      db,
      user,
      models=models,
      trimmed=trimmed,
      results=results,
      conversation_id=conversation_id,
      dataset_enabled=dataset_enabled,
      dataset_ids=dataset_ids,
      dataset_used=dataset_used,
    )

    attribution = DATASET_ATTRIBUTION if dataset_used else None
    done = {
      "type": "done",
      "conversation_id": conv_id,
      "dataset_attribution": attribution,
      "dataset_used": dataset_used,
      "tokens": total_tokens,
      "total_tokens_used": int(user.total_tokens_used or 0),
    }
    yield _compare_sse_line(done)

  async def _persist_compare_exchange(
    self,
    db: AsyncSession,
    user: User,
    *,
    models: list[str],
    trimmed: list[dict],
    results: list[dict],
    conversation_id: int | None,
    dataset_enabled: bool,
    dataset_ids: list[int] | None,
    dataset_used: bool,
  ) -> int | None:
    replies: dict[str, str] = {}
    total_tokens = 0
    for r in results:
      model_name = r["model"]
      total_tokens += int(r.get("tokens", 0))
      if r["error"]:
        replies[model_name] = f"[错误] {r['error']}"
      else:
        replies[model_name] = r["content"] or ""

    conv_id = conversation_id
    if conversation_id is not None or trimmed:
      primary_model = models[0]
      if conversation_id:
        conv = await ConversationService.get(db, user, conversation_id)
      else:
        conv = await ConversationService.create(
          db,
          user,
          model=primary_model,
          dataset_enabled=dataset_enabled,
          dataset_ids=dataset_ids,
        )
      conv_id = conv.id

      last_user = trimmed[-1] if trimmed else None
      if last_user and last_user.get("role") == "user":
        user_content = str(last_user.get("content", ""))
        await ConversationService.add_message(
          db, conv, role="user", content=user_content
        )
        if conv.title == "新对话" and user_content.strip():
          conv.title = user_content.strip()[:24] or "新对话"

      attribution = DATASET_ATTRIBUTION if dataset_used else None
      await ConversationService.add_message(
        db,
        conv,
        role="assistant",
        content=encode_multi_model_replies(replies),
        model=primary_model,
        dataset_used=dataset_used,
        dataset_attribution=attribution,
        tokens=total_tokens,
      )
      if total_tokens > 0:
        user.total_tokens_used += total_tokens
      for r in results:
        db.add(
          UsageRecord(
            user_id=user.id,
            record_type="chat",
            tokens=int(r.get("tokens", 0)),
            model=r["model"],
          )
        )
      await db.flush()

    return conv_id
