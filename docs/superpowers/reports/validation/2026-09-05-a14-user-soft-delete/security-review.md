# A14 users.delete Task 18 independent security rereview

Date: 2026-09-06

Reviewed backend candidate: `811213d557eea7b6b9523a584245252ba4dd7d80`

Reviewed frontend candidate: `bace6d167b94b693abd6be4c720152dc0eb905bb`

Review boundary: read-only inspection of the approved design and plan,
production code, tests, corrected Task 18 reports, private-artifact structure,
hashes and permissions, and live listener/container state. No fixture reset,
test execution, service start/stop, cleanup, commit, deployment, migration,
push or production access occurred. No secret value was printed. This report
is the only repository write.

## Verdict

S1 through S5 from the first security review are closed. The corrected code,
preserved evidence, live application identities, container identities and
fail-closed cleanup manifest satisfy the approved Task 18 security boundary.
This PASS authorizes the coordinator to continue the plan's remaining review,
bounded status-update and exact-cleanup steps; it does not authorize production
deployment, production migration, push or another management action.

## Original finding closure

### S1 (P1) — destructive-action password autofill: CLOSED

Frontend commits `96c0708` and `bace6d1` change the password field to
`autocomplete="off"`, keep it as a non-revealing password input, and add an
owned mount-time clear. `clearUserDeleteConfirmationForm` clears the Vue model,
the Element Plus native input, the coordinator's private reason/password and
form validation. `scheduleUserDeleteMountClear` performs the clear only while
the same dialog ownership token remains current.

The updated source-contract tests cover open, close, remount and an already-open
store remount. They assert the native input, model and private store value are
empty without replacing the active ownership token. The real lifecycle
artifact independently records `autocomplete=off`, native input clearing, empty
reopened password/reason and safe reopened Pinia state for three sensitive
states. The frozen design's no-autofill requirement is now met.

### S2 (P1) — lifecycle evidence bypassed real UI/store/transport: CLOSED

The replacement mode-`0600` helper
`/private/tmp/playwright-test-a14-real-sensitive-lifecycle.js` drives the visible
production dialog, actual Pinia store and production action adapter. It does
not import `createUserDeleteWorkflow`, install an independent workflow on
`globalThis`, or replace the production API. It controls delay/failure only at
the loopback browser-network boundary.

The preserved mode-`0600` artifact proves:

- verifying followed by reload;
- submitting followed by context close;
- querying after a real backend commit followed by reload;
- native password input cleared before termination;
- reopened model inputs empty and Pinia state idle with no operation ref;
- storage, URL, visible DOM and console free of password/reason markers;
- zero unexpected external requests.

This is now real UI lifecycle evidence rather than a direct state-machine
substitute.

### S3 (P1) — joint security rows and UI/database binding: CLOSED

The corrected evidence keeps its layers explicit and closes the former gaps:

- visible UI eligibility covers self, Root target and equal Admin with no
  delete control, plus an authorized lower User with the control;
- Playwright API context against the exact backend covers self/equal/higher/
  missing target as hidden 404 and missing capability as fixed 403;
- real HTTP covers version conflict, expiry, same-key same/different payload,
  terminal Query and duplicate delete without Execute replay;
- the visible production UI and adapter cover commit unknown, processing
  Retry-After, pending recovery, 401 and network ambiguity. Only presentation
  faults are injected at the browser boundary. The commit-unknown case performs
  one real backend commit and then only Query; no POST is replayed;
- Playwright API context proves old Access, logical session, Refresh, Gateway
  token and login are each rejected with 401 for all three deleted targets,
  giving 15/15 post-delete credential rejections;
- the preserved sanitized database artifact binds all three visible UI
  successes to one physical disabled tombstone row, one revoked session/token,
  one deleted policy head/override, one management audit, one reason-free user
  deletion auth audit, one succeeded outbox and one succeeded terminal
  operation per alias. It stores aliases, irreversible target hashes,
  counts/booleans, and no raw GUID, token, public ref, reason or secret.

All eight named corrected artifacts exist at mode `0600` and match the hashes
recorded in `joint-acceptance.md`. Their safe counters reproduce 3/3 UI
successes, 4/4 UI eligibility cases, 16 real API cases, five UI recovery cases,
three sensitive lifecycle cases, 15 credential rejections and 3/3 database
bindings. Service, API-context, browser-boundary fault and real-UI claims are
not conflated.

