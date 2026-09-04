# B1-D H3 independent QA and security report

Date: 2026-09-04. Verdict: **PASS** for the H3 retained-fixture candidate. The result is bounded to this fixture and does not establish disk-cold, frontend, deployment, or complete-PRD acceptance. Containers and private fixture files remain retained; this QA performed no cleanup.

## Frozen baseline and isolation

- Manifest SHA256 matched: `26c063e0d31a4aa10bbd41df3396e965cdb9d186a88eecb9e0d23577aa58e2a5`.
- H3 implementation SHA256 matched: `internal/service/admin_users_read.go` = `7a20df0cb257feb393f930b0d006fc88795acbc0b9dead2784f5ae3b61d4f715`.
- Immutable 0004 checksum matched source and migration ledger: `44b5caba0473162c239e6b3035e6d9067e3af0b35e998a79c827a1424621494e`.
- Exact MySQL and Redis IDs, names, task label, frozen images, running state, AutoRemove, tmpfs storage, loopback-only publishing, and lack of named volumes were re-inspected before use.
- Every mutable phase reset only the task database and task Redis, migrated 0001--0004, and silently loaded the private fixture values. No production connection variable was available to a test.

## Functional verification

| Gate | named tests | leaf tests | package events | result |
| --- | ---: | ---: | ---: | --- |
| Migration and service focused | 48 pass | 44 pass | 2 pass | PASS |
| Handler focused | 17 pass | 8 pass | 1 pass | PASS |
| Service race | 47 pass | 43 pass | 1 pass | PASS |
| Handler race | 17 pass | 8 pass | 1 pass | PASS |
| Fresh serial full `go test -p 1 ./...` | 693 pass, 1 skip | 633 pass, 1 skip | 15 pass, 4 no-test skips | PASS |
| Build, vet, diff check | — | — | — | PASS / PASS / PASS |

The one full-suite skip is only the explicit opt-in performance test, which was then run separately three times. `dynamic-contract.log` records actual-MySQL coverage of active/disabled/deleted state, role/status, literal `%`/`_`/`!` LIKE behavior, canonical GUID search, all supported sort directions, null ordering, exact total, and empty-page `items` preservation. `TestAdminUsersListStatementReusesPredicateAndArguments` also confirms the dynamic WHERE and its two escaped LIKE binds are generated once, used exactly twice, copied in order, and retain the limit/offset order.

## Three independent fresh performance runs

Each run independently reset the task MySQL and Redis fixture, applied 0001--0004, seeded 100,000 synthetic users, ran `ANALYZE TABLE users`, and issued 10 workers times 20 HTTP reads with page size 20.

| Run | ANALYZE ms | first application read ms | warm P95 ms | result |
| --- | ---: | ---: | ---: | --- |
| 1 | 4.544 | 36.160 | 113.688 | PASS |
| 2 | 4.323 | 39.986 | 76.997 | PASS |
| 3 | 4.401 | 37.047 | 78.737 | PASS |

All three P95 values are below the 500 ms hard gate. Seeding and ANALYZE warm the database, therefore disk-cold remains `NOT_RUN`.

After the third run, `post-perf-query-plan.txt` recorded total `100000` and `20` page items. The direct B0 count used `idx_users_active_updated` and took about 69.8 ms; H3's count-local `IGNORE INDEX FOR JOIN (idx_users_active_updated)` selected the primary-key range scan and took about 29.4 ms. The unhinted page used reverse `uk_users_guid` and took about 0.05 ms. The forward 0004 index exists with nonunique ordered columns `(is_deleted, role, status)`; H3 deliberately does not force that low-selectivity index for the all-active fixture.

## Static security and contract review

- 0004 is additive `CREATE INDEX ... ALGORITHM=INPLACE LOCK=NONE`; its down file contains no executable destructive DDL and requires a separately reviewed forward 0005.
- The migration runner checks 0004's checksum and fails closed unless its nonunique ordered column contract is exactly `(is_deleted, role, status)`.
- H3 places a constant index directive only in the count-local direct `users` query. It retains one generated predicate for both filtered and counted branches, duplicates only the generated bind list, and leaves paged unhinted. No user-controlled identifier or SQL fragment reaches the directive, sort clause, or binding order.
- The count and page remain inside the same read transaction and statement. API, DTO, authorization, hidden-target, and error contracts are unchanged by H3.
- No credential value, database URL, token, or Authorization value appears in this independent evidence directory.

Security findings: Critical 0, High 0, Medium 0, Low 0. Remaining non-security scope: true disk-cold measurement, frontend-to-backend acceptance, the 26 joint cases, other admin routes, production migration authorization, and exact cleanup.
