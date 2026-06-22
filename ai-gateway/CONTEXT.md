# AI Gateway Domain Context

Python FastAPI backend for domestic LLM aggregation, RAG, billing, and admin.

## Module layout

Aligned with [project module conventions](../docs/conventions/module-structure.md):

| Module | Path | Responsibility |
| ------ | ---- | -------------- |
| api | `app/api/` | HTTP controllers (FastAPI routes, deps) |
| common | `app/common/` | Schemas (Req/Resp DTO), constants, shared errors/exceptions/security |
| service | `app/service/` | Business logic |
| repository | `app/repository/` | SQLAlchemy models, session, data access helpers |
| task | `app/task/` | Scheduled jobs (placeholder) |
| tool | `app/tool/` | Logging, startup checks, ID validation utilities |

## Infrastructure (outside layered modules)

| Path | Responsibility |
| ---- | -------------- |
| `app/providers/` | Upstream LLM provider adapters |
| `app/observability/` | Prometheus metrics |
| `app/config.py` | Settings |
| `app/state.py` | Application runtime state |
| `app/main.py` | FastAPI entrypoint |

## Key domain terms

| Term | Meaning |
| ---- | ------- |
| Logical model | Client-facing model alias in `config/models.yaml` |
| Platform client | Internal downstream key used by platform chat to call the gateway |
| Dataset | RAG knowledge base slice (product, CS, rules, reviews) |
| Plan | User subscription tier (free / professional / enterprise) |
