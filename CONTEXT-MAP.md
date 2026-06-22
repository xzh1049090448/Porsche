# Context Map

This monorepo contains multiple independent subprojects. Each has its own domain context.

| Context | Directory | Description |
| ------- | --------- | ----------- |
| ai-gateway | `ai-gateway/` | Python FastAPI AI gateway — domestic LLM aggregation, RAG, billing, admin |
| ai-gateway-go | `ai-gateway-go/` | Go AI gateway — high-concurrency proxy and routing |
| spring-app | `spring-app/` | Java Spring Boot backend — blog API and sample services |

Each subproject has (or should have) a `CONTEXT.md` describing its module layout.

Read the `CONTEXT.md` in the relevant directory before working in that subproject.

## Conventions

- [模块分层规范](docs/conventions/module-structure.md) — api / common / service / repository / task / tool
