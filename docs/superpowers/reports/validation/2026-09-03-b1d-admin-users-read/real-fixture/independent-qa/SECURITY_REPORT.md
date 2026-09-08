# B1-D independent real-fixture QA and security report

Date: 2026-09-04. Verdict: **PARTIAL — functional and isolation gates pass; the 100k warm-P95 performance gate fails.** The retained fixture remains available for Root's exact cleanup window.

## Scope and isolation

The exact MySQL container `94e8463c0411eeceb4b31d3a5ff78f9da715eec937e7d3cd4e48b5fd25680fcd` and Redis container `2711acfbc9aa7b3784b0b7b81854645b5c8f740e170bf7cb5c7290eaff3ebad0` were inspected before use. Their names, task label, frozen image IDs, running state, AutoRemove, tmpfs data paths, loopback-only publishing, and absence of named volumes all matched `resource-ledger.md`. No conflicting resource was adopted.

Only `porsche_b1d_admin_users_260904_test` was recreated; only the task Redis was flushed. Existing migrations 0001--0003 were applied by the private existing migration binary. `migration-*.log` records their non-secret checksums. Each test command unset `APP_ENV`, `SNOWFLAKE_NODE_ID`, `DATABASE_URL`, `REDIS_URL`, and `RUN_START_COMMAND`, silently loaded the private fixture file, and passed only `TEST_DATABASE_URL` and `TEST_REDIS_URL` to the test process. The sandbox's loopback denial was observed before connection and did not reach the fixture; authorized out-of-sandbox runs produced the retained raw evidence.

The QA evidence scan found no connection URL, password value, token, or Authorization credential. No production code, production test assertion, migration, production configuration, container, volume, or private credential file was changed or removed.

## Static and security review

- All nine frozen production SHA256 values match `manifest.json` again.
- The only legacy assertion changes are `admin -> same Admin` and `admin -> Root`, both 403 to 404. The B1-D design explicitly requires those known non-lower targets to be hidden, so the test changes match the frozen contract.
- Data-isolation review found no unexpected mount, external endpoint, named volume, or non-loopback publish.
- Evidence hygiene review found no secret value in writer or independent-QA evidence.

Security findings: Critical 0, High 0, Medium 0, Low 0. The performance defect below is a release/acceptance blocker, not a credential or authorization bypass finding.

## Functional verification

| Gate | named terminal tests | leaf tests | packages | result |
| --- | ---: | ---: | ---: | --- |
| Focused service | 46 pass | 42 pass | 1 pass | PASS |
| Focused handler | 17 pass | 8 pass | 1 pass | PASS |
| Race service | 46 pass | 42 pass | 1 pass | PASS |
| Race handler | 17 pass | 8 pass | 1 pass | PASS |
| Fresh serial full `go test -p 1 ./...` | 691 pass, 1 skip | 631 pass, 1 skip | 15 pass, 4 no-test skips | PASS |
| `go build ./...`, `go vet ./...`, `git diff --check` | — | — | — | PASS / PASS / PASS |

The full-suite skip is only the explicit opt-in 100k performance test. Raw JSON is preserved as `focused-*.jsonl`, `race-*.jsonl`, and `full.jsonl`.

## Performance investigation

The first independent 100k run failed: first application read 117.260 ms; warm P95 633.956 ms. A fresh identical reproduction passed narrowly at 466.753 ms. A third fresh identical run failed at 504.793 ms. All runs used 100,000 synthetic active User rows, 10 concurrent workers, 20 requests per worker, page size 20, and a database already warmed by seeding. Therefore true disk-cold remains `NOT_RUN`.

This is not safe to classify as harmless fixture noise. `mysql-query-components.txt` shows the count half of the exact list shape uses only `idx_users_active_updated` for `is_deleted=0`, then filters role/status while scanning 100,001 rows; its single-query actual time is 312 ms. The page half uses reverse `uk_users_guid` and completes in about 0.09 ms. Under ten concurrent HTTP requests, repeated full count scans explain the unstable warm P95 results. The current schema has no index that covers the list's `is_deleted + status + role` count filter.

Recommended next action for the implementation owner: design and review a forward MySQL migration plus query/index validation for the exact-count path, then rerun this retained-fixture performance gate. Do not relax the 500 ms threshold or edit the performance assertion. A composite index may reduce table lookups, but exact `total` over a nonselective 100k fixture still requires a measured revalidation; this report does not assume an index alone will close the gate.

## Remaining boundaries

This QA does not establish disk-cold performance, frontend-backend acceptance, the 26 joint cases, legacy writes, other admin GET authorization, deployment, or full PRD acceptance. Root may now perform the planned exact-ID cleanup only after deciding how to handle the failed performance gate.
