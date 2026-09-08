# A14 users.delete Task 18 independent specification re-review

Date: 2026-09-06

Reviewed backend candidate: `811213d557eea7b6b9523a584245252ba4dd7d80`

Reviewed frontend candidate: `bace6d167b94b693abd6be4c720152dc0eb905bb`

Review boundary: read-only inspection of code, tests, reports, private artifact
metadata and sanitized structure, listener health, Docker identity, and Git
history. No secret value was printed or retained. No fixture reset, test run,
service start/stop, cleanup, database mutation, production access, migration,
deployment, or push was performed. This report is the review's only repository
write.

## Verdict

The corrected evidence closes all three previous findings. F1's real
joint-browser matrix and F2's immutable sanitized database binding remain
valid. A complete read-only execution of every cleanup-manifest preflight
assertion before the first mutation now passes for the root-held application
processes and unchanged replacement containers. The specification review is
PASS; status update and cleanup remain separate later plan steps.

## Previous findings and closure

### F1 — required joint-browser rows used other evidence layers: CLOSED

The replacement Playwright artifacts now separate and cover the required
layers without importing the workflow or replacing the production adapter:

- `final-ui-successes.json` proves three visible production Vue/Pinia/adapter
  success flows against the real backend: Root to User, Root to Admin, and
  authorized Admin to User. Each records one Issue POST, one Execute POST, no
  Query GET, row removal after reload, autocomplete disabled, no sensitive
  persistence, and zero external requests.
- `ui-eligibility.json` proves visible real UI presentation for self, Root,
  equal Admin, and an authorized lower User. The protected targets have no
  action; the lower User does.
- `api-matrix.json` is Playwright API-context traffic against the exact real
  backend. It covers self/equal/higher/missing hidden 404, permission-denied
  403, version conflict, expired ticket, same-key same/different payload,
  terminal Query, duplicate delete, and five post-delete credential denials.
- `ui-recovery.json` uses the visible production dialog and adapter. The
  commit-unknown case performs a real backend commit, loses only the response
  at the browser boundary, then uses one Query and no POST replay. Processing,
  pending, 401 and network ambiguity are accurately labeled as controlled
  browser-network-boundary presentation tests; none is described as a real
  backend state transition.
- `sensitive-ui-lifecycle.json` uses the visible production dialog and actual
  Pinia/adapter for verifying, submitting, and querying lifecycle termination.
  Delay or response loss exists only at the browser boundary. Reload/close
  proves the native password element, reopened form, store, storage, URL, and
  console are clear.

The Playwright sources contain no import of
`admin-user-actions-state.js`/`createUserDeleteWorkflow`. Route handlers either
forward the original request to the backend or deliberately simulate the
named browser-boundary failure. The evidence layer descriptions therefore
match the actual test mechanisms. The API-context matrix and backend real
integration retain server-side policy, ticket, idempotency, transaction and
credential facts; controlled UI recovery rows are used only for client
presentation/replay behavior.

### F2 — UI-to-database binding was not preserved: CLOSED

The new mode-`0600` artifact
`/private/tmp/a14-user-delete-20260906031001-95b2aeb3-final-readback-sanitized.json`
has SHA-256
`949d1c9e57e0dcb342662af40702ba7543d38183da34fd633337fc5c749b381e`.
It contains exactly three aliases corresponding to the three final visible UI
successes. For every alias it records:

- one physical user row, `is_deleted=true`, disabled state, and cleared
  sensitive identity fields;
- one session and one token, each revoked;
- one policy head and override, each logically deleted;
- one management audit and one user-deleted authentication audit;
- one succeeded outbox and one succeeded terminal operation;
- normalized-reason equality as a boolean only.

The artifact contains only aliases, booleans, counts and irreversible target
hashes. It explicitly records no raw GUID, token, operation reference, reason,
credential, or secret. Structural inspection found no opaque ticket,
idempotency-key or operation-reference token, no `current_password` or raw
`reason` field, and no raw GUID field. The artifact's mtime is
2026-09-06T11:54:09+08:00, after the final UI successes at 11:52:56 and
credential rejection at 11:53:41, and before the corrected full/race gates at
12:16:58 and 12:18:59. The lifecycle report records that those later gates
reset only the disposable child. The immutable hashed artifact therefore
preserves the UI-to-database binding before reset, while the post-reset smoke
is correctly treated as a separate clean-start check.

