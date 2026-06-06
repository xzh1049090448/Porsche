"""Pydantic schemas for model analytics API."""

from __future__ import annotations

from datetime import datetime

from pydantic import BaseModel, Field


class AnalyticsAccessResponse(BaseModel):
    allowed: bool


class AnalyticsSummaryResponse(BaseModel):
    total_tokens: int
    total_cost: float
    total_calls: int
    range_label: str
    start_at: datetime
    end_at: datetime
    updated_at: datetime


class ModelFilterItem(BaseModel):
    model: str
    total_tokens: int
    total_calls: int
    is_top5: bool


class ModelsListResponse(BaseModel):
    items: list[ModelFilterItem]


class SeriesDataPoint(BaseModel):
    time: datetime
    tokens: int = 0
    cost: float = 0.0
    calls: int = 0
    ratio: float = 0.0


class ChartSeries(BaseModel):
    name: str
    data: list[SeriesDataPoint]


class RankingItem(BaseModel):
    key: str
    label: str
    tokens: int = 0
    cost: float = 0.0
    calls: int = 0
    ratio: float = 0.0


class ChartResponse(BaseModel):
    view: str
    metric: str
    granularity: str
    start_at: datetime
    end_at: datetime
    time_labels: list[str] = Field(default_factory=list)
    series: list[ChartSeries] = Field(default_factory=list)
    ranking: list[RankingItem] = Field(default_factory=list)
