# Refresh replay / integration isolation verification

## Scope

Original task: continue resolving project issues; user explicitly approved fixing the discovered Refresh replay panic and test contamination, then approved the written design. Local branch only; no push, merge, issue closure, production access, schema migration changes or deployment.

Production change is restricted to the successful-transaction / no-rotation result in `internal/service/auth_session.go`. Tests cover durable replay revocation, dependency-failure rollback, real TLS HTTP behavior, Root isolation, scoped audit failure and reusable fixture identities. The approved design and implementation plan are adjacent under `../specs/` and `../plans/`.

## Fixture

Task-owned Docker containers, MySQL data in tmpfs, Redis persistence disabled, loopback-only random published ports:

- MySQL `porsche-replay-mysql-20260902`, ID `3a6940e43b95f3e4e94c3b6d3db5d4a765cb9348c91e2cdae7b5a9f5776c74a2`, MySQL 8.0.46, port 61087.
- Redis `porsche-replay-redis-20260902`, ID `f39733e330a0260d92a2821f57254a9c358569e925b9c4a838c4c404f7d3dd26`, port 61144.
- `porsche_replay_test` + Redis DB 0 for RED/targeted tests; newly created `porsche_replay_clean_test` + initially empty Redis DB 1 reserved for full/repeat/race tests. All passwords below are public disposable fixture values, not production credentials.

### Check: pre-change basic initialization

**Command run:**
```sh
GOCACHE=/private/tmp/porsche-go-build-cache bash ./init.sh
```
**Output observed:**
```text
go version go1.22.12 darwin/arm64
ok github.com/porsche/ai-gateway-go/internal/service (cached)
ok github.com/porsche/ai-gateway-go/internal/whitelabel (cached)
```
**Result: PASS** for initialization only. No TEST_* were set; cached/default tests do not prove integration acceptance.

### Check: HTTP replay regression catches the original bug (RED)

**Command run:**
```sh
TEST_DATABASE_URL=mysql://root:ReplayFixtureOnly20260902@127.0.0.1:61087/porsche_replay_test TEST_REDIS_URL=redis://127.0.0.1:61144/0 GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/handler -run '^TestRefreshReplayHTTPRejectsAfterCommittedRevocation$' -count=1
```
**Output observed:**
```text
--- FAIL: TestRefreshReplayHTTPRejectsAfterCommittedRevocation (0.49s)
    auth_refresh_replay_integration_test.go:90: replay status=500 cookies=0; want 401 with no issued cookie
FAIL github.com/porsche/ai-gateway-go/internal/handler 1.431s
```
**Result: PASS** for RED evidence: actual pre-fix request returned 500 under Gin recovery instead of the required 401. This is not a production test.

### Check: full suite on fresh database and Redis DB (GREEN)

**Command run:**
```sh
set -o pipefail
TEST_DATABASE_URL=mysql://root:ReplayFixtureOnly20260902@127.0.0.1:61087/porsche_replay_clean_test TEST_REDIS_URL=redis://127.0.0.1:61144/1 GOCACHE=/private/tmp/porsche-go-build-cache go test -json -p 1 ./... -count=1 | jq -s '{passed: ([.[] | select(.Action=="pass" and .Test!=null)]|length), skipped: [.[] | select(.Action=="skip" and .Test!=null) | .Test], failed: [.[] | select(.Action=="fail") | {Package,Test}], diagnostics: [.[] | select(.Action=="output" and (.Output | test("_test.go:[0-9]+:|panic:|fatal error:"))) | .Output]}'
```
**Output observed:**
```json
{
  "passed": 247,
  "skipped": [],
  "failed": [],
  "diagnostics": [
    "    auth_refresh_replay_integration_test.go:116: HTTPS: register=201 login=200 rotate=200 replay=401 repeat=401 current-refresh=401 self=401; committed revocation, one audit, Redis barrier verified\n"
  ]
}
```
**Result: PASS**. Exit 0. Counts include subtests. All opt-in integration tests were enabled; no skip events.

### Check: repeat and shuffle without clearing state

**Command run:**
```sh
set -o pipefail
TEST_DATABASE_URL=mysql://root:ReplayFixtureOnly20260902@127.0.0.1:61087/porsche_replay_clean_test TEST_REDIS_URL=redis://127.0.0.1:61144/1 GOCACHE=/private/tmp/porsche-go-build-cache go test -json -p 1 ./... -count=2 -shuffle=on | jq -s '{passed: ([.[] | select(.Action=="pass" and .Test!=null)]|length), skipped: [.[] | select(.Action=="skip" and .Test!=null) | .Test], failed: [.[] | select(.Action=="fail") | {Package,Test}], diagnostics: [.[] | select(.Action=="output" and (.Output | test("_test.go:[0-9]+:|panic:|fatal error:|shuffle"))) | .Output]}'
```
**Output observed (abridged):**
```text
"passed": 494,
"skipped": [],
"failed": [],
"-test.shuffle 1788364628201306000\n",
"    auth_refresh_replay_integration_test.go:116: HTTPS: register=201 login=200 rotate=200 replay=401 repeat=401 current-refresh=401 self=401; committed revocation, one audit, Redis barrier verified\n",
"-test.shuffle 1788364635912411000\n",
```
**Result: PASS**. Exit 0. No database/Redis reset between runs; 494 pass events include subtests and both repetitions.

### Check: repeated race / adversarial authentication probes

