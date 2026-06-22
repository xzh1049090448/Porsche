"""SQLAlchemy ORM models for the aggregation platform."""

from __future__ import annotations

import enum
from datetime import datetime, timezone

from sqlalchemy import (
    JSON,
    Boolean,
    DateTime,
    Float,
    ForeignKey,
    Integer,
    String,
    Text,
    UniqueConstraint,
)
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column, relationship

from app.repository.enum_utils import str_enum


def utcnow() -> datetime:
    return datetime.now(timezone.utc)


class Base(DeclarativeBase):
    pass


class UserStatus(str, enum.Enum):
    ACTIVE = "active"
    DISABLED = "disabled"


class PlanType(str, enum.Enum):
    FREE = "free"
    PROFESSIONAL = "professional"
    ENTERPRISE = "enterprise"


class DatasetCategory(str, enum.Enum):
    PRODUCT_KNOWLEDGE = "product_knowledge"
    CUSTOMER_SERVICE = "customer_service"
    PLATFORM_RULES = "platform_rules"
    REVIEW_SENTIMENT = "review_sentiment"


class DatasetStatus(str, enum.Enum):
    DRAFT = "draft"
    PROCESSING = "processing"
    ACTIVE = "active"
    OFFLINE = "offline"


class VectorStatus(str, enum.Enum):
    PENDING = "pending"
    PROCESSING = "processing"
    READY = "ready"
    FAILED = "failed"


class OrderStatus(str, enum.Enum):
    PENDING = "pending"
    PAID = "paid"
    CANCELLED = "cancelled"
    REFUNDED = "refunded"


class User(Base):
    __tablename__ = "users"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    phone: Mapped[str] = mapped_column(String(20), unique=True, index=True)
    password_hash: Mapped[str | None] = mapped_column(String(255), nullable=True)
    nickname: Mapped[str | None] = mapped_column(String(64), nullable=True)
    real_name: Mapped[str | None] = mapped_column(String(64), nullable=True)
    id_card_hash: Mapped[str | None] = mapped_column(String(128), nullable=True)
    is_verified: Mapped[bool] = mapped_column(Boolean, default=False)
    plan_type: Mapped[PlanType] = mapped_column(str_enum(PlanType), default=PlanType.FREE)
    status: Mapped[UserStatus] = mapped_column(str_enum(UserStatus), default=UserStatus.ACTIVE)
    allowed_models: Mapped[list | None] = mapped_column(JSON, nullable=True)
    allowed_datasets: Mapped[list | None] = mapped_column(JSON, nullable=True)
    daily_call_limit: Mapped[int] = mapped_column(Integer, default=100)
    daily_calls_used: Mapped[int] = mapped_column(Integer, default=0)
    daily_calls_reset_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    total_tokens_used: Mapped[int] = mapped_column(Integer, default=0)
    dataset_calls: Mapped[int] = mapped_column(Integer, default=0)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    updated_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), default=utcnow, onupdate=utcnow
    )

    conversations: Mapped[list[Conversation]] = relationship(back_populates="user")
    orders: Mapped[list[Order]] = relationship(back_populates="user")


class Conversation(Base):
    __tablename__ = "conversations"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    user_id: Mapped[int] = mapped_column(ForeignKey("users.id"), index=True)
    title: Mapped[str] = mapped_column(String(256), default="新对话")
    model: Mapped[str | None] = mapped_column(String(128), nullable=True)
    dataset_enabled: Mapped[bool] = mapped_column(Boolean, default=False)
    dataset_ids: Mapped[list | None] = mapped_column(JSON, nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    updated_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), default=utcnow, onupdate=utcnow
    )

    user: Mapped[User] = relationship(back_populates="conversations")
    messages: Mapped[list[Message]] = relationship(back_populates="conversation", cascade="all, delete-orphan")


class Message(Base):
    __tablename__ = "messages"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    conversation_id: Mapped[int] = mapped_column(ForeignKey("conversations.id"), index=True)
    role: Mapped[str] = mapped_column(String(32))
    content: Mapped[str] = mapped_column(Text)
    model: Mapped[str | None] = mapped_column(String(128), nullable=True)
    dataset_used: Mapped[bool] = mapped_column(Boolean, default=False)
    dataset_attribution: Mapped[str | None] = mapped_column(String(512), nullable=True)
    tokens: Mapped[int] = mapped_column(Integer, default=0)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)

    conversation: Mapped[Conversation] = relationship(back_populates="messages")


