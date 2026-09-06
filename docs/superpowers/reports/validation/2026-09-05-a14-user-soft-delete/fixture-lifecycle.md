# A14 user-delete isolated fixture lifecycle

Status: `RETAINED_FOR_TASK_18`.

A first planned suffix, `20260905124612-69ba7580`, did not reach a valid
fixture: MySQL exited before identity verification and `AutoRemove` removed it.
The paired Redis container was stopped by exact name, all same-label resources
were verified absent, and that attempt's private files were removed. No
migration or test used the failed attempt. The replacement suffix below was
generated only after exact cleanup and will not reuse any failed resource.

- Suffix: `20260905125101-20c53025`
- Planned MySQL container: `a14-user-delete-mysql-20260905125101-20c53025`
- Planned Redis container: `a14-user-delete-redis-20260905125101-20c53025`
- Planned namespace database: `a14_user_delete_20260905125101-20c53025`
- Planned test child database: `a14_user_delete_20260905125101-20c53025_test`
- MySQL creation time: `2026-09-05T12:51:41.796054553Z`
- Redis creation time: `2026-09-05T12:51:42.100899761Z`
- MySQL image digest: `mysql@sha256:7dcddc01f13bab2f15cde676d44d01f61fc9f99fe7785e86196dfc07d358ae2b`
- Redis image digest: `redis@sha256:ff02b58f971e7d7d156a1267e283fcbbeee91773b6aa36c49dac28ecfe28eadf`
- MySQL full container ID: `ed6dc7dcf1e4b249a76992aea18e6226bc1bee7f2b0f140c0fa80b5a6fed764b`
- Redis full container ID: `8e4e74b6b04dab1e532712c400506ee47d8bea69e995774f232d61a189ce271b`
- Host bindings: MySQL `127.0.0.1:55060`; Redis `127.0.0.1:55065`
- `AutoRemove`: inspected `true` for both containers
- Root filesystem: inspected read-only for both containers
- MySQL tmpfs: `/var/lib/mysql`, `/var/run/mysqld`, `/var/lib/mysql-files`, and `/tmp`
- Redis tmpfs: `/data` and `/tmp`
- Required task label: `codex.task=a14-user-delete-20260905125101-20c53025`
- Required retention label: `codex.retention=task-18`

The exact namespace database is created to satisfy the plan identity. The `_test`
child exists in the same MySQL tmpfs solely because existing test safety gates
reject databases without that suffix. Every test URL points only to the child;
the safety check is not weakened.

The executor will use only task-owned random credentials stored in
`/tmp/a14-user-delete-20260905125101-20c53025.env` with mode `0600`. The file,
credential values, and connection URLs must never enter repository evidence or
command output. Both images must be pinned to the immutable local image IDs
recorded after inspection and run with `--pull=never`, health checks, read-only
root filesystems, tmpfs-backed writable paths, no named volume, and random
loopback-only host ports. Exact full container IDs, names, image IDs, labels,
mounts, port bindings, and `AutoRemove` are verified immediately after creation.
Any mismatch stops the task before migrations or tests.

Post-test inspection matched both full IDs, names, immutable image IDs, labels,
tmpfs mounts, loopback bindings, `AutoRemove`, read-only roots, and healthy
state. MySQL contains exactly the planned namespace database and its `_test`
child among databases with this task prefix. The final focused and race gates
used only Redis logical database 8. No named volume, remote endpoint, or
unrelated fixture was used. Wrong MySQL and Redis credentials were both
rejected, and the private env file remained mode `0600`.

## Retained-fixture reset and reviewer rerun

The private, executable reset helper is
`/tmp/a14-user-delete-20260905125101-20c53025-reset.sh`, mode `0700`, SHA-256
`b8e59e3ade30afaa6c1090f059127afa98e83a681892bbed845ca208c0d28b04`.
It contains no credential. It reads credentials only from the mode-`0600`
fixture env file and never prints them or either URL.

