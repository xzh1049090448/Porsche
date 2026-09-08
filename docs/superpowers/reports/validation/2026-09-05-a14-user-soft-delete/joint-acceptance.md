# A14 users.delete isolated joint acceptance draft

Status: `ACCEPTED_LOCAL_USERS_DELETE_SLICE_ONLY`.

This draft records the corrected Task 18 Steps 1–3 evidence. It does not
change acceptance/progress status, alter any review verdict, authorize a
release, or execute cleanup. The outbox delivery/recovery worker remains
outside this slice.

## Exact candidates and live replacement fixture

- Backend: `811213d557eea7b6b9523a584245252ba4dd7d80`.
- Frontend: `bace6d167b94b693abd6be4c720152dc0eb905bb`.
- Backend app: root-held exec-session PID `10318`, started 2026-09-06
  12:34:28 +08:00, listener
  `127.0.0.1:57181`, current `/health` `200`.
- Route bridge: root-held exec-session PID `10331`, started 2026-09-06
  12:34:45 +08:00, listener
  `127.0.0.1:8000`. It forwards only production API paths to the exact backend;
  it does not implement an API or import the workflow.
- Frontend app: root-held exec-session PID `10376`, started 2026-09-06
  12:35:04 +08:00, listener
  `127.0.0.1:55795`, current root response `200`.
- Fixture suffix: `20260906031001-95b2aeb3`; MySQL full ID
  `d1e96a2f780a4c604168c320e690f4685e467ace39edd9d701974903ebd807a3`
  on `127.0.0.1:56060`; Redis full ID
  `365aa2d572c00f438db7c234273d82388b70d8de186d60b724174e7fa526596d`
  on `127.0.0.1:56065`.
- Both containers are healthy with exact names, pinned digest images,
  loopback-only bindings, `AutoRemove=true`, read-only roots, required tmpfs,
  `codex.task=a14-user-delete-20260906031001-95b2aeb3`, and
  `codex.retention=task-18-rerun`.
- Namespace `a14_user_delete_20260906031001-95b2aeb3`, disposable child
  `a14_user_delete_20260906031001-95b2aeb3_test`, Redis DB 8. The final
  re-review start is reset, migrated through `0006`, and bootstrapped with a
  new private local Root credential.

The host restart removed the previous suffix `20260905125101-20c53025`
AutoRemove containers, its `/tmp` artifacts, PIDs `90334`/`90407`, and
listeners `57178`/`55792`. This was unexpected absence, not Step 6 cleanup and
earns no cleanup credit. Docker Desktop was restarted only as a local fixture
prerequisite. No remote environment was contacted.

The first replacement application holders, PIDs `4020`/`4045`/`4084`, then
ended when their agent lifetime ended. Their second unexpected absence also
earns no Step 6 cleanup credit. The root-held exec sessions above are the only
accepted live application identities and cleanup targets.

## Corrected evidence matrix

| Matrix item | Result | Evidence layer |
| --- | --- | --- |
| Root to User, Root to Admin, authorized Admin to User | PASS 3/3 | Visible production Vue dialog, Pinia store, production adapter and exact real backend. Issue=1, Execute=1, Query=0 per success; reload kept the row absent; external request count 0. |
| UI eligibility: self, Root target, equal Admin, lower User | PASS 4/4 | Visible real UI. The first three had no delete action; the authorized lower User did. |
| Hidden/forbidden denial | PASS 5/5 | Playwright API context against the exact backend: self/equal/higher/missing were hidden 404; missing capability was 403. UI eligibility above separately proves the presentation layer. |
| Version conflict and expired ticket | PASS 2/2 | Playwright API context and real MySQL/Redis; 409 version conflict and expired verification rejection. |
| Idempotency/replay | PASS 4/4 | Real HTTP: same key/same payload returned the existing result without Execute replay; same key/different payload conflicted; terminal Query succeeded; duplicate delete was hidden. |
| Commit unknown, processing Retry-After, pending | PASS 3/3 | Visible UI through production adapter. Fault responses were injected only at the browser network boundary. Commit-unknown used a real backend commit then Query only; API-context terminal Query independently binds the backend layer. Processing queried twice after about one second; pending terminated without POST replay. |
| 401 and network ambiguity | PASS 2/2 | Visible UI/client evidence at the browser network boundary; neither replayed a POST. |
| Sensitive verifying/submitting/querying lifecycle | PASS 3/3 | Visible production dialog and actual Pinia. Delay/controlled response existed only at the browser network boundary. Reload/close left password/reason/unknown state absent from DOM, store, storage, URL and console; reopened password/reason were empty; native password input was cleared; autocomplete was off; external request count 0. |
| Post-delete credentials | PASS 15/15 | Playwright API context against the exact backend: Access, logical session, Refresh, Gateway token and login each returned 401 for all three deleted targets. |
| Database terminal binding | PASS 3/3 | Sanitized direct readback generated before reset: one retained deleted/disabled row; one revoked session/token; one deleted policy head/override; one management audit, user-deleted auth audit, succeeded outbox and succeeded terminal operation per alias. Normalized reason matched without retaining it. Artifact has only aliases, irreversible target hashes, counts/booleans and zero raw GUID/token/ref/reason/secret. |
| Clean re-review smoke | PASS | After the final reset/migrate/bootstrap, visible Chrome logged in and loaded `/users`; external request count 0. |

