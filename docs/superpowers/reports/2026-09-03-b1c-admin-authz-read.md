# B1-C admin authorization read APIs — PASS_LIMITED_SCOPE

Final status: `PASS_LIMITED_SCOPE`; `go-015` is `passing`. PM final `SPEC PASS`
and independent real-fixture `VERDICT: PASS` cover the two read-only APIs and
local integration readiness. Both exact task containers and all batch-private
credentials have been cleaned up and their absence verified.

## Delivered boundary

- `GET /admin/v2/authz/catalog`: exact active, non-deleted Admin or Root; stable
  ordered 24-capability catalog, version1 and inherit/allow/deny effects.
- `GET /admin/v2/users/{guid}/permissions`: exact Root viewing a non-deleted Admin,
  including disabled targets. Disabled targets have a dedicated display projection,
  never an active authorization evaluator. Version0 baseline and positive empty
  policy-head versions are preserved.
- Both registered routes set no-store and gateway request ID before authentication.
  Matched-route GUID/query checks are strict; unmatched paths retain Gin semantics.
  Error bodies contain only detail, with request ID in the header.
- Root-pool READ COMMITTED transaction owns actor user → optional target user →
  actor session SHARE locks, validates current AuthVersion/session ownership,
  version/revocation/expiry and Redis barrier, and returns a DTO only after commit.
  Authentication loss401 precedes role403/target404. Hidden targets stay uniform404
  even with bad target status/AuthVersion; otherwise-visible corruption is503.
- Shared private policy reading preserves the existing snapshot loader's strict
  orphan/head/row checks and active-only evaluator behavior. No permission writer,
  ticket, outbox, new schema/migration, legacy write enablement or FE UI is added.

## Actual writer verification

The user authorized this B1-C disposable lifecycle with “就行” and “继续”: task
containers/database, existing0001–0003, test-owned child schemas and exact cleanup.
Exact labels/names showed no prior partial startup. Migration ran from a private
no-.env directory using a compiled migration binary and explicit database settings.
Actual post-migration ledger and checksums are archived in
`validation/2026-09-03-b1c-admin-authz-read/real-fixture/migration-ledger.txt`.

Commands run against this explicit MySQL8.0.46/Redis7.4.11 fixture:

```sh
go test -p 1 ./internal/service ./internal/handler -run '^(TestAdminPermission.*|TestAdminAuthz.*)$' -count=1 -json
go test -race -p 1 ./internal/service ./internal/handler ./internal/authz ./internal/middleware -run '^(TestAdminPermission.*|TestAdminAuthz.*|TestPermission.*)$' -count=1 -json
go test -p 1 ./... -count=1 -json
go build ./...
go vet ./...
git diff --check
```

Observed: focused83 PASS; race107 PASS; fresh full627 test PASS, 0 FAIL, 0 SKIP,
15 package PASS and4 no-test packages. Build/vet/diff exit0. Real SQL lock order,
waited expiry, commit failure/noDTO, session/Redis failure matrices, corrupt policy,
partial head/rule writer serialization, disabled target and authenticated HTTP
contracts all executed. No production code, assertion or migration change was
needed. All eight production hashes remained frozen.

## Independent final verification

The independent reviewer re-hashed all eight production files before and after
real execution and ran:

```sh
go test -json -p 1 ./internal/service ./internal/handler -run '^(TestAdminPermission.*|TestAdminAuthz.*|TestPermission.*)$' -count=1
go test -race -json -p 1 ./internal/service ./internal/handler -run '^(TestAdminPermission.*|TestAdminAuthz.*|TestPermission.*)$' -count=1
go test -overlay=<private-overlay.json> -json -p 1 ./internal/handler -run '^TestIndependentAdminAuthzHTTPBoundaryProbe$' -count=1
```

Observed: focused96 leaf PASS (service72/handler24), race96 leaf PASS, independent
real authenticated HTTP boundary probe1 PASS; all0 FAIL/0 SKIP. The probe rejects
query parameters and noncanonical signed/leading-zero/overflow GUIDs, checks
minimal fixed errors, request ID/no-store and a valid detail read afterward.
CRITICAL/HIGH/MEDIUM/LOW findings: all0. Final independent `VERDICT: PASS`; PM
independently recounted writer evidence and hashes and gave final limited SPEC PASS.
Full report/logs/probe are archived in `real-fixture/independent-real/`.

## Cleanup verified

Before stopping, exact IDs, names, task label, image IDs and AutoRemove were
rechecked for MySQL `133f7729e68712032b691f154657c8c504f52dc6030bf40bd2dcf96443cf6816`
and Redis `f3c304aaec4be4bdf30a8f4adab914b416be679db13c0957705d93ee9c2d7f0a`.
Both were stopped and auto-removed; subsequent exact-ID/name queries were empty.
Only this batch's owned private files/directory were deleted. `fixture.env`, random
credentials and private directory absence were verified. No volumes, cache, other
fixtures or production resources were touched. `real-fixture/cleanup.json` records
these checks and cleaned identities without credentials.

## Historical evidence and reproduction

The earlier no-fixture run (356 PASS/229 fixture SKIP) and static-only review
(18 leaf PASS/34 fixture SKIP, PARTIAL) are historical, superseded for this local
slice by the real runs above. Their original redacted artifacts remain preserved.

Initial real full failed because archived overlay `.go` files were compiled as a
new package and migration APP_ENV=test leaked into two config tests. Root causes
were corrected by preserving probe bytes as `.go.txt` and scoping migration env
to its process. Config regression and package scanning then passed; corrected
fresh full627/0/0 is the final result. The failed-run JSON remains historical.

Archived `.go.txt` sources are deliberately outside Go scanning. Reproduce a probe
by copying it to a private temporary `.go` file, regenerating the overlay mapping,
and supplying a newly authorized disposable fixture. Original overlay paths are
historical provenance, not reusable fixture credentials. See the reproduction
notes beside each archived probe. JSON action/count evidence is preserved while
arbitrary diagnostics are omitted from writer public logs to avoid SQL/session or
credential leakage. The manifest records artifact and production hashes.

This limited PASS does not complete broader B1, FE wiring, any permission write
path, production deployment or the26 joint PRD acceptance cases. FE overall
contract stays DRAFT; only the two GET entries are AGREED_FOR_IMPLEMENTATION,
other25 entries/root contract/web-012 and the26 NOT_RUN joint cases are unchanged.

VERDICT: PASS
