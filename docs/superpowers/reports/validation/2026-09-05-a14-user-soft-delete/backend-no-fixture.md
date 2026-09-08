# A14 backend no-fixture validation

Date: 2026-09-05

Validation window: 2026-09-05T20:21:47+08:00 through 2026-09-05T20:23:02+08:00

Validated commit: `1a94044320dc7e865fc73badc4c5d048f341e990` (`test: prove legacy delete route isolation`)

## Scope and environment boundary

This run validates Tasks 2–10 without an external fixture. Every required test command used `env -u TEST_DATABASE_URL -u TEST_REDIS_URL`; no test database, Redis instance, Docker resource, frontend, deployment, production migration, or production data was accessed. No credential or environment value is recorded in this report.

The repository initializer first failed inside the default sandbox because the Go build cache under the user cache directory was not writable. This was classified as `SANDBOX_INFRASTRUCTURE`, not a product failure. It was rerun with only `GOCACHE` redirected to a task-specific writable temporary directory and with `TEST_DATABASE_URL`, `TEST_REDIS_URL`, `DATABASE_URL`, `REDIS_URL`, `APP_ENV`, and `RUN_START_COMMAND` explicitly unset:

```bash
GOCACHE=/private/tmp/porsche-a14-task11-gocache env -u TEST_DATABASE_URL -u TEST_REDIS_URL -u DATABASE_URL -u REDIS_URL -u APP_ENV -u RUN_START_COMMAND ./init.sh
```

Result: exit 0; all packages passed or reported `[no test files]`; the script did not start the application.

## Required gates

All required commands completed with exit code 0.

| Gate | Exact command | Result |
| --- | --- | --- |
| Focused packages | `env -u TEST_DATABASE_URL -u TEST_REDIS_URL go test -p 1 ./internal/actionsecurity ./internal/dto ./internal/models ./internal/migration ./internal/service ./internal/handler ./internal/router ./internal/app -count=1` | PASS: 8/8 packages |
| Focused race | `env -u TEST_DATABASE_URL -u TEST_REDIS_URL go test -race ./internal/actionsecurity ./internal/service ./internal/handler -run 'UserDelete|Action' -count=1` | PASS: 3/3 packages, no race report |
| Full repository | `env -u TEST_DATABASE_URL -u TEST_REDIS_URL go test -p 1 ./... -count=1` | PASS: 16 tested packages; 4 packages had no test files |
| Build | `go build ./...` | PASS |
| Vet | `go vet ./...` | PASS |
| Diff | `git diff --check` | PASS |

For exact event accounting, the same three test gates were rerun with Go's `-json` flag and their output was kept only in task-specific temporary files outside the repository:

| JSON accounting run | Test PASS | Test FAIL | Test SKIP | Package PASS | Package FAIL | Package SKIP |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Focused packages | 763 | 0 | 285 | 8 | 0 | 0 |
| Focused race | 282 | 0 | 26 | 3 | 0 | 0 |
| Full repository | 1047 | 0 | 285 | 16 | 0 | 4 |

The 285 full-run test SKIP events are explicit and exhaustive:

- 168 require an isolated `TEST_DATABASE_URL` MySQL fixture.
- 58 require both isolated `TEST_DATABASE_URL` and `TEST_REDIS_URL`, explicitly stating that no fixture provision or migration occurred.
- 44 require an explicitly configured `TEST_REDIS_URL` for Redis authentication tests.
- 11 require both isolated fixture variables.
- 2 require a disposable `TEST_REDIS_URL` to execute the production Lua scripts.
- 1 is the opt-in 100k synthetic-user performance fixture, outside this batch.
- 1 is the isolated MySQL migration runner and reports that `TEST_DATABASE_URL` is not set.

These SKIPs are retained as NOT RUN fixture coverage. They are not treated as integration acceptance; Task 12 owns the isolated real MySQL/Redis proof.

## Production boundary and secret scans

The required scans were run exactly as follows and each completed with exit code 0:

```bash
rg -n 'current_password|X-Action-Ticket|Idempotency-Key|reason' internal --glob '*.go'
rg -n 'ActionUsers(CreateAdmin|ResetPassword|Promote|Demote|PermissionsWrite)|ActionPublicContent' internal --glob '*.go'
rg -n 'SoftDeleteUser\(' internal --glob '*.go'
```

Manual review conclusions:

- The first scan returned 239 lines, including unrelated fixed diagnostic reason enums and test fixtures. In A14 production code, `current_password` is confined to the strict DTO decoder; the handler passes owned password bytes to verification and clears them with `defer clear`. `X-Action-Ticket` and `Idempotency-Key` occur in exact header validation and operation binding. No production logging call includes an A14 password, deletion reason, ticket, idempotency key, or their request values.
- The deletion reason is normalized and bounded before use. It is intentionally persisted in the official management audit detail for `users.delete`. It is absent from the outbox row, authentication audit, public response/error envelope, and operation view. This is the approved audit boundary rather than a secret leak.
- The inactive-action scan returned 61 lines. The 14 production-code matches are limited to action type declarations and the `inactiveActionDescriptors` catalogue/encoders. `ActiveActionRegistry()` constructs capacity one, selects only `ActionUsersDelete`, marks only that copied descriptor active, and returns immediately. No inactive action is routed or consumed.
- The legacy scan returned exactly one line: the declaration of `AuthService.SoftDeleteUser`. There are zero call sites under `internal`; the retired legacy `DELETE /admin/users/:guid` handler cannot invoke it and remains a fixed authenticated 410 response.

## Boundary verdict

`PASS_NO_FIXTURE`

Tasks 2–10 pass all local no-fixture tests, focused race checks, build, vet, diff, route/action inventory, and secret-boundary review at commit `1a94044320dc7e865fc73badc4c5d048f341e990`. Real migration and transactional deletion semantics remain deliberately unclaimed until Task 12. Production migration, deployment, push, physical deletion, restore, unrelated management actions, and frontend work remain outside this validation.
