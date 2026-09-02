# Backend test baseline verification

Scope: repair the two previously identified test defects only. Changed `internal/migration/runner_test.go`, `internal/service/mysql_test.go`, `auth_registration_test.go`, `auth_session_test.go`; no production code/schema changes.

Tests used newly-created loopback-only MySQL 8.0.46 / Redis 7 containers, not production or pre-existing local services. URLs below contain public dummy fixture credentials, not production secrets. They expire with container cleanup.

## Check: original failures and regression

**Command run:**

```sh
TEST_DATABASE_URL=mysql://root:BaselineFixtureOnly20260902@127.0.0.1:63054/porsche_baseline_test TEST_REDIS_URL=redis://127.0.0.1:63096/0 GOCACHE=/private/tmp/porsche-go-build-cache go test -p 1 ./internal/migration ./internal/service -run 'TestAuthCoreMigrationOnIsolatedMySQL|TestAuthSessionCreateEvictsOldestAt51|TestLoginUsernameRejectsDisabledAndSoftDeletedUser|TestFixtureUsername' -count=1 -v
```

**Output observed:** Before repair the three existing tests failed with empty type/nullability and MySQL 1406. Direct SQL returned `DATA_TYPE IS_NULLABLE` and `varchar YES`. New helper regression initially failed compilation with `undefined: fixtureUsername`. After repair:

```text
--- PASS: TestAuthCoreMigrationOnIsolatedMySQL (0.06s)
--- PASS: TestLoginUsernameRejectsDisabledAndSoftDeletedUser (0.08s)
--- PASS: TestAuthSessionCreateEvictsOldestAt51 (0.31s)
--- PASS: TestFixtureUsernamePreservesIDWithinSchemaLimit (0.00s)
```

**Result: PASS**. Original behavior assertions are preserved. Boundary probe includes 1, 9, 10, an 18-digit ID, MaxInt64-1 and MaxInt64, validates real production username rules, uniqueness, and full integer round-trip.

## Check: repeated race regression and build

**Command run:**

```sh
TEST_DATABASE_URL=mysql://root:BaselineFixtureOnly20260902@127.0.0.1:63054/porsche_baseline_test TEST_REDIS_URL=redis://127.0.0.1:63096/2 GOCACHE=/private/tmp/porsche-go-build-cache go test -race -p 1 ./internal/migration ./internal/service -run 'TestAuthCoreMigrationOnIsolatedMySQL|TestAuthSessionCreateEvictsOldestAt51|TestLoginUsernameRejectsDisabledAndSoftDeletedUser|TestFixtureUsername' -count=3
GOCACHE=/private/tmp/porsche-go-build-cache go build ./...
GOCACHE=/private/tmp/porsche-go-build-cache go vet ./...
git diff --check
```

**Output observed:**

```text
ok  github.com/porsche/ai-gateway-go/internal/migration 1.979s
ok  github.com/porsche/ai-gateway-go/internal/service 4.935s
```

All commands exited 0; build/vet/diff were silent. **Result: PASS**.

## Check: complete suite on a new empty database

**Command run:**

```sh
set -o pipefail
TEST_DATABASE_URL=mysql://root:BaselineFixtureOnly20260902@127.0.0.1:63054/porsche_baseline_clean_test TEST_REDIS_URL=redis://127.0.0.1:63096/1 GOCACHE=/private/tmp/porsche-go-build-cache go test -json -p 1 ./... -count=1 | jq -s '{passed: ([.[]|select(.Action=="pass" and .Test!=null)]|length), skipped: [.[]|select(.Action=="skip" and .Test!=null)|.Test], failed: [.[]|select(.Action=="fail")|{Package,Test}], diagnostics: [.[]|select(.Action=="output" and (.Output|test("_test.go:[0-9]+:|panic:|fatal error:")))|.Output]}'
```

**Output observed:** `passed: 225` (includes subtests), `skipped: []`, exit 1. Service failures:

```text
TestChangePasswordRollsBackMySQLWhenPasswordAuditWriteFails
auth_account_test.go:101: add isolated password audit failure constraint: Error 3819 (HY000): Check constraint 'chk_change_password_audit_353562237791502336' is violated.
TestRootBootstrapCreatesOnlyTheFirstRoot
auth_registration_test.go:149: bootstrap root = (*models.User)(nil), <nil>
TestRefreshRotationReplayOutsideWindowRevokesSession
panic: runtime error: invalid memory address or nil pointer dereference
```

**Result: FAIL**. CHECK injection scans existing password-change audit rows; Root bootstrap sees earlier fixtures' Root rows. A repeated run against a reused database additionally fails fixed-username registration, confirming missing fixture isolation. No test assertions were relaxed to hide this. The panic aborts the remaining tests in the service test process: zero explicit skips is not proof that every remaining test executed.

## Check: independently reproduce Refresh panic

**Command run:**

```sh
TEST_DATABASE_URL=mysql://root:BaselineFixtureOnly20260902@127.0.0.1:63054/porsche_baseline_test TEST_REDIS_URL=redis://127.0.0.1:63096/0 GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service -run '^TestRefreshRotationReplayOutsideWindowRevokesSession$' -count=1 -v
```

**Output observed:**

```text
--- FAIL: TestRefreshRotationReplayOutsideWindowRevokesSession (1.17s)
panic: runtime error: invalid memory address or nil pointer dereference [recovered]
internal/service.(*SessionService).Refresh
internal/service/auth_session.go:203
```

**Result: FAIL**. This is not test-state contamination: replay revocation commits successfully without assigning `rotated`; post-transaction code dereferences `rotated.Session`. Correcting this requires a separately approved production-code change. HTTP/production impact was not exercised; do not infer process termination or a specific live HTTP response from this service-level panic.

No push, merge, deployment, production access or issue closure. The two original test defects are fixed, but complete backend verification remains blocked. Retain the isolated branch and obtain approval for the replay flow plus fixture-isolation follow-up. Both task-owned disposable containers were stopped by verified full IDs; automatic removal destroyed only their temporary fixture data, with no user volumes touched.

VERDICT: FAIL
