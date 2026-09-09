# Platform generation control BE04 report

Date: 2026-09-09

## Scope and candidate

BE04 final code candidate is `620dcd852222284e0d9c057d1e50ae6d3bb5e0cb`. Earlier evidence commits `40a195dce72827edb85105fc449e2e394293641e`, `1f78b81b90d5ce31266b895d925980eb8444906d`, and `412d790c1c0c929a47b36c5774f9295b246a032c` described candidate `23820acbccee28a291cf4bfacb6adf30e3cdfbee`. Final review then added intermediate corrupt-record handling commit `b114ef5290f6c6566d32ffc1bcd1921ca4016ba6`, final fix 1 `9b7e925cee306c36039d141eb3b19149857b71a2`, fix 2 `fee9ae4044639917300168b99e85870111e2b7bd`, and fix 3/current candidate `620dcd852222284e0d9c057d1e50ae6d3bb5e0cb`. This report binds executable evidence to that final code candidate. Its final documentation revision is identified by `git log`; post-documentation committed-tree verification is recorded by the Task 9 runner rather than asserted self-referentially here.

This tranche covers authenticated generation status and cancellation, cancellation-before-claim tombstones, owner-bound runner leases, restart-safe bounded convergence, strict completed-result hydration, and application lifecycle ownership. It does not implement BE05 or BE06 streaming orchestration.

The first Task 9 attempt was `BLOCKED_TEST_ISOLATION`: shared Redis fixture activity made two handler tests compare global `DBSIZE` and made the store scan test observe another valid generation key. Commit `23820ac` replaced global cleanup assumptions with owned-key evidence and a dedicated scan prefix. The refreshed evidence below comes from a new fixture and an unmodified, parallel `go test ./... -count=1`; the historical blocker is not hidden or counted as a pass.

## Implemented contracts

- `GET /api/v1/platform/chat/generations/:generation_id` and `POST /api/v1/platform/chat/generations/:generation_id/cancel` are registered under authenticated platform middleware and return `Cache-Control: no-store`.
- GET is user-scoped, projects metadata only before completion, and returns completed content only after exact MySQL receipt-graph validation and current-user token-total loading.
- Cancel is idempotent, creates an absent-ID tombstone, transitions running to cancelling, invokes an optional process-local hook, and uses one three-second dependency-bounded wait. Only unresolved cancelling or committing may return `202` with `Retry-After: 1`.
- Newly claimed running records carry a 30-second hashed owner lease; renewal is capability-bound. Lease material never enters an HTTP DTO.
- One serial worker performs an immediate pass and five-second bounded passes, scanning at most 512 keys or 100 ms and reconciling expired running, stale cancelling, and committing records through CAS and the BE03 receipt authority.
- Application state owns worker and Redis-client shutdown. Legacy chat orchestration and the v2 SSE encoder are unchanged; BE05/BE06 POST behavior remains guarded.
- The final review fixes make corrupt per-record convergence data non-fatal while preserving caller cancellation/deadline, require exact receipt matching immediately before the completing CAS, and propagate the cancel deadline through advisory-lock release while discarding a pinned session if release fails.

## Real Redis evidence

The refreshed Redis fixture used image `redis:7-alpine`, actual Redis version 7.4.11, image ID `sha256:ff02b58f971e7d7d156a1267e283fcbbeee91773b6aa36c49dac28ecfe28eadf`, container ID `73eb163489302d30ff235b790c10fa7210f54fd2871e5c045422316b7eebd155`, task label `codex.task=be04-final-review-20260909-e62f1c49`, loopback binding `127.0.0.1:57434`, Redis DB 15, tmpfs storage, and persistence disabled.

The final-fix focused normal and race gates passed with every selected leaf present and zero skips or failures. They covered corrupt-record continuation, caller cancellation/deadline after benign error and successful scan, exact receipt mismatch before CAS, cancel deadline propagation, and failed advisory-lock release session discard. Task 8 combined normal/race also passed with zero skips or failures:

```text
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./internal/service -run 'Test(PlatformGenerationConvergerRunPass(SkipsCorruptRecordAndConvergesNextIdentity|ReturnsCallerCancellationAfterBenignRecordError|ReturnsCallerDeadlineAfterBenignRecordError|ReturnsCallerCancellationAfterSuccessfulScan|ReturnsCallerDeadlineAfterSuccessfulScan)|ReconcilePlatformGeneration(ReceiptMatcherGuardsCommittingCAS|MismatchDoesNotCompleteRedisIntegration)|PlatformGenerationControlCancelDeadlineIncludesAdvisoryLockCleanup|WithPlatformGenerationAdvisoryLockReleaseFailureDiscardsPinnedSession)$' -count=1 -json
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test -race ./internal/service -run 'Test(PlatformGenerationConvergerRunPass(SkipsCorruptRecordAndConvergesNextIdentity|ReturnsCallerCancellationAfterBenignRecordError|ReturnsCallerDeadlineAfterBenignRecordError|ReturnsCallerCancellationAfterSuccessfulScan|ReturnsCallerDeadlineAfterSuccessfulScan)|ReconcilePlatformGeneration(ReceiptMatcherGuardsCommittingCAS|MismatchDoesNotCompleteRedisIntegration)|PlatformGenerationControlCancelDeadlineIncludesAdvisoryLockCleanup|WithPlatformGenerationAdvisoryLockReleaseFailureDiscardsPinnedSession)$' -count=1
ok github.com/porsche/ai-gateway-go/internal/service 3.299s
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./internal/service ./internal/handler -run 'TestPlatformGeneration.*(Integration|Restart|MultiInstance|Receipt|Owner)' -count=1 -json
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test -race ./internal/service ./internal/handler -run 'TestPlatformGeneration.*(Integration|Restart|MultiInstance|Owner)' -count=1 -json
```

