# Platform generation persistence verification

Date: 2026-09-08

Scope: BE03 migration 0011, atomic generation persistence, receipt integrity, and receipt-aware reconciliation only. No v2 route activation, frontend change, production migration, deployment, push, merge, or real upstream request.

## Isolated fixtures

The final run used one loopback-only disposable MySQL 8.4 `*_test` database and one loopback-only disposable Redis 7 instance. Both containers used tmpfs storage, were selected by exact container ID and the task label `codex.task=be03-security-findings-fix`, and were cleaned up without touching unrelated Docker resources. Neither `.env` nor production database/Redis configuration was read.

- MySQL image: `mysql@sha256:b3b90af2a6552ae30c266fdb7d5dd55f3afb72404bb78d37fe8a23eb857fd3fb`
- MySQL container: `porsche-chat-be03-secfix-mysql-20260909`
- MySQL container ID: `2cb7af30e74641a2af6be6c02e3fa9c98d2760516922860443fb4efcdf182b34`
- MySQL binding and database: `127.0.0.1:57112`, `porsche_generation_secfix_full_test`
- Redis image: `redis@sha256:ff02b58f971e7d7d156a1267e283fcbbeee91773b6aa36c49dac28ecfe28eadf`
- Redis container: `porsche-chat-be03-secfix-redis-20260909`
- Redis container ID: `0b10e3a8da8a3b8baa7e08559b0b41edb3412e318a5871eb0d2d8bc8f807c40b`
- Redis binding: `127.0.0.1:57188`

Docker lifecycle evidence recorded successful health probes followed by `stop`, exit-zero `die`, and `destroy` events for both exact IDs. The final label query was empty and both loopback ports had no listener. No credential, DSN, test HMAC value, prompt, model response, or raw dependency error is retained in this report.

## RED evidence

The migration contract first failed because 0011 was absent. Typed persistence tests first failed because the receipt/result models, validation, advisory-lock helper, and finalizer did not exist. Receipt-reader, atomic finalization, quota/idempotency, and reconciliation tests were each observed failing before their minimal implementation.

Implementation and review RED cases additionally exposed and then fixed these boundaries:

- The first real MySQL advisory-lock run showed that reusing GORM statement metadata after `GET_LOCK` could corrupt subsequent scans; fresh GORM sessions now retain the same pinned physical connection while clearing statement state, and tests prove `GET_LOCK`, protected work, and `RELEASE_LOCK` share the connection.
- Reconciliation could allow a stale-failure decision before a concurrent SQL commit; Redis state is now rechecked while holding the advisory lock and before opening the transaction, with receipt authority winning commit/reconcile races.
- Receipt hydration initially did not reject every deleted or mismatched child/reference graph; ownership, active-row, cardinality, aggregate, message-role/model, and global user-message reference checks now fail closed.
- Retry idempotency initially did not distinguish a request for a new conversation from one for an existing conversation. The receipt now persists `requested_existing_conversation`, and retries must match both that provenance and the exact requested conversation GUID where applicable.
- Reconciliation initially exposed wrapped database/Redis/release details. Unknown dependency failures are now reduced to the stable `platform generation persistence unavailable` sentinel while recognized typed domain errors retain their stable identity; tests reject leaked addresses and dependency text.
- A compatibility guard was added for the configured example model allowlist so every exact model ID is valid UTF-8 and fits the 128-byte persistence column. A 129-byte fixture is explicitly rejected.

The exact full-suite attempt with only `TEST_DATABASE_URL` and `TEST_REDIS_URL` did not pass because the isolated action-key test prerequisite was absent. It is classified as `FAIL_ENV_PREREQUISITE`, not a product or test pass. The succeeding fresh run supplied a one-use, locally valid test-only `ACTION_SECURITY_HMAC_KEY` without recording its value, recreated and fully migrated the same dedicated `*_test` database, and then ran the full suite.

## GREEN evidence

The final focused real-fixture race gate passed without fixture skips after the security fixes:

```text
internal/migration  PASS  4.964s
internal/models     PASS  2.099s
internal/service    PASS  30.521s
internal/app        PASS  1.974s
```

Coverage includes 0011 schema, exact metadata verification, constraints and rerun behavior; case-distinct opaque model IDs; globally unique user-message receipt references; owned receipt hydration; exact duplicate input and conversation-provenance comparison; retry-timestamp independence; single/compare atomicity; per-model assistant messages; partial-failure accounting; transaction rollback; duplicate and quota races; commit-unknown resolution; malformed graph rejection; 30-second stale-commit reconciliation; Redis CAS/TTL preservation; stable error normalization; configured-model compatibility; fail-closed AppState wiring; and the inactive v2 behavior boundary.

The fresh complete run, using the same explicit fixture pair, a fully migrated `porsche_generation_secfix_full_test` database, and the unrecorded test-only HMAC prerequisite, passed all packages. Observed package timings included:

```text
internal/service    PASS  98.786s
internal/migration  PASS  63.148s
internal/handler    PASS  25.695s
all remaining Go packages PASS
```

`go vet ./...`, `git diff --check`, tracker JSON parsing, and source checks for the HTTP boundary all passed. The two legacy completion/compare POST routes remain registered, but their v2 behavior branches are not activated and authenticated v2 requests receive the stable 503 response. Generation GET/cancel routes remain unregistered and return 404. This is not a claim that an HTTP stream or real upstream request was exercised.

## Review

- Independent specification review: `SPEC_PASS`.
- Independent implementation-quality review: `IMPLEMENTATION_PASS`.
- Independent security review: `SECURITY_PASS` after conversation-provenance binding, reconciliation error normalization, configured-model compatibility coverage, checksum alignment, and the final focused/full fixture reruns.

BE03 remains below the HTTP boundary. BE04 generation GET/cancel and restart scheduling, BE05 single v2 stream, BE06 compare v2 stream, frontend/proxy/real-model acceptance, production migration, deployment, push, and merge remain separately scoped and unexecuted.