The helper is idempotent and fail-closed. Before any mutation it verifies both
full container IDs, exact names, fixed image IDs, task and retention labels,
`AutoRemove=true`, read-only roots, required tmpfs paths, healthy state, exact
loopback ports, env-file mode, suffix marker, and the namespace database. It
then drops and recreates only
`a14_user_delete_20260905125101-20c53025_test`, clears only Redis logical DB 8,
and rewrites only the private `TEST_REDIS_URL` selector to DB 8. Its postcheck
requires the two exact database names to exist, the child to contain zero
tables, and a `LEFT(schema_name, CHAR_LENGTH(exact_namespace)) = exact_namespace`
set check to return exactly those two databases. This avoids wildcard `LIKE`
identity checks and rejects any unexpected same-suffix database. It also
requires Redis DB 8 to contain zero keys and
both container identities and health to remain unchanged. Every test URL is
checked to address the `_test` child; the namespace database is never used by
tests.

An independent reviewer can reproduce the exact clean-start gate with:

```sh
test "$(stat -f '%Lp' /tmp/a14-user-delete-20260905125101-20c53025.env)" = 600
test "$(stat -f '%Lp' /tmp/a14-user-delete-20260905125101-20c53025-reset.sh)" = 700
test "$(shasum -a 256 /tmp/a14-user-delete-20260905125101-20c53025-reset.sh | awk '{print $1}')" = b8e59e3ade30afaa6c1090f059127afa98e83a681892bbed845ca208c0d28b04
/tmp/a14-user-delete-20260905125101-20c53025-reset.sh
set -a
source /tmp/a14-user-delete-20260905125101-20c53025.env
set +a
go test -p 1 ./internal/migration ./internal/service ./internal/handler -run 'AdminActionOutbox|AdminOperationSafety|DeleteUser|ActionVerification|ActionOperation' -count=1
go test -race ./internal/service ./internal/handler -run 'DeleteUser.*Concurrent|Action.*Concurrent' -count=1
```

Run the reset once before the focused-plus-race sequence, rather than between
those two commands. The final Task 12 proof reset observed 16 child tables and
103 task-local Redis DB-8 keys before reset, then zero child tables and zero
DB-8 keys.
An immediate second reset observed zero and zero before reset and again ended
at zero and zero, proving the retained clean start is recoverable and
idempotent. The fixture was left at that clean rerun starting point.

The healthy exact fixture is retained exclusively for Task 18 and must not be
removed or reused for unrelated work. Exact cleanup commands to execute only
after Task 18 authorizes cleanup:

```sh
docker stop a14-user-delete-mysql-20260905125101-20c53025
docker stop a14-user-delete-redis-20260905125101-20c53025
rm -f /tmp/a14-user-delete-20260905125101-20c53025.env /tmp/a14-user-delete-20260905125101-20c53025.ids /tmp/a14-user-delete-20260905125101-20c53025.public /tmp/a14-user-delete-20260905125101-20c53025-reset.sh /tmp/a14-user-delete-20260905125101-20c53025-focused-final.json /tmp/a14-user-delete-20260905125101-20c53025-race-final.json
test "$(cat /tmp/a14-user-delete-current-suffix)" != "20260905125101-20c53025" || rm -f /tmp/a14-user-delete-current-suffix
```

These commands are recorded for Task 18 and were not executed in Task 12.

## 2026-09-06 host-restart replacement fixture addendum

The host restarted before independent Task 18 re-review. The two retained
`AutoRemove=true` containers, every `/tmp` private fixture/browser artifact,
and both application processes were absent after restart; Docker Desktop was
also stopped. Their absence is unexpected historical state and is not Step 6
cleanup evidence. Docker Desktop was started only as a local fixture
prerequisite; no production or remote service was accessed.

Before creating a replacement, the executor generated and validated the new
suffix `20260906031001-95b2aeb3` against the required format and wrote the
credential-free private plan
`/tmp/a14-user-delete-20260906031001-95b2aeb3-plan.txt` with mode `0600`.
The replacement must not reuse any old name, port, credential, database, or
Redis selector.

