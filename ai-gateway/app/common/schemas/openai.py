"""OpenAI Chat Completions compatible request/response schemas (subset)."""

from __future__ import annotations

from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field


class ChatMessage(BaseModel):
    role: str
    content: str | list[dict[str, Any]] | None = None
    name: str | None = None
    tool_calls: list[dict[str, Any]] | None = None
    tool_call_id: str | None = None


class ChatCompletionRequest(BaseModel):
    """Incoming /v1/chat/completions body (fields commonly used by clients)."""

    model_config = ConfigDict(extra="allow")

    model: str
    messages: list[ChatMessage]
    temperature: float | None = None
    top_p: float | None = None
    max_tokens: int | None = None
    stream: bool = False
    stop: str | list[str] | None = None
    user: str | None = None
    tools: list[dict[str, Any]] | None = None
    tool_choice: str | dict[str, Any] | None = None
    response_format: dict[str, Any] | None = None
    seed: int | None = None

    def to_upstream_dict(self) -> dict[str, Any]:
        """Serialize for upstream OpenAI-compatible JSON (drop None)."""
        data = self.model_dump(exclude_none=True)
        return data


class Usage(BaseModel):
    prompt_tokens: int = 0
    completion_tokens: int = 0
    total_tokens: int = 0


class ChatChoiceMessage(BaseModel):
    role: str = "assistant"
    content: str | None = None
    tool_calls: list[dict[str, Any]] | None = None


class ChatChoice(BaseModel):
    index: int = 0
    message: ChatChoiceMessage
    finish_reason: str | None = "stop"


class ChatCompletionResponse(BaseModel):
    id: str
    object: Literal["chat.completion"] = "chat.completion"
    created: int
    model: str
    choices: list[ChatChoice]
    usage: Usage | None = None