### F3 — cleanup manifest application identities were stale: CLOSED

The replacement Docker fixture is valid and live. Read-only Docker inspection
matches:

- MySQL full ID
  `d1e96a2f780a4c604168c320e690f4685e467ace39edd9d701974903ebd807a3`,
  Redis full ID
  `365aa2d572c00f438db7c234273d82388b70d8de186d60b724174e7fa526596d`;
- the recorded immutable MySQL/Redis image IDs, exact container names, task
  and `task-18-rerun` retention labels;
- running/healthy, `AutoRemove=true`, read-only root filesystems, exact tmpfs
  paths, and loopback bindings `56060`/`56065`.

The application side now also matches. The full preflight block from
`cleanup-manifest.md` was executed read-only through its final HTTP assertion
and stopped before the first `kill` mutation. It passed every assertion:

- backend PID `10318` owns `127.0.0.1:57181`, and its exact command is the
  task-owned server binary;
- bridge PID `10331` owns `127.0.0.1:8000`, and its exact command is the
  recorded route-bridge script;
- frontend PID `10376` owns `127.0.0.1:55795`, and its exact command is Vite
  from the reviewed frontend worktree with the recorded strict port;
- backend `/health` and frontend `/` both return HTTP 200;
- both containers match full ID, exact name, image ID and repository digest,
  task/retention labels, running/healthy state, `AutoRemove=true`, read-only
  root, exact tmpfs values/counts, zero mounts, one loopback port binding, and
  the recorded host ports.

The preflight exited zero and printed only
`preflight=PASS checks=pid_command_listener_http_container_identity_isolation_health`.
It performed no mutation. Because every identity assertion occurs before the
first `kill`, `docker stop`, or exact file removal, the pending manifest is
fail-closed and executable as recorded.

The documents retain the full history: PIDs `4020`/`4045`/`4084` ended with
their agent lifetime, are labeled unexpected absence, and receive no cleanup
credit. Only root-held PIDs `10318`/`10331`/`10376` are current accepted
cleanup targets. The earlier host-restart loss remains separately recorded and
also earns no cleanup credit.

## Evidence and gate verification

All eight corrected public-facing private artifacts listed in
`joint-acceptance.md` exist with mode `0600`; every SHA-256 matches the report:

| Evidence | Verified result |
| --- | --- |
| Final UI successes | `d5e170e08be751ec6b46b249a543eae415c6e45093bbbd47331075a08eabb2a1`, 3/3 PASS |
| UI eligibility | `f4c6ef41bb7f5a4b89d5ac8eff80e28a961e64ce140567ea88a33b28593aeccd`, 4/4 PASS |
| Real API-context matrix | `5b58c45d49f489c2292a0de3815db948de7d4754ff7afc758e2ff1e862b16209`, required denial/version/ticket/key/query/credential rows PASS |
| UI recovery matrix | `8af7e35b96a0d61faec6b56cefdbfccea84e4c8232d3e002dbea9cdaad56f54d`, 5/5 PASS with exact layer labels |
| Sensitive UI lifecycle | `7b0cc34bb49e5f968ffe1288117b4d11f76bbe64d1f2ad7d6e62a12c536b105b`, 3/3 PASS |
| Credential rejection | `918699ce0cdf68cae98c411d44e383c01dad213ed545d7a87c07fb79662fe1f9`, 15/15 HTTP 401 |
| Sanitized DB binding | `949d1c9e57e0dcb342662af40702ba7543d38183da34fd633337fc5c749b381e`, 3/3 complete rows |
| Clean-start UI smoke | `c829d66717d858d95496673e9c146863676cef131dadbc70291b6817587da68a`, PASS with zero external requests |

