# A05 managed-user nickname edit — Task7 isolated acceptance

**Verdict: BLOCKED.** This is evidence of an attempted, disposable local acceptance fixture, not a release or production approval.

The fixture was MySQL 8 and Redis 7 on private loopback ports, with a dedicated `*_test` database, Redis namespace 11, a labeled bridge network, MySQL tmpfs, and Redis persistence disabled. Credentials existed only in a mode-0600 private temporary file and are not present in this repository or this report. Existing migrations `0001` through `0010` applied and their ledger checksums matched.

The strict decoder, handler, and router selection passed 71 leaves with zero skips or failures. It exercised the frozen route, response headers/envelope, request size, forbidden fields, duplicate keys, case-folded keys, malformed JSON, and canonical path failures.

The required real MySQL/Redis service matrix did not pass. `TestAdminUserNicknameEditRollsBackUpdateAuditAndCommitFailures` panics at `internal/service/admin_user_edit_failures_test.go:92` because it dereferences the nullable fixture nickname. Excluding that leaf exposed a separate MySQL 8 defect: `internal/service/admin_user_edit.go:239` sends an unbound `?` in the `user_permission_heads` locking query, returning MySQL 1064 and turning expected success, 403, and 409 cases into 503. The 22-leaf service acceptance requirement is therefore unmet.

No backend runtime server, HTTP mutation session, visible-browser scenario, DB/audit conclusion, or end-to-end privacy claim was recorded after this blocker. The fixture resources are removed exactly after this evidence is written; no production database, deployment, tracker, or unrelated Docker resource was touched.