- MySQL container: `a14-user-delete-mysql-20260906031001-95b2aeb3`
- Redis container: `a14-user-delete-redis-20260906031001-95b2aeb3`
- Namespace database: `a14_user_delete_20260906031001-95b2aeb3`
- Disposable child: `a14_user_delete_20260906031001-95b2aeb3_test`
- Loopback bindings: MySQL `127.0.0.1:56060`; Redis `127.0.0.1:56065`
- Immutable local images: MySQL
  `mysql@sha256:7dcddc01f13bab2f15cde676d44d01f61fc9f99fe7785e86196dfc07d358ae2b`;
  Redis
  `redis@sha256:ff02b58f971e7d7d156a1267e283fcbbeee91773b6aa36c49dac28ecfe28eadf`
- Required labels: `codex.task=a14-user-delete-20260906031001-95b2aeb3`
  and `codex.retention=task-18-rerun`
- Required runtime: `AutoRemove=true`, read-only root filesystems, MySQL
  tmpfs at `/var/lib/mysql`, `/var/run/mysqld`, `/var/lib/mysql-files`, `/tmp`,
  Redis tmpfs at `/data`, `/tmp`, and health checks before any migration/test.

Credentials and URLs must exist only in
`/tmp/a14-user-delete-20260906031001-95b2aeb3.env` mode `0600`. The exact reset
helper must be mode `0700`, operate only on the exact `_test` child and Redis
DB 8, and fail before mutation on any container ID/name/image/label/port,
rootfs/tmpfs, health, namespace, suffix or file-mode mismatch. The new full
container IDs, creation times, helper digest, and health result are appended
only after successful exact inspection.

Creation completed after all fail-closed checks:

- MySQL full ID
  `d1e96a2f780a4c604168c320e690f4685e467ace39edd9d701974903ebd807a3`,
  created `2026-09-06T03:13:42.430947303Z`.
- Redis full ID
  `365aa2d572c00f438db7c234273d82388b70d8de186d60b724174e7fa526596d`,
  created `2026-09-06T03:13:42.573491928Z`.
- Both health checks passed at creation and again after the final re-review
  reset; exact name/ID/image/label/port/AutoRemove/read-only checks matched.
- Private plan SHA-256:
  `bb26d5f7f8acea18a1b7a605257aee8fccc32ccf2ed75e6928d98267a8bd5895`.
- Reset helper
  `/tmp/a14-user-delete-20260906031001-95b2aeb3-reset.sh`, mode `0700`,
  SHA-256
  `7cac435442a35ba88a0789c38b0ca882fed271531718c287f08be14c01249873`.

The final mutable-gate sequence reset separately before the full and race
commands, migrated `0001..0006`, and used container-root authority only for
tests that create disposable databases inside this exact isolated container.
The application continues to use its restricted child-database credential.
After all gates, a final helper run observed 16 child tables and 168 Redis DB-8
keys, reset them to zero, reapplied `0001..0006`, and created a new private
local Root test account. At that validation stop, the resources were backend PID `4020` on
`127.0.0.1:57181`, API-only route bridge PID `4045` on `127.0.0.1:8000`, and
frontend PID `4084` on `127.0.0.1:55795`. Backend health and frontend root are
both `200`; a visible-Chrome `/users` smoke passed with zero external requests.

Those three first replacement application holders ended with their agent
lifetime. PIDs `4020`/`4045`/`4084` are a second unexpected absence and are not
Step 6 cleanup evidence. Root-held exec sessions now preserve the same exact
application commits and ports: backend PID `10318` (started 2026-09-06
12:34:28 +08:00), route-bridge PID `10331` (started 12:34:45), and frontend PID
`10376` (started 12:35:04). Read-only inspection matched all three listener
owners, backend health `200`, frontend root `200`, and both unchanged healthy
container identities. These root-held identities are the accepted re-review
and cleanup targets.
