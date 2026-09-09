# Platform generation control BE04 report

Date: 2026-09-09

## Scope and candidate

BE04 code candidate is `23820acbccee28a291cf4bfacb6adf30e3cdfbee`. The evidence commit containing this report is its immediate child and changes only `progress.md`, `feature_list.json`, and this report; the final log and diff checks establish that relationship without changing candidate code.

This tranche covers authenticated generation status and cancellation, cancellation-before-claim tombstones, owner-bound runner leases, restart-safe bounded convergence, strict completed-result hydration, and application lifecycle ownership. It does not implement BE05 or BE06 streaming orchestration.

The first Task 9 attempt was `BLOCKED_TEST_ISOLATION`: shared Redis fixture activity made two handler tests compare global `DBSIZE` and made the store scan test observe another valid generation key. Commit `23820ac` replaced global cleanup assumptions with owned-key evidence and a dedicated scan prefix. The final evidence below comes from a new fixture and an unmodified, parallel `go test ./... -count=1`; the historical blocker is not hidden or counted as a pass.

## Implemented contracts

- `GET /api/v1/platform/chat/generations/:generation_id` and `POST /api/v1/platform/chat/generations/:generation_id/cancel` are registered under authenticated platform middleware and return `Cache-Control: no-store`.
- GET is user-scoped, projects metadata only before completion, and returns completed content only after exact MySQL receipt-graph validation and current-user token-total loading.
- Cancel is idempotent, creates an absent-ID tombstone, transitions running to cancelling, invokes an optional process-local hook, and uses one three-second dependency-bounded wait. Only unresolved cancelling or committing may return `202` with `Retry-After: 1`.
- Newly claimed running records carry a 30-second hashed owner lease; renewal is capability-bound. Lease material never enters an HTTP DTO.
- One serial worker performs an immediate pass and five-second bounded passes, scanning at most 512 keys or 100 ms and reconciling expired running, stale cancelling, and committing records through CAS and the BE03 receipt authority.
- Application state owns worker and Redis-client shutdown. Legacy chat orchestration and the v2 SSE encoder are unchanged; BE05/BE06 POST behavior remains guarded.

## Real Redis evidence

The final Redis fixture used image `redis:7-alpine`, actual Redis version 7.4.11, image ID `sha256:ff02b58f971e7d7d156a1267e283fcbbeee91773b6aa36c49dac28ecfe28eadf`, container ID `06d6f000d4620e4c2a2a8ee7309898bafd4fe0def9c36ac61a50371111386583`, task label `codex.task=be04-task9-final-20260909-a71d5e90`, loopback binding `127.0.0.1:64819`, Redis DB 15, tmpfs storage, and persistence disabled.

The following gate passed with zero skips and failures:

```text
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test -race ./internal/service -run 'TestPlatformGeneration(Cancel|Lease|Expired|Scan|Converger)' -count=10
ok github.com/porsche/ai-gateway-go/internal/service 18.613s
```

Coverage includes cancellation/tombstone races, lease ownership/expiry, bounded cursor scanning, and converger behavior. No production Redis configuration was read.

## MySQL plus Redis restart evidence

The final MySQL fixture used image `mysql:8.4`, actual MySQL version 8.4.11, image ID `sha256:b3b90af2a6552ae30c266fdb7d5dd55f3afb72404bb78d37fe8a23eb857fd3fb`, container ID `d2ac16c7a683a83111da661fe686c270a8c4bee750510120ef36f0b93c216cbb`, the same task label, loopback binding `127.0.0.1:64818`, parent database `porsche_be04_task9_final_test`, and tmpfs storage. Migrations `0001` through `0011` were freshly applied before fixture gates. The one-use test-only action-security HMAC was held only in the test shell and its value was not recorded.

These commands passed with selected tests present, zero fixture skips, and zero failures:

