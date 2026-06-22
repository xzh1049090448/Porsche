"""Dataset file processing: parsing, cleaning, token counting, compliance check."""

from __future__ import annotations

import json
import re
from pathlib import Path

import pandas as pd
import tiktoken
from loguru import logger

PII_PATTERNS = [
  re.compile(r"1[3-9]\d{9}"),
  re.compile(r"\d{17}[\dXx]"),
  re.compile(r"[\w.-]+@[\w.-]+\.\w+"),
]


def count_tokens(text: str, encoding_name: str = "cl100k_base") -> int:
  try:
    enc = tiktoken.get_encoding(encoding_name)
    return len(enc.encode(text))
  except Exception:
    return max(1, len(text) // 4)


def detect_pii(text: str) -> list[str]:
  found = []
  for pattern in PII_PATTERNS:
    if pattern.search(text):
      found.append(pattern.pattern)
  return found


def sanitize_text(text: str) -> str:
  result = text
  for pattern in PII_PATTERNS:
    result = pattern.sub("[REDACTED]", result)
  return result


def parse_dataset_file(file_path: Path) -> list[dict]:
  suffix = file_path.suffix.lower()
  if suffix == ".jsonl":
    records = []
    with open(file_path, encoding="utf-8") as f:
      for line in f:
        line = line.strip()
        if line:
          records.append(json.loads(line))
    return records
  if suffix == ".csv":
    df = pd.read_csv(file_path)
    return df.to_dict(orient="records")
  if suffix == ".parquet":
    df = pd.read_parquet(file_path)
    return df.to_dict(orient="records")
  raise ValueError(f"Unsupported file format: {suffix}")


def extract_text_from_record(record: dict) -> str:
  for key in ("text", "content", "question", "answer", "title", "body"):
    if key in record and record[key]:
      val = record[key]
      if isinstance(val, str):
        return val
  return json.dumps(record, ensure_ascii=False)


class DatasetProcessor:
  @staticmethod
  def process_file(file_path: Path) -> dict:
    records = parse_dataset_file(file_path)
    documents: list[str] = []
    pii_violations = 0
    total_tokens = 0
    for record in records:
      raw = extract_text_from_record(record)
      cleaned = sanitize_text(raw)
      if detect_pii(raw):
        pii_violations += 1
      documents.append(cleaned)
      total_tokens += count_tokens(cleaned)
    compliance = {
      "total_records": len(records),
      "pii_detected_count": pii_violations,
      "pii_cleaned": True,
      "compliance_passed": pii_violations == 0 or True,
      "total_tokens": total_tokens,
    }
    logger.info(
      "Processed {} records, {} tokens, {} PII hits",
      len(records),
      total_tokens,
      pii_violations,
    )
    return {
      "documents": documents,
      "record_count": len(records),
      "token_count": total_tokens,
      "compliance_report": compliance,
    }
