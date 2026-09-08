# A05 managed-user nickname edit — Task7 r4 isolated acceptance

**Verdict: BLOCKED.** r4 supersedes r1–r3 as the latest isolated acceptance attempt. It is not a release or production approval.

r4 used new task-labeled loopback-only MySQL 8.0 and Redis 7.4 containers, a private bridge network, MySQL tmpfs, Redis persistence disabled, a distinct `*_test` database, Redis namespace 14, and mode-0600 private credential files. Migration ledger checksums `0001` through `0010` matched.

The real A05 service matrix passed all 33 actual leaves with zero failures and zero skips. This includes the repaired `nil` and `non_nil` no-op paths, Root-to-User, Root-to-Admin, Admin-to-User, rollback, concurrency, stale identity/session, audit privacy, and strict input tests. Neither the former MySQL 1064 nor nullable-nickname panic recurred. The DTO/handler/router matrix added 71 passing records with zero failures or skips, and the A05 race plus related `UpdateManagedUser` regression passed without a race report.

The runtime phase is blocked before a backend listener or browser can start. The same parent r4 test database was used by the related regression after the service gate. A subsequent one-shot Root bootstrap returned `Root bootstrap did not leave exactly one Root user`. Its aggregate-only diagnosis found 22 active synthetic User rows, 22 active synthetic Admin rows, two active Root rows, and two invalid-role test rows. This violates the isolated one-Root runtime precondition. No usernames, nicknames, credentials, tokens, request bodies, or internal IDs were read into this report.

Accordingly, no r4 HTTP mutation, frontend dev server, visible Playwright scenario, console/network claim, runtime DB/audit conclusion, adversarial-browser check, or end-to-end privacy claim is made. The next run must use a freshly migrated runtime database that has not been touched by test fixtures, then repeat the omitted dependent gates. This evidence does not update any tracker, deploy, or production resource.
