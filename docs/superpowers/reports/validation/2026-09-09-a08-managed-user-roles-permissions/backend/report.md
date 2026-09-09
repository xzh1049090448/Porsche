# A08 managed-user roles and permissions backend verification

Status: `BLOCKED_FIXTURE`

Code under test is backend `9fdc07b3bcf4cb06049e4af0f5adde28e36facab`, with the A08 range `c6b1fb6d58bd5bc1281d6c44f2e262b20bef407f..9fdc07b3bcf4cb06049e4af0f5adde28e36facab`. The frozen contract SHA-256 is `dd202cb5019b10a891e10f03f77629b5f54e993110f148f417e05d089df35698`.

A08 implements verified promote, demote and permission-replacement actions, stable operation results, strict DTO and HTTP boundaries, atomic role/policy/session/audit/outbox writes, and fresh Gateway Key owner authorization. This report does not promote A08 to `PASS_LIMITED_SCOPE` because the required isolated real fixture was unavailable.

## Verification results

- Focused gate: exit 0. Its verbose evidence contained 308 pass events, 79 declared skip events and zero fail events.
- Focused race gate: exit 0, with fixture-gated cases still skipped.
- Full gate inside the restricted sandbox: exit 1 because existing `httptest` cases could not bind `tcp6 [::1]:0` and returned `bind: operation not permitted`. This is recorded as `BLOCKED_SANDBOX_BIND`, not PASS.
- Full gate retried in a loopback-enabled execution environment: exit 0, with 1,985 test pass events, 452 test skip events, zero test fail events, 17 passing packages and zero failing packages.
- Full race gate had the same sandbox bind failure, then exited 0 in the loopback-enabled retry with the same 1,985 pass, 452 skip and zero fail events across 17 passing packages.
- `go build ./...`, `go vet ./...` and pre-archive `git diff --check`: exit 0.
- The initial `./init.sh` invocation ended at exit 1 on the same sandbox-only `[::1]:0` restriction in `internal/handler`, `internal/service` and `internal/whitelabel`; it is not reported as a successful gate.

The complete 452-entry skip inventory for both full and full-race gates is stored in `manifest.json`. The two gates produced the same skip set. The A08-specific and migration-0012 skips were:

- `TestAdminOperationRolePermissionResults0012RealMySQLDownPreservesCompatibleResults`
- `TestA08RolePermissionRealOperationChainCommitsAtomicFacts`
- `TestA08RolePermissionRealOutboxFailureRollsBackEverySQLFact`
- `TestA08RolePermissionRealConcurrentPromoteSerializesOneTransition`
- `TestA08RolePermissionRealDemoteAndRepromoteNeverRevivesHistory`
- `TestA08RuntimeCredentialsAndGatewayOwnerPolicyChangeImmediately`
- `TestA08RuntimeGatewayPolicyCorruptionFailsClosed`
- `TestA08RuntimeBearerRequestReloadsPermissionDeny`

## Fixture and migration boundary

`TEST_DATABASE_URL`, `TEST_REDIS_URL` and `ACTION_SECURITY_HMAC_KEY` were all unset or empty. No replacement values were invented and no `.env` or production credential was read. Consequently, MySQL 8 and Redis 7 image IDs, migration ledger `0001` through `0012`, real credential lifecycle, rollback, concurrency and cleanup are all `NOT_RUN / BLOCKED_FIXTURE`.

Migration 0012 static contract checks passed. Its up SHA-256 is `7ed008718e76bf8251a15f4d115ef9f959f5f0f2c7e1f8a9a9398201a7239bde`; its down SHA-256 is `1f222263c40006002ffcc3fb1bcb1cf9ab5661cb9b317af008621fbbf689b8de`. The real MySQL up/down/schema/ledger test remains one of the declared skips.

## Review and finding closure

- Task 1 at `6a590c7`: final `SPEC_REVIEW_PASS` and `QUALITY_REVIEW_PASS` after exact envelope, catalog, query, RootOnly and TTL repairs.
- Task 2 at `883d4e1`: `SPEC_REVIEW_PASS` and `QUALITY_REVIEW_PASS`.
- Task 3 at `81659ed`: `SPEC_REVIEW_PASS` and `QUALITY_REVIEW_PASS` after the inherit wire-omission clarification; frontend clarification `94953e0` is outside this backend candidate.
- Task 4 at `050ed73`: `SPEC_REVIEW_PASS` and `QUALITY_REVIEW_PASS`; real migration-0012 MySQL up/down remains `NOT_RUN`.
- Task 5A at `f68b4e5`: `SPEC_REVIEW_PASS` and `QUALITY_REVIEW_PASS`.
- Task 5B at `85a5606`: the actor ID/GUID and direct-Execute finding was fixed; final `SPEC_REVIEW_PASS` and `QUALITY_REVIEW_PASS`.
- Task 5C at `89e54f8`: HMAC binding was fixed by `0628866`; ignored Count errors were fixed by `89e54f8`; final `SPEC_REVIEW_PASS` and `QUALITY_REVIEW_PASS`.
- Task 6 at `ee83987`: production Issue, same-state 403 and 413 boundaries were fixed by `f50c770`; cross-scope validation was fixed by `ee83987`; final `SPEC_REVIEW_PASS` and `QUALITY_REVIEW_PASS`.
- Task 7 at `9fdc07b`: the stale `REPEATABLE READ` policy snapshot was fixed with explicit `READ COMMITTED`; final `SPEC_REVIEW_PASS` and `QUALITY_REVIEW_PASS`.

No independent `SECURITY_REVIEW` verdict was obtained. It remains `PENDING_NOT_RUN`; security checks performed during quality review are not presented as a separate security review.

## Tracker boundary

A08 moves from `BLOCKED_NOT_IMPLEMENTED` to `BLOCKED_FIXTURE`; it does not become a passing item. The 26-item matrix remains 14 `PASS_LIMITED_SCOPE` and 12 blocked: 9 `BLOCKED_NOT_IMPLEMENTED`, 1 `BLOCKED_FIXTURE`, 1 `BLOCKED_PRODUCT` and 1 `BLOCKED_ENV`. `web-012` remains `in_progress`.

Frontend implementation, external backend `project_manager` written confirmation, 26-item joint acceptance, production migration, deployment, production acceptance and real business accounts remain `NOT_RUN`. No production or fixture resources were modified.