**Command run:**
```sh
set -o pipefail
TEST_DATABASE_URL=mysql://root:ReplayFixtureOnly20260902@127.0.0.1:61087/porsche_replay_clean_test TEST_REDIS_URL=redis://127.0.0.1:61144/1 GOCACHE=/private/tmp/porsche-go-build-cache go test -json -race -p 1 ./internal/service ./internal/handler -run 'Refresh|RootBootstrap|ChangePassword|AuthRedis|LoginRateLimit|AuthSessionHTTPFlow|UsernameRegistration|RootTest' -count=3 | jq -s '{passed: ([.[] | select(.Action=="pass" and .Test!=null)]|length), skipped: [.[] | select(.Action=="skip" and .Test!=null) | .Test], failed: [.[] | select(.Action=="fail") | {Package,Test}], diagnostics: [.[] | select(.Action=="output" and (.Output | test("_test.go:[0-9]+:|panic:|fatal error:|DATA RACE"))) | .Output]}'
```
**Output observed (abridged):**
```text
"passed": 60,
"skipped": [],
"failed": [],
"    auth_refresh_replay_integration_test.go:116: HTTPS: register=201 login=200 rotate=200 replay=401 repeat=401 current-refresh=401 self=401; committed revocation, one audit, Redis barrier verified\n"
```
**Result: PASS**. Exit 0. Packages are serial (`-p 1`) while existing concurrent-refresh tests still launch real concurrent requests. Probes cover same-result concurrency, A→B→C generation, expired replay, repeated/current-token rejection, audit-write rollback with retained denial barrier, and Root tombstone isolation. Actual local TLS requests verify response envelope, no issued cookie, access rejection and exact single durable replay audit.

### Check: build, vet, diff and JSON

**Command run:**
```sh
GOCACHE=/private/tmp/porsche-go-build-cache go build ./... && GOCACHE=/private/tmp/porsche-go-build-cache go vet ./... && git diff --check && jq empty feature_list.json
```
**Output observed:** no output, exit 0.

**Result: PASS**.

### Check: owned schema / fault constraint cleanup

**Command run:**
```sh
docker exec porsche-replay-mysql-20260902 sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot --batch --skip-column-names -e "SELECT COUNT(*) AS owned_root_schemas_remaining FROM information_schema.schemata WHERE schema_name LIKE '\''porsche_root_%_test'\''; SELECT COUNT(*) AS injected_constraints_remaining FROM information_schema.table_constraints WHERE constraint_schema IN ('\''porsche_replay_test'\'', '\''porsche_replay_clean_test'\'') AND (constraint_name LIKE '\''chk_replay_audit_%'\'' OR constraint_name LIKE '\''chk_change_password_audit_%'\'');"'
```
**Output observed:**
```text
0
0
```
**Result: PASS**. Parent test data remained available throughout repeated tests; no Root fixture databases or injected constraints leaked.

### Check: independent quality/security regression probe

**Command run (independent reviewer):**
```sh
TEST_DATABASE_URL=mysql://root:ReplayFixtureOnly20260902@127.0.0.1:61087/porsche_replay_clean_test TEST_REDIS_URL=redis://127.0.0.1:61144/1 GOCACHE=/private/tmp/porsche-go-build-cache go test -p 1 ./internal/service ./internal/handler -run '^(TestRefreshRotationReplayOutsideWindowRevokesSession|TestRefreshReplayAuditFailureRollsBackMySQLAndRetainsRedisBarrier|TestRootTestMySQLIsolatesFixturesAndPreservesParent|TestRefreshReplayHTTPRejectsAfterCommittedRevocation)$' -count=1 -v
```
**Output observed (reviewer report):** four top-level tests and the Root subtest PASS, exit 0, no skips. HTTP evidence: `register=201 login=200 rotate=200 replay=401 repeat=401 current-refresh=401 self=401`; committed revocation, single audit and Redis barrier verified. Expected injected CHECK rejection demonstrated rollback.

**Result: PASS**. Initial sandbox attempt could not connect to loopback; scoped elevated retry passed. Reviewer found no blocking or optional findings. Spec compliance had independently passed first.

### Check: disposable fixture teardown

**Command run (after exact ID / task label / AutoRemove inspection):**
```sh
docker stop 3a6940e43b95f3e4e94c3b6d3db5d4a765cb9348c91e2cdae7b5a9f5776c74a2 f39733e330a0260d92a2821f57254a9c358569e925b9c4a838c4c404f7d3dd26 && docker ps -a --filter label=codex.task=replay-isolation --format '{{.ID}} {{.Names}}'
```
**Output observed:**
```text
3a6940e43b95f3e4e94c3b6d3db5d4a765cb9348c91e2cdae7b5a9f5776c74a2
f39733e330a0260d92a2821f57254a9c358569e925b9c4a838c4c404f7d3dd26
```
**Result: PASS**. Exit 0, no task-labelled containers remained. Temporary fixture data was destroyed; existing containers and named volumes were untouched. Commands containing the old ports cannot be rerun after teardown without creating fresh disposable fixtures and replacing the ports.

## Scope remaining (completed 2026-09-03 Asia/Shanghai)

Independent spec and quality/security review approved the actual diff. These tests do not replace the outstanding real JieKou upstream catalog/detail/Chat/SSE acceptance (go-004 / Issue #2). No production rollout, push/merge or remote issue closure was performed.

VERDICT: PASS
