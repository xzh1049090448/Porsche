# Backend test baseline repair

> Execution: inline, using systematic-debugging and test-driven-development. User approved continuation of these two previously reproduced test defects.

**Goal:** Restore real MySQL/Redis integration tests without changing production behavior or schema.

**Architecture:** Keep changes in `_test.go`: scan column metadata positionally to avoid driver result-label casing; use a short prefix and the full Snowflake integer for unique usernames within 20 characters. Preserve assertions, MySQL-only isolation guards and real dependencies.

**Tech stack:** Go 1.22, GORM, MySQL 8, Redis 7.

## Execution checklist

- [ ] Run `bash ./init.sh`, then reproduce migration metadata and two username fixture failures in fresh disposable loopback-only MySQL/Redis containers.
- [ ] Inspect raw column labels/values; in `internal/migration/runner_test.go`, replace struct-name scanning with `Row().Scan(&result.DataType, &result.IsNullable)`. Keep all type/nullability assertions and propagate missing-row/query errors.
- [ ] Add boundary/uniqueness regression in `internal/service/mysql_test.go` for `fixtureUsername(int64)`: positive IDs through MaxInt64 must normalize under the production username validator, fit 20 characters, and remain distinct. Observe RED before implementing `return fmt.Sprintf("u%019d", guid)` (padding also meets the minimum length for tiny test IDs).
- [ ] Replace only overlong username creation in `auth_session_test.go` and `auth_registration_test.go` with `fixtureUsername(testSnowflake.Next())`; remove unused imports.
- [ ] Run original three regressions, all packages serially against real fixtures with `go test -p 1 ./... -count=1`, and race tests, build, vet. Do not hide failures with skipped tests or schema changes.
- [ ] Record evidence, preserve Issue #2 real-upstream block, commit locally, and stop only this task's disposable containers. Do not push, merge or deploy.
