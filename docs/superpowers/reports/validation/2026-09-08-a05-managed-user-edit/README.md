# A05 managed-user nickname edit — Task7 isolated acceptance

**Verdict: BLOCKED.** This is evidence of an attempted, disposable local acceptance fixture, not a release or production approval.

The original blocked run is superseded by a fresh r2 fixture: MySQL 8 and Redis 7 on different private loopback ports, a different `*_test` database, Redis namespace 12, a separately labeled bridge network, MySQL tmpfs, and Redis persistence disabled. Credentials existed only in a mode-0600 private temporary file and are not present in this repository or this report. Existing migrations `0001` through `0010` applied and their ledger checksums matched.

The strict decoder, handler, and router selection passed 71 leaves with zero skips or failures on the superseded source head. It exercised the frozen route, response headers/envelope, request size, forbidden fields, duplicate keys, case-folded keys, malformed JSON, and canonical path failures.

The r2 service matrix confirms that the earlier nullable-nickname panic and MySQL 1064 are gone; all rollback and concurrency leaves pass. It still does not pass the required 22 fixture leaves. `TestAdminUserNicknameEditAuthorizationAndSetClear` fails at `internal/service/admin_user_edit_test.go:90` for Root-to-User, Root-to-Admin, and Admin-to-User, while the no-op assertion fails at line 115. The package has zero skips but a failing terminal result.

No r2 backend runtime server, HTTP mutation session, visible-browser scenario, DB/audit conclusion, or end-to-end privacy claim was recorded after this blocker. Handler/router reruns were not started after the service dependency failed. The fixture resources are removed exactly after this evidence is written; no production database, deployment, tracker, or unrelated Docker resource was touched.
