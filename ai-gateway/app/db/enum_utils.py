"""Helpers for SQLAlchemy Enum columns (MySQL may return plain strings on read)."""

from __future__ import annotations

import enum
from typing import TypeVar

from sqlalchemy import Enum as SAEnum

E = TypeVar("E", bound=enum.Enum)


def enum_value(member: enum.Enum | str) -> str:
    if isinstance(member, enum.Enum):
        return member.value
    return str(member)


def enum_is(member: enum.Enum | str, expected: E) -> bool:
    return enum_value(member) == expected.value


def mysql_enum(enum_cls: type[E]) -> SAEnum:
    """Enum column stored as VARCHAR — avoids MySQL native ENUM read quirks."""
    return SAEnum(
        enum_cls,
        values_callable=lambda obj: [e.value for e in obj],
        native_enum=False,
    )
