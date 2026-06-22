"""Chinese resident ID card format and checksum validation."""

from __future__ import annotations

import re

_ID_RE = re.compile(r"^\d{17}[\dXx]$")
_WEIGHTS = (7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2)
_CHECK_CHARS = "10X98765432"


def is_valid_id_card(id_card: str) -> bool:
    normalized = id_card.strip().upper()
    if not _ID_RE.match(normalized):
        return False
    digits = normalized[:17]
    if not digits.isdigit():
        return False
    total = sum(int(d) * w for d, w in zip(digits, _WEIGHTS, strict=True))
    return _CHECK_CHARS[total % 11] == normalized[17]
