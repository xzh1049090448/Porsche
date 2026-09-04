# B1-B3 Gateway Owner ACL Validation

## Scope

This local-only slice adds owner ACL awareness to Gateway Key authentication and
`/v1` model access. It does not add a migration, schema, Key CRUD change,
ticket, idempotency, outbox, frontend integration, deployment, commit or push.

## Evidence to date

The first real owner-freshness test stopped before its behavior assertion when
the new disposable MySQL 8 database had no schema. The user subsequently gave
an explicit one-time authorization to apply existing embedded migrations
`0001`–`0003` only to `porsche_b1b3_17884390752a356ab0_test`; that migration
completed successfully. The private fixture env path remains unrecorded by
value; the task-owned containers were later cleaned after final review.

A separate pure behavior RED established that a nil `GatewayTokenService`
panicked. It is now fail-closed as `gateway_authentication_unavailable`.
Pure tests also cover SQL-null/whole-null/empty raw ACL semantics, rejected raw
elements, zero-principal denial, and defensive ACL copies.

## Current validation

- PASS: `env -u TEST_DATABASE_URL -u TEST_REDIS_URL -u RUN_START_COMMAND GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service -run '^(TestGatewayTokenAuthenticateNilServiceFailsClosed|TestGatewayAllowedModelsRawValidationAndPrincipalCopies)$' -count=1 -v`
- PASS: cleared-environment `go build ./...`, `go vet ./...`, and `git diff --check`.
- PASS: real MySQL owner-freshness, token/owner persisted raw ACL matrix,
  same-millisecond last-used compatibility, soft-deleted owner, and existing
  Gateway HTTP suite. Service read/write failure coverage uses GORM callback
  injection on the test connection; it is not a simulated MySQL server fault.
- Writer full JSON run: 531 test pass, 0 fail, 0 skip; 15 packages pass and 4
  no-test packages skip. This is early writer evidence, not final independent
  validation.
- Historical only: before handler completion, writer evidence listed cross-route
  assertions and independent counts as pending. Those checks are included in
  the final independent PASS evidence below.

`go-014` is passing for this limited local scope; its fixture has been cleaned.

## Historical independent partial review

`permission_snapshot_verify` returned **VERDICT: PARTIAL** for this frozen
stage: no Critical, High, Medium or Low finding was observed in its bounded
review; two pure service tests with nine parser subtests, one independent
principal matrix with eight subtests, and an actual nil-service HTTP 503 probe
passed. Build, vet and diff checks also passed. It did not access the fixture.
This bounded review predates the user-authorized fixture migration and final
independent verification; it is retained only for traceability.
Archived material is under `validation/2026-09-03-b1b3-gateway-owner-acl/`.

## Final review state

PM returned **final SPEC PASS**. Independent `permission_snapshot_verify`
returned **VERDICT: PASS** with Critical/High/Medium/Low all zero. Its fresh
serial JSON run recorded 544 test passes, 0 failures, 0 skips, 15 passing
packages and 4 no-test packages; focused race passed for service (1.803s) and
handler (2.383s), with build, vet, diff and feature JSON checks passing.

The earlier writer JSON count of 531 was an intermediate run and is retained
only as history. Final raw JSON, security report and seven-file hash inventory
are archived under `validation/2026-09-03-b1b3-gateway-owner-acl/`.

After review completed, the exact task-owned MySQL and Redis containers were
rechecked for the `b1b3-gateway-owner-260903` label, disposable label,
AutoRemove, tmpfs and loopback-only mapping, then stopped by exact ID. Their
private fixture env/meta and creation script were removed; the task-label
container query is empty. No volume, shared container or production object was
touched.
