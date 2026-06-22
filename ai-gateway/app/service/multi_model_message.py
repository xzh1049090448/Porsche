"""多模型回复在单条 assistant 消息中的编码格式（前后端约定）。"""

from __future__ import annotations

import json

MULTI_MODEL_MARKER = "__MULTI_MODEL__"


def encode_multi_model_replies(replies: dict[str, str]) -> str:
  return MULTI_MODEL_MARKER + json.dumps(replies, ensure_ascii=False)


def decode_multi_model_replies(content: str) -> dict[str, str] | None:
  if not content or not content.startswith(MULTI_MODEL_MARKER):
    return None
  try:
    data = json.loads(content[len(MULTI_MODEL_MARKER) :])
    if isinstance(data, dict):
      return {str(k): str(v) for k, v in data.items()}
  except json.JSONDecodeError:
    return None
  return None