### S4 (P1) — stale application cleanup identity: CLOSED

The corrected reports honestly state that the prior suffix, containers,
applications and private files disappeared during a host restart and give that
event no Step 6 cleanup credit. They then record replacement application
identities:

- backend PID `4020` on `127.0.0.1:57181`;
- route bridge PID `4045` on `127.0.0.1:8000`;
- frontend PID `4084` on `127.0.0.1:55795`.

The first replacement holders later ended with their agent lifetime; the
corrected reports explicitly record that second unexpected absence and give it
no Step 6 cleanup credit. Root-held replacements are now the only accepted
application identities:

- backend PID `10318` on `127.0.0.1:57181`;
- route bridge PID `10331` on `127.0.0.1:8000`;
- frontend PID `10376` on `127.0.0.1:55795`.

Independent sandbox-external read-only checks matched each exact PID, listener
and full command line. Backend `/health` and frontend root both returned 200.
This corrects the earlier sandbox-local false negative, where `lsof` could see
the listeners but process and loopback access were denied.

The replacement MySQL and Redis containers remain running and healthy. Exact
Docker and image inspection matches both full IDs, names, pinned image IDs and
RepoDigests, task/retention labels, loopback-only ports `56060`/`56065`,
`AutoRemove=true`, read-only root filesystems, exact tmpfs entries/counts, zero
mounts and one exposed port each.

The manifest completes every PID/command/listener/health and container/image/
label/isolation check before its first `kill`, `docker stop` or `rm`. `set -eu`
makes any mismatch fail closed. It uses exact names and paths, forbids wildcards,
broad kills, prune, volume removal and unrelated database operations, and
records both earlier absences honestly without cleanup credit. Its exact file
inventory matches all 60 retained task files: 45 suffix-owned plus 15
Playwright/bridge helpers, with zero missing and zero unlisted files.

### S5 (P2) — world-readable sensitive browser helpers: CLOSED

All 15 current `playwright-test-a14-*` helpers are mode `0600`; none is mode
`0644`. Private environment, user and credential inputs and all corrected
results are also mode `0600`; executable fixture/reset/seed helpers are mode
`0700`. The server executable alone is mode `0755` and contains none of the
runtime private values checked below.

The cleanup manifest enumerates all 60 current task-suffix and Playwright helper
files exactly; the inventory found zero unlisted task temp files. It requires
zero residuals after Step 6. No cleanup was run during this review, so the files
remain intentionally retained for review.

## Revalidated backend security properties

### Ticket, key, request and logical-session binding — PASS

- Separate HKDF/HMAC domains exist for ticket, intent, idempotency and lease;
  digest comparison is constant-time.
- Issue persists only ticket/intent HMACs and binds actor, auth version, logical
  session, action, target and expiry. Owned password bytes are cleared by the
  handler and are never persisted.
- Begin converts raw ticket/key values to HMACs, clears the raw buffers, binds
  the request HMAC to the verification, and permanently keys replay identity
  by actor/action/key HMAC. Changed payload and cross-session reuse fail closed.
- Existing operations never receive a fresh execution capability. Execute
  consumes a one-shot in-memory lease and re-locks actor, logical session,
  operation, verification, target and current policy before callback writes.
- Query requires the original key HMAC, exact action, current actor and original
  logical session. Ticket, target and public ref are not query authority, Query
  does not invoke the consumer, and it does not consume Begin limiting.
- Commit acknowledgement uncertainty exposes only a safe operation ref; the
  one-shot capability prevents POST replay and resolution uses the original
  session/key Query.

### Actor, target visibility and authorization — PASS

Persistent actor/session state and current policy are re-read under the defined
lock order. Admin-to-User and Root-to-User/Admin success, self/Root/equal/
higher/deny/hidden, missing/deleted target, disabled actor/session and version/
state drift are covered by unit, real fixture and corrected API/UI evidence.
Missing, deleted and hierarchy-hidden targets converge on 404; action permission
or credential rejection uses fixed 403. The frontend check controls display
only and is narrower than the authoritative backend rule.

### Credential invalidation and atomicity — PASS

