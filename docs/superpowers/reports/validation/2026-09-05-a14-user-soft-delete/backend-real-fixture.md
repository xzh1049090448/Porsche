# A14 user-delete backend real-fixture validation

Verdict: `PASS` for the isolated Task 12 backend lifecycle scope. The exact
healthy MySQL/Redis fixture is `RETAINED_FOR_TASK_18`; this is not production,
deployment, or full cross-team acceptance evidence.

## Scope and fixture

The run used only the suffix `20260905125101-20c53025` fixture documented in
`fixture-lifecycle.md`. Migrations `0001` through `0006`, the exact schema
verifiers, and dependency-safe `0006/0005` down/up restoration ran against the
dedicated `_test` child database. The required namespace database remained in
the same isolated MySQL tmpfs. No production or remote database/Redis was read.

## TDD evidence

The first valid A14 real test run produced two behavior RED failures:

- committed deletion could not be resolved through `Query` after the target
  became a tombstone;
- same-key, same-payload, same-ticket terminal `Begin` replay could not return
  the stored result after deletion.

Both failures had the same cause: the operation read paths reapplied the
pre-action active-target authorization predicate after terminal commit. The
minimal implementation correction retains all actor, logical-session,
idempotency/request-HMAC, action, verification, ticket, state, and retention
checks, but reapplies target authorization only while the operation is
`processing`. Terminal reads return no execution capability. The real tests
then passed.

A subsequent diagnostic gate exposed stale B1-E real tests that still injected
the historical fictional `test.noop` descriptor. Production correctly rejects
that descriptor now that `users.delete` is the sole active action. Only the
real MySQL/Redis test fixtures were switched to the exact active descriptor;
scripted tests keep the fictional action and continue proving that unreviewed
future actions fail closed. Repeated-run failures from deterministic ticket
digests and accumulated rate keys were isolated as test-data reuse; the final
gate ran once after precisely recreating the task-owned `_test` database and
selecting an empty Redis logical database.

## Covered behavior

Real tests cover Root to User, Root to Admin, explicitly authorized Admin to
User, self/Root/equal/higher/deny/hidden targets, Issue-time state/version and
overflow conflicts, Execute-time state/version drift, exact ticket expiry,
same-key same-payload replay, different-payload conflict, cross-session
conflict, refresh within the same logical SID, duplicate delete, and permanent
username reservation.

A successful committed deletion proves the user row remains, the username and
non-sensitive business fields remain, sensitive identity/authentication fields
are cleared, status becomes disabled, `is_deleted=1`, and `auth_version`
increments. Every active and expired undeleted/unrevoked session is revoked and
version-bumped. The real Access/session validation, Refresh call, logical
session listing, and Gateway credential authentication all reject afterward.
Target policy rows are tombstoned. Exactly one user-deletion auth audit, one
management audit, and one outbox row are bound to the operation. Re-registering
the tombstoned username conflicts.

The real fault matrix injects one failure at each session, gateway-token,
policy-head, policy-override, user, auth-audit, management-audit, outbox, and
terminal-operation write. Each case directly reloads the seeded active Gateway
token and unchanged policy head and override in addition to the target,
sessions, audits, outbox, operation, and verification. Every case proves zero
partial delete facts, no consumed verification, and a still-processing
operation.
Commit-return uncertainty returns the sanitized `CommitUnknownError` with the
operation public reference, does not replay Execute, and resolves the committed
success only through `Query`. Existing handler tests in the same focused gate
prove the corresponding fixed 503 response carries the operation reference.

The successful path directly reloads and requires exactly one row for the auth
audit, management audit, outbox, operation, and consumed verification. It
asserts the management audit contains the trimmed canonical reason, while the
auth-audit schema has no reason column and the outbox schema has neither reason
nor payload columns. It also proves the exact action, actor, actor auth version,
logical session, verification, idempotency HMAC, request/intent HMAC, ticket
HMAC, target, succeeded state, result kind/GUID/status, lease clearance, and
query-retention bindings. Assertion failures expose none of the payload,
reason, ticket, refresh material, gateway secret, or stored credential hashes.

The prior test commit was a review RED: it did not directly assert the token
and policy rollback facts or the complete terminal persistence bindings, so it
failed the Task 12 specification even though its bounded runtime gate passed.
The added assertions required no production change and passed against the
retained real fixture. This report treats that missing evidence as a structural
SPEC failure rather than inventing a runtime product failure.

## Final gates

- Serial real fixture command from the plan, after the documented reset: 251
  terminal test PASS events, 228 leaf PASS, 0 FAIL, 0 SKIP; 3 package PASS.
  Private JSON SHA-256:
  `7bb3f7b69b96dc37344f944dd42a3d1fafb3b8630980c9e942620e737a0b47de`.
- Race command from the plan: 1 terminal/leaf test PASS, 0 FAIL, 0 SKIP; 2
  package PASS. Private JSON SHA-256:
  `3a469d29f1ec49d4fe21668760a88d2b8c399e715707e12e2b176339acb4394c`.
- Fresh no-fixture `go test ./...`, `go build ./...`, `go vet ./...`,
  `git diff --check`, and the repository `./init.sh` gate all passed.
- Private env mode was `0600`; deliberately wrong MySQL and Redis credentials
  were rejected. No credential, connection URL, ticket, password, SID, refresh
  material, or gateway secret is present in this report.

The reset flow and exact reviewer commands are in `fixture-lifecycle.md`. The
final reset plus an immediate second reset restored and confirmed the empty,
independently repeatable starting point without changing either fixture
identity or the namespace database.

The exact two containers remain healthy and labeled for Task 18. Cleanup
commands are recorded in `fixture-lifecycle.md` and were not executed.
