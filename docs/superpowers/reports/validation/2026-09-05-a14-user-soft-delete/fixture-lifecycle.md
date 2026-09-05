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
`85e931a2f833f54bb2507e1d39b675bb2f87b56ba3e2bc1fcc6fe50198bce8f7`.
It contains no credential. It reads credentials only from the mode-`0600`
fixture env file and never prints them or either URL.

The helper is idempotent and fail-closed. Before any mutation it verifies both
full container IDs, exact names, fixed image IDs, task and retention labels,
`AutoRemove=true`, read-only roots, required tmpfs paths, healthy state, exact
loopback ports, env-file mode, suffix marker, and the namespace database. It
then drops and recreates only
`a14_user_delete_20260905125101-20c53025_test`, clears only Redis logical DB 8,
and rewrites only the private `TEST_REDIS_URL` selector to DB 8. Its postcheck
requires the namespace database to remain, the child to contain zero tables,
exactly the two suffix databases to exist, Redis DB 8 to contain zero keys, and
both container identities and health to remain unchanged. Every test URL is
checked to address the `_test` child; the namespace database is never used by
tests.

An independent reviewer can reproduce the exact clean-start gate with:

```sh
test "$(stat -f '%Lp' /tmp/a14-user-delete-20260905125101-20c53025.env)" = 600
test "$(stat -f '%Lp' /tmp/a14-user-delete-20260905125101-20c53025-reset.sh)" = 700
test "$(shasum -a 256 /tmp/a14-user-delete-20260905125101-20c53025-reset.sh | awk '{print $1}')" = 85e931a2f833f54bb2507e1d39b675bb2f87b56ba3e2bc1fcc6fe50198bce8f7
/tmp/a14-user-delete-20260905125101-20c53025-reset.sh
set -a
source /tmp/a14-user-delete-20260905125101-20c53025.env
set +a
go test -p 1 ./internal/migration ./internal/service ./internal/handler -run 'AdminActionOutbox|DeleteUser|ActionVerification|ActionOperation' -count=1
go test -race ./internal/service ./internal/handler -run 'DeleteUser.*Concurrent|Action.*Concurrent' -count=1
```

Run the reset once before the focused-plus-race sequence, rather than between
those two commands. The final Task 12 proof reset observed 16 child tables and
106 task-local Redis DB-8 keys before reset, then zero child tables and zero
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