The consumer uses only the supplied MySQL transaction. It revokes and version-
bumps every undeleted/unrevoked session, revokes valid Gateway tokens,
tombstones policy rows, clears authentication/identity fields, disables and
soft-deletes the user, increments `auth_version`, preserves the physical row,
GUID, username and history, and writes a reason-free authentication audit.
Management audit, outbox and terminal operation writes share that transaction.
The real fault matrix proves rollback at every write boundary; corrected joint
evidence now also proves the committed browser targets and post-delete
credential rejection.

### Reason and public-data boundary — PASS

The normalized reason appears only in the allowlisted management audit detail.
It is absent from the tombstone, auth audit, operation, outbox, public errors,
ordinary logs and corrected evidence. Password, ticket, idempotency value,
reason and unknown workflow state are not stored in URL, local/session storage,
analytics, screenshots or traces. Production DTO/string/JSON formatting
redacts password-bearing requests and omits reason from execution formatting.

### Route inventory and fail-closed activation — PASS

`ActiveActionRegistry()` returns only `ActionUsersDelete=6`; all other seven
descriptors remain inactive. Only explicit Issue, user-specific Execute and
scoped Query routes are registered behind the complete typed bundle. There is
no generic dispatcher. Legacy authenticated `DELETE /admin/users/:guid` is a
state-free fixed 410 with no target/business dependency, and
`AuthService.SoftDeleteUser` has no call site. Legacy GET/PUT remain separate.

### Strict HTTP, errors, Retry-After and no replay — PASS

The DTOs enforce the 4 KiB raw limit, a single exact object, case-sensitive
fields, duplicate/unknown/trailing/type/UTF-8 rejection, canonical positive
int64 GUID, integer version bounds and a trimmed 1–200-code-point reason.
Headers reject forbidden, missing, duplicate, combined and malformed ticket/key
values. Query accepts only exact `scope=users.delete`. Responses are `no-store`
with request IDs. Retry-After is confined to rate limiting and processing Query;
POST never returns a processing success and never auto-replays. Only
`operation_commit_unknown` may include an operation public ref.

## Frontend production security — PASS

Issue and Execute use a dedicated Axios transport without response/retry
interceptors; the exact Query GET may refresh once. The workflow generates a
cryptographically random key, prevents duplicate runs, holds sensitive values
in closures, clears password after Issue and ticket after Execute, bounds
polling, and clears all private state on terminal completion/unmount. Pinia
exposes only safe status, operation ref and fixed failure code. A14 production
source has no storage, analytics or console sink. The autofill and native/model/
store mount-clearing corrections close the remaining production UI gap.

## Secret and evidence scan

- A fresh exact-value scan collected 78 unique private values associated with
  credential, password, token, cookie, key/HMAC, URL, GUID/ref, SID and reason
  fields from the retained private fixture/user inputs. It found zero matches
  in either repository's A14 reports and zero matches in all eight sanitized
  corrected artifacts.
- All five artifact-level `contains_*` confidentiality assertions are false.
  The sanitized readback has only aliases, target hashes, counts and booleans;
  no actual secret value was printed or copied into this review.
- The 15 browser helpers contain zero `createUserDeleteWorkflow`, `__a14wf` or
  `globalThis.__a14` references. They write only mode-`0600` results. Their
  console output is limited to fixed summaries/errors and the preserved
  artifacts contain no raw sensitive request material.
- There are zero A14 screenshots and zero trace archives for the replacement
  suffix. The reports do not present browser-runtime doubles as real UI or
  service proof.
- Backend final full evidence records 1395 leaf PASS, 0 FAIL and one explicit
  unauthorized performance SKIP; race records 319 PASS, 0 FAIL and no race
  diagnostic. Frontend corrected evidence records 201 PASS, 0 FAIL, 0 SKIP and
  a successful build. Earlier invalid harness invocations remain preserved and
  are not rewritten as product passes or failures.

## Acceptance decision

S1 through S5 are closed. The current security review places no remaining block
on the plan's independent-review completion, bounded A12/A14 status update or
execution of the exact fail-closed cleanup manifest. Cleanup must still record
postconditions proving the three application identities, both containers,
namespace resources, task files and task-labelled Docker resources are absent.

PASS
