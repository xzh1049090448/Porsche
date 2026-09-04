# B1-B2 Managed User Security Validation

## Scope

Local-only B1-B2 tightens the existing `PUT /admin/users/:guid` flow and `UpdateManagedUser`: strict bounded JSON parsing, semantic no-op handling, session invalidation and AuthVersion transitions for actual status/plan/ACL changes, event 10 audit, and actor-attributed session-revocation audit. It adds no route, DTO, permission-override writer, ticket, idempotency key, outbox, schema migration, frontend integration, production deployment, commit, or push.

## TDD evidence

The first real service behavior RED used a disposable MySQL/Redis fixture: changing a target plan returned successfully while its existing session remained live and `AuthVersion` was 1 rather than 2. A later event-10 assertion was also a behavior RED (no managed-user audit row). The resulting focused service matrix is GREEN.

The strict handler suite initially appeared to pass only because its TEST variables had not been exported and the fixture tests skipped; that result is not evidence. With `set -a` fixture export, the actual route RED showed duplicate `status` JSON writing the final disabled value and malformed/unknown/trailing input accepted. The test worker then verified the final actual-route matrix against the fixture with no skip.

## Verified local evidence

- Focused `TestUpdateManagedUser*` real fixture matrix passed: plan/ACL/status/mixed transitions, daily-limit-only event, semantic ACL no-op, zero-session audit, typed invalid input, Redis failure, AuthVersion overflow, SQL/update/audit/commit failures, and same-value concurrency.
- A genuine `AuthService.LoginUsername` post-transition check passed: old Access/session and Refresh were rejected; current username/password created a new session and non-empty Access JWT that validated.
- Role/AuthVersion matrix passed for ordinary, disabled, self, equal role, Root target, unknown actor/target role, actor/target version zero rejection and Root-to-Admin success. The self case asserts rejection; it does not claim every target-side field or Redis state was separately observed.
- The earlier writer-run `GOCACHE=/private/tmp/porsche-go-build-cache go test -p 1 ./... -count=1` established only 15 passing packages and 4 no-test-file packages. It did not emit JSON and is not the source of test-event counts. The authoritative 512/0/0 counts are from the independent JSON run recorded below.

## Contract limits and follow-up debt

`[]` remains the existing unrestricted user-model ACL meaning; daily limit 0 retains its existing quota calculation. This slice does not complete old role-authorization debt, fine-grained permission writes, tickets, idempotency, outbox, Key ACL repair, or front-end management. Redis denial barriers are fail-closed but not part of a cross-store atomic commit. If MySQL commit returns an error, the API returns nil and fixed 503 but outcome may be unknown; callers must not safely replay without a future idempotency/operation-query contract.

## Review state

2026-09-03: backend PM returned **SPEC PASS** for this limited scope. Independent `permission_snapshot_verify` returned **VERDICT PASS**, with no Critical, High, Medium, or Low finding. Its authoritative command was `GOCACHE=/private/tmp/porsche-go-build-cache go test -json -p 1 ./... -count=1`, archived at `validation/2026-09-03-b1b2-managed-user-security/full-test.json`; it recorded 512 passing test events, 0 failures, 0 skips, 15 passing packages and 4 no-test-file packages. Focused fixture-backed race passed for models (1.466s), service (13.693s), and handler (3.235s); build, vet, diff and feature JSON checks passed. Its independent HTTP adversarial probe passed in 1.515s. `go-013` is passing only for this local limited scope; it does not close the 26-case FE/BE acceptance matrix or authorize production.

The final implementation/test SHA-256 inventory is in `validation/2026-09-03-b1b2-managed-user-security/validation.json`. The two task-owned MySQL/Redis fixture containers were verified disposable, tmpfs-backed and AutoRemove before exact-ID cleanup; the task label has no remaining container and its private environment file has been removed.