```text
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./internal/service ./internal/handler -run 'TestPlatformGeneration.*(Integration|Restart|MultiInstance|Receipt|Owner)' -count=1 -json
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test -race ./internal/service ./internal/handler -run 'TestPlatformGeneration.*(Integration|Restart|MultiInstance|Owner)' -count=1 -json
```

Observed BE04 coverage includes completed single and partial-success compare HTTP views, current account totals, receipt mismatch rejection, owner isolation, receipt-present and receipt-absent restart behavior, two-instance convergence, active lease survival, expired-running failure, and stale-cancelling completion.

## Full regression and static gates

The original parallel command, with no `GOFLAGS`, passed on the fresh final fixture:

```text
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./... -count=1
```

Notable package times were handler 30.133s, migration 70.870s, service 115.460s, and router 8.274s; every package passed. A second JSON full run also passed. Its only opt-in test skip was `TestAdminUsersReadPerformance`, whose exact reason is `NOT_RUN: opt-in 100k synthetic-user performance fixture requires this batch authorization`. No BE04 fixture test skipped.

After a fresh database/Redis reset and migrations, the affected-package race command passed: service 211.604s, handler 29.901s, app 1.735s, and router 5.144s.

```text
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test -race ./internal/service ./internal/handler ./internal/app ./internal/router -count=1
```

The first affected-race attempt followed full without a fixture reset and hit the pre-existing A03 action-security rate limit in `TestCreateAccountRealWriteFaultsRollbackEveryStage/terminal_operation`. It is classified `FAIL_ENV_FIXTURE_NOT_FRESH`, not a BE04 or product pass. The fresh-reset rerun above is the final result.

`gofmt`, `git diff --check`, `go vet ./...`, and `go build ./...` all exited zero before documentation. Final committed-tree full, vet, and diff checks are recorded by the immediate evidence commit workflow.

## Security and privacy checks

The lease scan found the internal capability field only in handler integration-test setup that asserts a claim produced a capability. No production handler or model contains lease capability or digest fields, and this report records neither raw nor hashed lease material.

The production control, converger, and generation-handler scan found no `prompt`, `reply`, `Authorization`, `DATABASE_URL`, or `REDIS_URL` occurrence. Manual review confirms non-completed projections contain no content and dependency failures use stable body-free sentinels.

The scoped diff against `origin/main` shows no change to `internal/service/platform_chat.go` or `internal/service/platform_sse_v2.go`. The only `internal/handler/platform.go` change constructs the shared platform route base and registers the two planned authenticated generation routes with no-store middleware; existing platform routes retain authentication and diagnostics.

## Fixture cleanup

Final cleanup checked the unique task label, owned database list, Redis DB, and loopback bindings, then stopped the exact MySQL container `d2ac16c7a683a83111da661fe686c270a8c4bee750510120ef36f0b93c216cbb` and Redis container `06d6f000d4620e4c2a2a8ee7309898bafd4fe0def9c36ac61a50371111386583`. Before stopping, the owned-database query returned only parent `porsche_be04_task9_final_test`, proving zero owned child databases remained. Both `--rm` containers were removed after stop; their tmpfs parent database and Redis data were destroyed with them. The final `codex.task=be04-task9-final-20260909-a71d5e90` label query was empty, and `127.0.0.1:64818` and `127.0.0.1:64819` had no listener. No Docker prune, volume removal, unrelated-container action, credential file, or production resource operation was used.

## Explicitly not run and remaining BE05-BE06 work

BE05 single-model v2 streaming, its ten-second production lease-renewal loop, BE06 compare fan-out/streaming, frontend/backend joint acceptance, production migration, deployment, push, merge, and real or paid upstream calls were not run. Production data and `.env` were not read or changed.

`go-018` therefore remains `in_progress`: BE01 through BE04 are complete as bounded local backend/frontend tranches, while BE05, BE06, joint acceptance, production migration, deployment, and real-upstream acceptance remain outstanding.
