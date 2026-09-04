# B1-D real fixture validation

Date: 2026-09-04. Writer verdict: `PASS_PENDING_INDEPENDENT_QA_AND_EXACT_CLEANUP`.
This writer result is superseded by `independent-qa/SECURITY_REPORT.md`:
functional/security/integration PASS, overall `PARTIAL / PERFORMANCE_BLOCKED`
because independent warm P95 was 633.956ms FAIL, 466.753ms PASS, and
504.793ms FAIL.
`go-016` remains `in_progress`; this evidence does not complete the full PRD,
the 26 joint cases, frontend-to-backend acceptance, or the legacy write routes.

## Isolation and migration

- Task label: `codex.task=b1d-admin-users-read-260904`.
- Database: `porsche_b1d_admin_users_260904_test` in the task-only MySQL 8.0.46 container.
- Redis: task-only Redis 7.4.11 container.
- Both ports bind only to `127.0.0.1`, use Docker dynamic ports, AutoRemove, tmpfs data, no named volumes, and the frozen local image IDs recorded in `resource-ledger.md`.
- Credentials and connection URLs exist only in a 0700 private task directory with 0600 files. They are absent from this directory and all raw logs.
- Existing migrations 0001/0002/0003 were run by the compiled `cmd/migrate` binary from a directory without `.env`. The subprocess received only `DATABASE_URL`, `APP_ENV=test`, and `SNOWFLAKE_NODE_ID=904`. Ledger checksums exactly match the three source files.
- Test commands explicitly unset `APP_ENV`, `SNOWFLAKE_NODE_ID`, `DATABASE_URL`, `REDIS_URL`, and `RUN_START_COMMAND`; only `TEST_DATABASE_URL` and `TEST_REDIS_URL` were loaded from the private task file.

## Final results

| Gate | Final result |
| --- | --- |
| Focused service | 46 terminal PASS, 42 leaf PASS, 0 SKIP/FAIL |
| Focused handler | 17 terminal PASS, 8 leaf PASS, 0 SKIP/FAIL |
| Race service | 46 terminal PASS, 42 leaf PASS, 0 SKIP/FAIL |
| Race handler | 17 terminal PASS, 8 leaf PASS, 0 SKIP/FAIL |
| Fresh serial full | 691 terminal PASS, 1 SKIP, 0 FAIL; 631 leaf PASS, 1 SKIP; 15 package PASS, 4 no-test package SKIP |
| Frozen 404 focused | 6 terminal PASS, 5 leaf PASS, 0 SKIP/FAIL |
| Build / vet / diff-check | exit 0 / exit 0 / exit 0 |
| 100k performance | PASS; 200 measured requests, first application read 96.890 ms, warm P95 340.023 ms |

The single full-suite skip is the explicitly opt-in performance test. It was
then run separately with `B1D_RUN_PERFORMANCE=1` and passed. Seeding 100000
users warms MySQL, so the first application request is not a disk-cold sample;
true disk-cold performance remains `NOT_RUN`.

Performance host: macOS arm64, model `Mac15,6`, 12 logical CPUs,
19327352832 bytes host memory. Both containers report zero per-container CPU
and memory limits in HostConfig. This does not expose the Docker Desktop VM's
own resource ceiling.

## Real-fixture findings and narrow test fixes

No production implementation changed. Four test files changed:

1. The B1-D literal-LIKE fixture username exceeded the real `VARCHAR(20)`.
   It now retains `%`, `_`, and `!` while using a short GUID-derived suffix;
   the initial RED is preserved in `focused-initial-fixture-bug.jsonl`.
2. Two historical legacy-read tests still expected 403 for an Admin reading an
   equal-role Admin or Root. The frozen B1-D lower-target hiding contract is
   404, matching the already-frozen production implementation. Only those stale
   assertions changed; the initial full failure and the focused GREEN are both
   retained for PM and independent QA review.
3. The 100k test generated decimal-GUID usernames longer than `VARCHAR(20)`.
   It now uses an exact unique base36 GUID string. `performance-initial.log`
   preserves the real RED and `performance.log` is the final GREEN.

The first combined service+handler focused command let Go execute two packages
against one mutable fixture concurrently. Its failure is retained in
`focused-parallel-package-fail.jsonl`; the final service and handler commands
were run separately. A MySQL-only reset also left Redis issue-limit counters,
which caused one historical session test to fail; the evidence is retained in
`full-mysql-only-reset-redis-counter-fail.jsonl`. Final fresh full reset both
task-only MySQL and task-only Redis before migration and execution.

## Remaining gate

The exact containers and private credentials are retained for independent QA.
After QA, Root must verify exact IDs, names, label and image IDs, then stop only
those IDs. AutoRemove must remove both containers, followed by removal of only
the private task files. No volume, prune, wildcard, production, commit, push,
deployment, external provider, or frontend joint test operation was performed.
