# Domain Docs

How the engineering skills should consume this repo's domain documentation when exploring the codebase.

## Layout: multi-context

This is a monorepo with independent subprojects. Read `CONTEXT-MAP.md` at the repo root to find the relevant `CONTEXT.md` for each area.

| Context | Path | Stack |
| ------- | ---- | ----- |
| AI Gateway (Python) | `ai-gateway/CONTEXT.md` | Python, FastAPI, RAG, Chroma |
| AI Gateway (Go) | `ai-gateway-go/CONTEXT.md` | Go, HTTP gateway |
| Spring App | `spring-app/CONTEXT.md` | Java 17, Spring Boot, Maven |

System-wide architectural decisions live in `docs/adr/` at the repo root. Context-specific ADRs may live under each subproject's `docs/adr/`.

## Before exploring, read these

- **`CONTEXT-MAP.md`** at the repo root — points to one `CONTEXT.md` per context.
- The relevant **`CONTEXT.md`** for the subproject you are working in.
- **`docs/adr/`** — read ADRs that touch the area you're about to work in. Also check `<context>/docs/adr/` for context-scoped decisions.

If any of these files don't exist, **proceed silently**. Don't flag their absence; don't suggest creating them upfront. The `/domain-modeling` skill creates them lazily when terms or decisions actually get resolved.

## File structure

```
/
├── CONTEXT-MAP.md
├── docs/adr/                          ← system-wide decisions
├── ai-gateway/
│   ├── CONTEXT.md                     ← Python AI gateway domain
│   └── docs/adr/
├── ai-gateway-go/
│   ├── CONTEXT.md                     ← Go gateway domain
│   └── docs/adr/
└── spring-app/
    ├── CONTEXT.md                     ← Spring Boot API domain
    └── docs/adr/
```

## Use the glossary's vocabulary

When your output names a domain concept (in an issue title, a refactor proposal, a hypothesis, a test name), use the term as defined in the relevant `CONTEXT.md`. Don't drift to synonyms the glossary explicitly avoids.

If the concept you need isn't in the glossary yet, that's a signal — either you're inventing language the project doesn't use (reconsider) or there's a real gap (note it for `/domain-modeling`).

## Flag ADR conflicts

If your output contradicts an existing ADR, surface it explicitly rather than silently overriding:

> _Contradicts ADR-0007 (event-sourced orders) — but worth reopening because…_
