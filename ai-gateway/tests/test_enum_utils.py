"""Enum helper unit tests."""

from app.db.enum_utils import StrEnumType, enum_is, enum_value
from app.db.models import PlanType, UserStatus


def test_enum_value_from_enum_and_str():
    assert enum_value(PlanType.FREE) == "free"
    assert enum_value("free") == "free"


def test_enum_is_accepts_str_from_mysql():
    assert enum_is("active", UserStatus.ACTIVE)
    assert not enum_is("disabled", UserStatus.ACTIVE)


def test_str_enum_type_coerces_mysql_string():
    col = StrEnumType(PlanType)
    assert col.process_result_value("free", None) == PlanType.FREE
    assert col.process_result_value("FREE", None) == PlanType.FREE
