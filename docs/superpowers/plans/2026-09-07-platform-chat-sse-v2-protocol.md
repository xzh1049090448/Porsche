# Platform Chat SSE v2 Protocol Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add strict, independently tested backend request and event primitives for `platform-chat-sse.v2` while leaving legacy streaming behavior and public routing unchanged until the generation lifecycle exists.

**Architecture:** Handler-level helpers recognize and validate v2-only request fields, then strip platform fields before upstream projection. A service-level encoder owns sanitized SSE v2 event schemas and ordering state; it writes one framed event per flushable byte slice but does not claim generations, access Redis/MySQL, or activate v2 routes in this tranche.

**Tech Stack:** Go 1.22+, `testing`, `httptest`, existing handler/service/dto conventions.

---

### Task 1: Strict v2 request projection

**Files:**

- Modify: `internal/handler/platform.go`
- Modify: `internal/handler/platform_decode_test.go`
- Create: `internal/handler/platform_v2_contract_test.go`

- [x] Add RED tests for exact `stream_version: "platform-chat-sse.v2"`, `stream: true`, UUID generation ID, unknown platform fields, and legacy requests.
- [x] Implement a private/request DTO projection that validates v2 fields and strips `stream_version` and `generation_id` from the upstream `WhiteLabelBody`.
- [x] Prove non-v2 requests retain current decoding and routing behavior. Do not register cancel/status routes or activate v2 streaming without the later generation store.

### Task 2: Sanitized v2 SSE encoder

**Files:**

- Create: `internal/service/platform_sse_v2.go`
- Create: `internal/service/platform_sse_v2_test.go`

- [x] Add RED tests for `meta → delta* → model_done|model_error → done|error`, strict per-model seq, single/compare interleaving, and exact JSON keys.
- [x] Implement an encoder/state object producing `event: <name>\ndata: <json>\n\n`; every call returns a complete byte slice suitable for immediate write+Flush.
- [x] Enforce first/unique meta, non-empty deltas, opaque generation/model matching, seq from 1, one model terminal each, all-model terminal before global done, and no event after terminal.
- [x] Use stable error-code allowlists and explicit DTO structs/maps. Never serialize prompt, reply content, Authorization, upstream error bodies, internal URLs, arbitrary extras, or bare `[DONE]`.
- [x] Emit exact response-header values through a tested helper: `text/event-stream; charset=utf-8`, `no-cache, no-transform`, and `X-Accel-Buffering: no`.

### Task 3: Failure and compatibility matrix

- [x] Add RED tests for duplicate/conflicting meta, seq gap/out-of-order/duplicate delta, wrong generation/model, empty delta, model event after terminal, premature/duplicate global terminal, callback/write error, and sensitive strings.
- [x] Fail closed with stable Go errors; do not emit a successful done after any encoder failure.
- [x] Confirm existing legacy tests still expect and receive the old protocol, including `[DONE]`, because v2 is not wired in this tranche.

### Task 4: Verification and commit

Run:

```bash
go test ./internal/service ./internal/handler -run 'Test.*Platform.*V2|Test.*SSE.*V2' -count=1
go test ./internal/service ./internal/handler -count=1
go test ./... -count=1
go vet ./...
git diff --check
```

Expected: all commands exit zero. Commit only the listed handler/service tests and implementation plus this plan with `feat(platform): add strict SSE v2 protocol primitives`. Do not change database models, migrations, Redis, production Nginx, deployment, push, merge, or run a real upstream call.

## Deferred

Generation claim/CAS, Redis TTL, cancel/status routes, transactional persistence, context cancellation, actual v2 route activation, proxy configuration and real HTTPS acceptance require later plans and the outstanding product decisions.

## Approved Follow-up Decisions

On 2026-09-07 the user approved the following contract choices for later lifecycle tasks:

- A duplicate `generation_id` POST returns HTTP 409 with the current authoritative state and never starts or reattaches an upstream stream.
- Compare mode persists one independent `assistant_message_guid` per model.
- Cancelled and failed generations do not consume daily call quota; incurred upstream cost is recorded through a separate audit boundary.
- Redis unavailability fails only v2 requests with a stable 503 while legacy requests retain their current behavior.

These decisions do not activate v2 routing in BE-01 and do not authorize deployment, migration, push, merge, or real paid model calls.
