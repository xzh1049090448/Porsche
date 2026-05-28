"""Helpers for SQLAlchemy Enum columns (MySQL may return plain strings on read)."""

from __future__ import annotations

import enum
from typing import Any, TypeVar

from sqlalchemy import String
from sqlalchemy.types import TypeDecorator

E = TypeVar("E", bound=enum.Enum)


def enum_value(member: enum.Enum | str) -> str:
    if isinstance(member, enum.Enum):
        return member.value
    return str(member)


def enum_is(member: enum.Enum | str, expected: E) -> bool:
    return enum_value(member) == expected.value


def _coerce_enum(enum_cls: type[E], value: Any) -> E:
    if isinstance(value, enum_cls):
        return value
    if isinstance(value, enum.Enum):
        value = value.value
    raw = str(value)
    try:
        return enum_cls(raw)
    except ValueError:
        pass
    upper = raw.upper()
    if upper in enum_cls.__members__:
        return enum_cls[upper]
    for member in enum_cls:
        if member.name.upper() == upper:
            return member
    raise ValueError(f"{raw!r} is not a valid {enum_cls.__name__}")


class StrEnumType(TypeDecorator):
    """Store enum values as strings; safe for MySQL ENUM and VARCHAR columns."""

    impl = String(32)
    cache_ok = True

    def __init__(self, enum_cls: type[E], *, length: int = 32) -> None:
        self.enum_cls = enum_cls
        super().__init__(length=length)

    def process_bind_param(self, value: Any, dialect: Any) -> str | None:
        if value is None:
            return None
        return enum_value(value)

    def process_result_value(self, value: Any, dialect: Any) -> E | None:
        if value is None:
            return None
        return _coerce_enum(self.enum_cls, value)


def str_enum(enum_cls: type[E], *, length: int = 32) -> StrEnumType:
    return StrEnumType(enum_cls, length=length)
