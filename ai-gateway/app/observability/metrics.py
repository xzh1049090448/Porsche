"""Prometheus metrics (optional lightweight export)."""

from prometheus_client import Counter, Histogram

REQUESTS_TOTAL = Counter(
    "gateway_requests_total",
    "Total chat completion requests",
    ["provider", "model", "status"],
)

REQUEST_LATENCY = Histogram(
    "gateway_request_latency_seconds",
    "End-to-end latency for non-streaming requests",
    ["provider"],
    buckets=(0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60, 120),
)

TOKENS_TOTAL = Counter(
    "gateway_tokens_total",
    "Estimated or reported tokens",
    ["direction", "client"],
)
