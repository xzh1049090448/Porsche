# Refresh replay and test isolation implementation plan

> **For agentic workers:** Use subagent-driven-development for implementation, then spec and independent quality review. User approved the written design; retain work locally, without push/merge/deployment.

**Goal:** Reject expired refresh replay without panic, preserving committed revocation, and make integration regressions repeatable.

**Architecture:** Minimal post-transaction guard; Root-only fresh schemas; scoped audit constraint and unique fixture identities. See approved `docs/superpowers/specs/2026-09-02-refresh-replay-test-isolation-design.md`.

**Tech Stack:** Go 1.22, GORM, real disposable MySQL 8 and Redis 7.

## Task 1: Implementation and test-first evidence

Files: `internal/service/auth_session.go`, `auth_session_test.go`, `mysql_test.go`, `auth_registration_test.go`, `auth_account_test.go`, `auth_redis_test.go`; `internal/handler/auth_sessions_integration_test.go`.

- [x] Strengthen `TestRefreshRotationReplayOutsideWindowRevokesSession` using injected `sessions.now`: rotate, advance beyond grace, require nil result and `StatusFromError(err)==401`; query committed revocation and exact one replay audit, check Redis barrier and reject both old/new tokens. Run it before changing production and capture panic.
- [x] After the transaction error check and before publication, implement `if rotated == nil { return nil, errUnauthorized("刷新凭据无效") }`; explain why error must be outside the transaction. Add real MySQL replay audit-failure injection and assert rollback plus retained Redis denial.
- [x] Reproduce Root contamination, audit constraint collision and repeat-run identity failures on fixture. For Root use a `_test.go` helper creating a random exact `porsche_root_<hex>_test` schema via `CREATE DATABASE` (no IF NOT EXISTS). Validate dedicated TEST_DATABASE_URL first, register cleanup only after successful create, close connections before exact DROP. Test isolation and preservation of parent database.
- [x] Root tests alone use that helper and existing explicit migration. Tombstone test asserts its tombstone is the only Root. Other tests retain their shared database and use generated usernames, unique Redis IP/account identifiers and fresh SID values. Audit CHECK becomes `CHECK (user_id <> <fixture user ID> OR event_type <> <password-changed>)`; NULL semantics and original rollback assertions remain intact.
- [x] Run targeted regressions twice on the same fixture, then self-review. No schema relaxations, FLUSH commands, production Redis changes or test skipping.

## Task 2: Independent verification and delivery

- [x] Spec reviewer reads actual diff against the approved design; resolve gaps before quality review.
- [x] Run `go test -p 1 ./... -count=1` with explicit TEST_DATABASE_URL/TEST_REDIS_URL, repeat on the same fixtures with `-shuffle=on`; record failures/skips accurately.
- [x] Run `go test -race -p 1 ./internal/service ./internal/handler -run 'Refresh|RootBootstrap|ChangePassword|AuthRedis|LoginRateLimit|AuthSessionHTTPFlow|UsernameRegistration|RootTest' -count=3` with real fixtures. Run build, vet, JSON and diff checks.
- [x] Independently exercise HTTP refresh replay against a local server/httptest with real dependencies, checking 401 envelope and persisted revocation, not merely status 200 health.
- [x] Complete quality/security review; update progress, feature status and command/output evidence report. Commit only scoped changes locally and stop only task-owned disposable containers.

All Go commands use `GOCACHE=/private/tmp/porsche-go-build-cache`. The initial `bash ./init.sh` passed without TEST_*; this is not full integration evidence.