Corrected backend full JSON hash
`c646df61fabb805e838b4f7ae3a780521677dbbbfe98fd52015e3fc84e879c65`
matches and recounts to 1395 leaf PASS, 0 FAIL, and the single explicit
`TestAdminUsersReadPerformance` SKIP. Corrected race JSON hash
`1ae1af7176625d0476fd1ca28cf8daf31eb5fff3e2ffacf6f35950cfb19de69e`
matches and recounts to 319 leaf PASS, 0 FAIL, 0 SKIP. The report preserves the
invalid harness attempts rather than converting them into product failures or
silently dropping them.

Frontend authoritative test hash
`51bfb9735a557279fd7d06ab97ac103c9b2129c2c3ecd8852f664b168405a0c2`
matches; its TAP summary is 201 PASS, 0 FAIL, 0 SKIP. Frontend build hash
`3a25685161819f9c50eb00b743a8475cbb98f5c5c267111fb035c87bbff7d04e`
matches and the build completed successfully. Both worktrees pass the current
read-only `git diff --check` inspection.

The frontend changes after `e6a92bc` are limited to the dialog, its contract
test, and the action store. Commits `96c0708` and `bace6d1` disable password
autofill and clear the model, native password input, and private coordinator
state on mount/open/close while preserving dialog ownership. The 201-test gate
includes the new regression assertions.

## Design-section mapping

| Approved design section | Current evidence | Result |
| --- | --- | --- |
| 1. Goal and conclusion boundary | Exact backend/frontend candidates recorded; no release or production claim; A12/A14 still unchanged. | PASS |
| 2. Architecture | Typed backend Issue/Execute/Query chain and dedicated frontend adapter/state/store/dialog remain as previously reviewed. | PASS |
| 3. Public HTTP contract | Frozen contract and runtime tests remain at backend `811213d`; frontend contract-backed gate passes at `bace6d1`. | PASS |
| 4. Permission, visibility and concurrency | Real API-context denial matrix, UI eligibility, real service policy/concurrency tests, and exact candidate code align. | PASS |
| 5. Delete transaction and data facts | Real service lifecycle/fault matrix plus the three-case sanitized UI DB binding prove atomic tombstone and credential/policy invalidation. | PASS |
| 6. Management audit and outbox | The three-case binding proves one management audit, auth audit, outbox and terminal operation per UI target without raw reason. Existing `0001..0005` remain unchanged; only `0006` is added. | PASS |
| 7. Frontend interaction and state machine | Visible real UI, production adapter/Pinia lifecycle, one-POST counts, reload/close clearing, accessibility/focus tests and 201-test gate pass. | PASS |
| 8. Error and recovery | Evidence accurately separates real commit/query from controlled browser-boundary processing/pending/401/network presentation; no POST replay is observed. | PASS |
| 9. Verification and acceptance | Corrected backend/full/race/build/vet/diff and frontend test/build/diff evidence pass; F1–F3 are independently closed. | PASS |
| 10. Implementation order | Tasks 1–17 and corrected Task 18 Steps 1–3 evidence are present. This specification review completes its part of Step 4; Steps 5–7 remain separately gated by all three reviews. | PASS |

## Scope-preservation audit

The frontend acceptance matrix and `progress.md` remain byte-identical to the
Task 1 frontend baseline. Backend `progress.md` remains byte-identical to its
plan baseline. No unrelated acceptance row, blocker, or count has changed, and
A12/A14 have not been prematurely marked passing.

Static implementation evidence remains unchanged: `ActiveActionRegistry()`
returns only `ActionUsersDelete=6`; the other seven descriptors and unrelated
routes remain inactive; no generic action dispatcher exists; legacy
`DELETE /admin/users/:guid` remains authenticated fixed 410 with no state
dependency; `AuthService.SoftDeleteUser` has no call site; `0001..0005`
up/down bytes match the design baseline.

The old fixture/process disappearance is explicitly recorded as a host-restart
loss and not misrepresented as cleanup. The first replacement application
holders are likewise recorded as unexpected absence without cleanup credit.
The root-held application processes and replacement containers are live and
retained. This PASS authorizes no mutation by itself: Step 5 status update,
Step 6 exact cleanup, and Step 7 final commits remain subject to the plan's
remaining independent reviews and coordinator sequencing.

PASS