The recovery matrix carefully separates real backend facts from browser-boundary
fault presentation. No Playwright script imported the workflow or replaced the
production adapter/API. Zero screenshots and traces were created.

## Private evidence hashes

All listed artifacts are mode `0600`.

| Artifact | SHA-256 |
| --- | --- |
| Final three UI successes | `d5e170e08be751ec6b46b249a543eae415c6e45093bbbd47331075a08eabb2a1` |
| UI eligibility | `f4c6ef41bb7f5a4b89d5ac8eff80e28a961e64ce140567ea88a33b28593aeccd` |
| Real API-context matrix | `5b58c45d49f489c2292a0de3815db948de7d4754ff7afc758e2ff1e862b16209` |
| UI recovery matrix | `8af7e35b96a0d61faec6b56cefdbfccea84e4c8232d3e002dbea9cdaad56f54d` |
| Sensitive UI lifecycle | `7b0cc34bb49e5f968ffe1288117b4d11f76bbe64d1f2ad7d6e62a12c536b105b` |
| Final credential rejection | `918699ce0cdf68cae98c411d44e383c01dad213ed545d7a87c07fb79662fe1f9` |
| Final sanitized DB binding | `949d1c9e57e0dcb342662af40702ba7543d38183da34fd633337fc5c749b381e` |
| Re-review UI smoke | `c829d66717d858d95496673e9c146863676cef131dadbc70291b6817587da68a` |

## Corrected final gates

Each mutable backend gate began with exact identity checks, a reset of only the
child database and Redis DB 8, and migration `0001..0006`.

- Backend full: 1395 leaf PASS, 0 FAIL, one explicitly unauthorized 100k
  performance SKIP; SHA
  `c646df61fabb805e838b4f7ae3a780521677dbbbfe98fd52015e3fc84e879c65`.
- Backend race: 319 leaf PASS, 0 FAIL, 0 SKIP, no race diagnostic; SHA
  `1ae1af7176625d0476fd1ca28cf8daf31eb5fff3e2ffacf6f35950cfb19de69e`.
- Backend `go build ./...`, `go vet ./...`, and `git diff --check`: exit 0.
- Frontend authoritative contract-backed test: 201 PASS, 0 FAIL, 0 SKIP; SHA
  `51bfb9735a557279fd7d06ab97ac103c9b2129c2c3ecd8852f664b168405a0c2`.
- Frontend build succeeded; SHA
  `3a25685161819f9c50eb00b743a8475cbb98f5c5c267111fb035c87bbff7d04e`.
  Frontend `git diff --check` exited 0.

The older dirty-fixture race failure remains preserved as
`INVALID_SETUP_DIRTY_FIXTURE`. Three additional rerun setup errors are also
retained rather than overwritten: empty schema plus insufficient app-user DDL
authority (`backend-full-final`, SHA
`9c57b659f6e52bc9193bdb7e24c2dd5974197fce207c4706e39cb72f3f11642b`),
an exit-zero full invocation that failed to export Redis and therefore skipped
238 leaf tests (`backend-full-rerun`, SHA
`96582744490e266ac4793b824fbfd950018e698f111f12371bfee9d1ac14302f`),
and a frontend invocation using the wrong environment variable/path (SHA
`fa7a6dcae565450b5a42b4f3b87e9c711b8db0548c4998702c56e48fd566caca`).
These are invalid harness invocations and are not product RED.

## Final bounded acceptance

Specification, quality and security re-reviews independently returned `PASS`.
Only the A12/A14 `users.delete` slice is accepted locally at the exact commits
above. This does not accept A12 restore, the other seven inactive management
actions, a general outbox delivery/recovery worker, the full PRD, production
migration, deployment, push or release.

The frontend matrix changed only A12/A14 plus their derived summary and one
append-only timeline entry. Its pre-update SHA-256 was
`6925173b045e77362b8fc096d68727d602cd261545b24e8f38d60e991f93a241`;
semantic comparison proved all 24 unrelated rows and the preceding three
timeline entries unchanged. The two progress files only prepend this bounded
result to their tracked baselines. Post-update SHA-256 values are matrix
`82fa881c3cbee2c4db5aede492dad1fa413d0c2ef40b61383c297b09c93ca45e`,
frontend progress
`262bbc2ca17faca3e73c417fa5a00b1572e26742931034cc028c0ad6d6c63489`,
and backend progress
`b1229d259fdc3b06500b0e3e37bbb49a287e1718a8c40a59a05d925d7ce61b85`.

Task18 cleanup completed at `2026-09-06T12:59:42+08:00`. The complete
fail-closed 42-test preflight passed twice; the second pass immediately
preceded the three exact PID kills and two exact container stops. Final proof
found no accepted listener/PID, container ID/name, task-labelled
container/network/volume, enumerated temp path, namespace storage or Playwright
skill temp execution file. Details, including the preserved first mutation
extractor error, are in `cleanup-manifest.md`.