class Dataset(Base):
    __tablename__ = "datasets"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    name: Mapped[str] = mapped_column(String(128))
    slug: Mapped[str] = mapped_column(String(64), unique=True, index=True)
    category: Mapped[DatasetCategory] = mapped_column(str_enum(DatasetCategory))
    description: Mapped[str | None] = mapped_column(Text, nullable=True)
    status: Mapped[DatasetStatus] = mapped_column(str_enum(DatasetStatus), default=DatasetStatus.DRAFT)
    current_version: Mapped[str] = mapped_column(String(32), default="1.0.0")
    token_count: Mapped[int] = mapped_column(Integer, default=0)
    vector_status: Mapped[VectorStatus] = mapped_column(
        str_enum(VectorStatus), default=VectorStatus.PENDING
    )
    access_plans: Mapped[list | None] = mapped_column(JSON, nullable=True)
    compliance_report: Mapped[dict | None] = mapped_column(JSON, nullable=True)
    asset_id: Mapped[str | None] = mapped_column(String(128), nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    updated_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), default=utcnow, onupdate=utcnow
    )

    versions: Mapped[list[DatasetVersion]] = relationship(back_populates="dataset", cascade="all, delete-orphan")


class DatasetVersion(Base):
    __tablename__ = "dataset_versions"
    __table_args__ = (UniqueConstraint("dataset_id", "version", name="uq_dataset_version"),)

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    dataset_id: Mapped[int] = mapped_column(ForeignKey("datasets.id"), index=True)
    version: Mapped[str] = mapped_column(String(32))
    file_path: Mapped[str | None] = mapped_column(String(512), nullable=True)
    token_count: Mapped[int] = mapped_column(Integer, default=0)
    record_count: Mapped[int] = mapped_column(Integer, default=0)
    compliance_report: Mapped[dict | None] = mapped_column(JSON, nullable=True)
    is_active: Mapped[bool] = mapped_column(Boolean, default=False)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)

    dataset: Mapped[Dataset] = relationship(back_populates="versions")


class UsageRecord(Base):
    __tablename__ = "usage_records"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    user_id: Mapped[int] = mapped_column(ForeignKey("users.id"), index=True)
    record_type: Mapped[str] = mapped_column(String(32))
    tokens: Mapped[int] = mapped_column(Integer, default=0)
    model: Mapped[str | None] = mapped_column(String(128), nullable=True)
    dataset_id: Mapped[int | None] = mapped_column(ForeignKey("datasets.id"), nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class Order(Base):
    __tablename__ = "orders"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    order_no: Mapped[str] = mapped_column(String(64), unique=True, index=True)
    user_id: Mapped[int] = mapped_column(ForeignKey("users.id"), index=True)
    plan_type: Mapped[PlanType] = mapped_column(str_enum(PlanType))
    amount: Mapped[float] = mapped_column(Float, default=0.0)
    status: Mapped[OrderStatus] = mapped_column(str_enum(OrderStatus), default=OrderStatus.PENDING)
    invoice_requested: Mapped[bool] = mapped_column(Boolean, default=False)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    paid_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)

    user: Mapped[User] = relationship(back_populates="orders")


class AuditLog(Base):
    __tablename__ = "audit_logs"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    user_id: Mapped[int | None] = mapped_column(ForeignKey("users.id"), nullable=True, index=True)
    action: Mapped[str] = mapped_column(String(64), index=True)
    resource: Mapped[str | None] = mapped_column(String(128), nullable=True)
    detail: Mapped[dict | None] = mapped_column(JSON, nullable=True)
    ip: Mapped[str | None] = mapped_column(String(64), nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow, index=True)


class ModelHealth(Base):
    __tablename__ = "model_health"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    model_name: Mapped[str] = mapped_column(String(128), unique=True, index=True)
    provider: Mapped[str] = mapped_column(String(64))
    is_available: Mapped[bool] = mapped_column(Boolean, default=True)
    avg_latency_ms: Mapped[float] = mapped_column(Float, default=0.0)
    error_rate: Mapped[float] = mapped_column(Float, default=0.0)
    last_checked_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