No production Redis configuration was read.

## MySQL plus Redis restart evidence

The refreshed MySQL fixture used image `mysql:8.4`, actual MySQL version 8.4.11, image ID `sha256:b3b90af2a6552ae30c266fdb7d5dd55f3afb72404bb78d37fe8a23eb857fd3fb`, container ID `f36d185511fa75cf1afcdf8964eab1c8acb60678540261447055238ea2e4ceb7`, the same task label, loopback binding `127.0.0.1:57433`, parent database `porsche_be04_final_review_test`, and tmpfs storage. Migrations `0001` through `0011` were freshly applied before fixture gates. The applied up-file SHA-256 ledger was `0001 2da41ffd07c44d45`, `0002 58712428ca668fb1`, `0003 31c49d9bb1f171d9`, `0004 44b5caba0473162c`, `0005 4fc34da357e155c4`, `0006 c0bc9f6837098531`, `0007 b3c3351771fce2db`, `0008 21289da334e7ef44`, `0009 4dc818d93180bb67`, `0010 b6ddd5b7088f1617`, and `0011 be6ea3beb18a64cf`. The one-use test-only action-security HMAC was held only in the test shell and its value was not recorded.

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

Notable package times were handler 21.606s, migration 50.417s, service 88.195s, and router 7.040s; every package passed. The JSON evidence identified one opt-in test skip, `TestAdminUsersReadPerformance`, whose exact reason is `NOT_RUN: opt-in 100k synthetic-user performance fixture requires this batch authorization`. No BE04 fixture test skipped.

```text
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./... -run '^TestAdminUsersReadPerformance$' -count=1 -json
```

After a fresh database/Redis reset and migrations, the affected-package race command passed: service 183.393s, handler 21.594s, app 1.492s, and router 4.020s.

```text
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test -race ./internal/service ./internal/handler ./internal/app ./internal/router -count=1
```

An earlier candidate's first affected-race attempt followed full without a fixture reset and hit the pre-existing A03 action-security rate limit in `TestCreateAccountRealWriteFaultsRollbackEveryStage/terminal_operation`. It remains classified `FAIL_ENV_FIXTURE_NOT_FRESH`, not a BE04 or product pass. For `620dcd`, each major gate used a fresh reset and the race result above is final.

`git diff --check origin/main...HEAD`, `go vet ./...`, and `go build ./...` all exited zero for code candidate `620dcd852222284e0d9c057d1e50ae6d3bb5e0cb` before this documentation revision. Earlier committed-tree verification at `40a195dce72827edb85105fc449e2e394293641e` covered only that exact old evidence HEAD and does not verify later code or documentation. The final documentation commit receives a separate fresh-fixture full, vet, diff, JSON, status, and cleanup verification outside this self-referential report.

## Security and privacy checks

The lease scan found the internal capability field only in handler integration-test setup that asserts a claim produced a capability. No production handler or model contains lease capability or digest fields, and this report records neither raw nor hashed lease material.

The production control, converger, and generation-handler scan found no `prompt`, `reply`, `Authorization`, `DATABASE_URL`, or `REDIS_URL` occurrence. Manual review confirms non-completed projections contain no content and dependency failures use stable body-free sentinels.

The scoped diff against `origin/main` shows no change to `internal/service/platform_chat.go` or `internal/service/platform_sse_v2.go`. The only `internal/handler/platform.go` change constructs the shared platform route base and registers the two planned authenticated generation routes with no-store middleware; existing platform routes retain authentication and diagnostics.

## Fixture cleanup

Candidate cleanup checked the unique task label, owned database list, Redis DB, and loopback bindings, then stopped the exact MySQL container `f36d185511fa75cf1afcdf8964eab1c8acb60678540261447055238ea2e4ceb7` and Redis container `73eb163489302d30ff235b790c10fa7210f54fd2871e5c045422316b7eebd155`. Before stopping, the owned-database query returned only parent `porsche_be04_final_review_test`, proving zero owned child databases remained; Redis DB 15 contained 565 owned fixture keys. Both `--rm` containers were removed after stop, destroying the tmpfs parent database and Redis data. The final `codex.task=be04-final-review-20260909-e62f1c49` label query was empty, and `127.0.0.1:57433` and `127.0.0.1:57434` had no listener. No Docker prune, volume removal, unrelated-container action, credential file, or production resource operation was used.

## Explicitly not run and remaining BE05-BE06 work

BE05 single-model v2 streaming, its ten-second production lease-renewal loop, BE06 compare fan-out/streaming, frontend/backend joint acceptance, production migration, deployment, push, merge, and real or paid upstream calls were not run. Production data and `.env` were not read or changed.

`go-018` therefore remains `in_progress`: BE01 through BE04 are complete as bounded local backend/frontend tranches, while BE05, BE06, joint acceptance, production migration, deployment, and real-upstream acceptance remain outstanding.
