# A08 managed-user roles and permissions backend verification

Status: `PASS_LIMITED_SCOPE`

Backend code under test is `f2f976005c2331c0409c1b27da79e3a43d25bcb0`. The frozen A08 contract SHA-256 remains `dd202cb5019b10a891e10f03f77629b5f54e993110f148f417e05d089df35698`.

## Current result

A08 now supports verified promotion, permission replacement and demotion with exact HTTP contracts, stable operation results, atomic role/policy/session/audit/outbox writes, zero-write conflict preflight, and fresh Gateway Key owner authorization. It is `PASS_LIMITED_SCOPE` in the disposable local acceptance environment.

The final focused real-fixture command ran 11 tests across migration, service and handler packages with exit 0. It covered:

- MySQL migration ledger `0001` through `0012` and real 0012 down/up compatibility.
- Successful atomic operation facts and outbox writes.
- same-state/stale/no-op preflight with unchanged verification, operation, session, audit and outbox facts.
- SQL/outbox rollback and concurrent promotion serialization.
- demotion and later promotion without reviving historical overrides.
- explicit `READ COMMITTED` gateway authentication.
- immediate Access/Refresh invalidation and fresh Gateway Key policy evaluation.
- corrupt owner policy fail-closed behavior and HTTP Bearer permission reload.

The command and test inventory are recorded in `manifest.json`. Earlier no-fixture full and race gates also passed in a loopback-enabled environment; their historical skip inventory remains in the manifest and is not counted as fixture evidence.

## Fixture evidence

- MySQL `8.0.46`, image `mysql:8.0` at `sha256:7dcddc01f13bab2f15cde676d44d01f61fc9f99fe7785e86196dfc07d358ae2b`.
- Redis `7.4.11`, image `redis:7-alpine` at `sha256:ff02b58f971e7d7d156a1267e283fcbbeee91773b6aa36c49dac28ecfe28eadf`.
- Migration ledger was exactly `0001` through `0012`.
- Migration 0012 SHA-256: up `7ed008718e76bf8251a15f4d115ef9f959f5f0f2c7e1f8a9a9398201a7239bde`; down `825d8f85c99ff9bc0ff0db68b3b036f87f307aceace9ea5d17d43655f5f60fb8`.
- The paired browser flow completed promotion, one explicit permission deny, and demotion. The final target was role User, active, `auth_version=7`, permission version `3`, rule count `0`, and zero active overrides.
- Before cleanup the isolated database contained 36 users, 63 sessions, 16 operations, 16 verifications, 11 policy heads, 11 override rows, 73 auth audit rows, 5 management audit rows and 5 outbox rows; Redis DB 15 contained 29 keys.
- Temporary services were stopped. The exact containers `porsche-a08-mysql-260909` and `porsche-a08-redis-260909` were removed, both names were absent from `docker ps -a`, and ports 8000/4176 had no listener.

One initial browser target was intentionally corrupt test data created by `TestA08RuntimeGatewayPolicyCorruptionFailsClosed`: an active override without a policy head. Its `policy_version_conflict` response demonstrated fail-closed behavior. The successful end-to-end run used a separate fixture user with no permission history.

## Review closure

- Task reviews through Task 7 remain `SPEC_REVIEW_PASS / QUALITY_REVIEW_PASS` after their recorded fixes.
- The zero-write same-state/stale/no-op issue was fixed by `d9896e9`; independent re-review returned `SECURITY_REVIEW_PASS`.
- The real migration fixture placeholder was fixed by `a4f9448`.
- Migration 0012 down/up compatibility was fixed by `f2f9760`; spec and security re-review passed.

## Remaining boundary

Visible browser commit-unknown and disabled-target branches are covered by automated contract/component tests rather than a live fault-injected browser run. External backend `project_manager` written confirmation, production migration, deployment, production acceptance and real business accounts remain `NOT_RUN`.

The 26-item matrix is now 15 `PASS_LIMITED_SCOPE`, 9 `BLOCKED_NOT_IMPLEMENTED`, 1 `BLOCKED_PRODUCT` and 1 `BLOCKED_ENV`. `web-012` remains `in_progress` because 11 non-A08 items are still blocked.
