"""Platform chat orchestration: RAG + gateway + persistence."""

from __future__ import annotations

import asyncio
import json
import time
from typing import Any, AsyncIterator
from fastapi import HTTPException
from loguru import logger
from sqlalchemy.ext.asyncio import AsyncSession

from app.db.models import Dataset, DatasetStatus, PlanType, UsageRecord, User
from app.schemas.openai import ChatCompletionRequest, ChatMessage
from app.services.billing_service import BillingService
from app.services.conversation_service import ConversationService
from app.services.gateway import GatewayService
from app.services.rag_engine import DATASET_ATTRIBUTION, RagEngine
from app.state import AppState


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
    client = self._state.clients.get_by_secret(self._state.settings.platform_client_secret)
    if client is None:
      raise HTTPException(status_code=500, detail="Platform internal client not configured")
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
      if ds.access_plans and user.plan_type.value not in ds.access_plans:
        if user.plan_type == PlanType.FREE and "free" not in ds.access_plans:
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

    datasets: list[Dataset] = []
    dataset_used = False
    if dataset_enabled:
      datasets = await self._validate_datasets(db, user, dataset_ids)

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

    client = self._get_platform_client()
    chat_messages = [ChatMessage(**m) for m in rag_messages]
    body = ChatCompletionRequest(
      model=model,
      messages=chat_messages,
      temperature=temperature,
      max_tokens=max_tokens,
      stream=True,
    )

    meta = {
      "dataset_used": dataset_used,
      "dataset_attribution": DATASET_ATTRIBUTION if dataset_used else None,
    }
    yield f"data: {json.dumps({'type': 'meta', **meta}, ensure_ascii=False)}\n\n".encode()

    async for chunk in self._gateway.stream(client=client, body=body):
      yield chunk

  async def compare(
    self,
    db: AsyncSession,
    user: User,
    *,
    models: list[str],
    messages: list[dict],
    temperature: float | None = None,
    max_tokens: int | None = None,
    dataset_enabled: bool = False,
    dataset_ids: list[int] | None = None,
  ) -> list[dict]:
    await self._billing.check_and_consume_call(db, user, count=len(models))

    trimmed = messages
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
        user.total_tokens_used += tokens
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
    return list(results)
