# B1-B1 Permission Snapshot Validation

## Scope

Local-only: forward-only 0003 policy schema, stable integer mappings, strict schema verification, and a read-only snapshot loader. No HTTP, DTO, policy writer, role mutation, frontend, production migration, deployment, commit, or push.

## Fixture

Task-owned MySQL 8/Redis 7 uses loopback-only publishing, task labels, AutoRemove and tmpfs storage. It is distinct from existing test containers and named volumes. TEST variables are loaded privately and never printed.

## Focused evidence

Initial API/entity absence was a compile RED. A real verifier issue then exposed MySQL information_schema label casing: GORM mapped unaliased labels to empty metadata fields. The fix explicitly aliases metadata labels while retaining strict types, signedness, null/default, indexes, InnoDB, same-schema single-column FK and non-cascading update/delete checks.

Focused real-MySQL `TestPermission*` passes. It covers no-head baseline v0; a valid empty head returning its explicit versioned baseline; persisted allow/deny; corrupt head/rule, orphan/history, invalid actor, nil/closed/external roots, cancelled reads, forced commit failure, and independent reader/writer root pools. The dual-root writer changes head before rules while holding users FOR UPDATE; loader waits on FOR SHARE and returns no mixed snapshot, then observes v1 allow after rollback or v2 deny after commit.

## Release boundary

0001/0002 remain unchanged. Old binaries reject a 0003 ledger and new binaries reject missing 0003. Production migration and rollback are not authorized; recovery needs a separately reviewed non-destructive forward plan.

## Historical pre-review gates (completed)

The first serialized fixture run on this implementation snapshot produced 456 top-level/subtest pass events, 0 test skip, 0 test fail; package events were 15 pass, 0 fail and 4 no-test-file skips. `go build ./...`, `go vet ./...`, `git diff --check` and JSON parsing passed. The raw JSON is private temporary evidence, not committed material.

The migration worker subsequently added and verified the missing-fixture skip guard and unsafe-target rejection. These were historical gates, not current blockers: `go-012` is now passing after the PM and independent reviews recorded below.

## Specification review

2026-09-03: backend project manager completed final specification review and returned **SPEC PASS**. This was the precondition for the completed independent permission-snapshot verification below; neither review authorizes production release.

## Final independent verification

2026-09-03: `permission_snapshot_verify` returned **VERDICT PASS**, with no Critical, High, Medium, or Low finding. Its fresh full run recorded 456 passing test events, 0 skips and 0 failures; 15 passing packages and 4 no-test-file packages; build, vet, diff and JSON checks all exited zero. Its reader-before-writer probe passed in 1.362s: the loader held `FOR SHARE` to commit, blocked the writer for at least 250ms, then observed v1 allow before the writer commit and v2 deny afterwards.

Archived safe verification material is under `validation/2026-09-03-b1b1-permission-snapshot/`; the probe source is stored as `.go.txt` so it is not part of `go build`. 0001/0002 SQL hashes and B1-A authz hashes were unchanged; B1-B1 production-file hashes are recorded in `validation.json`. No HTTP, DTO, frontend integration, production migration/deployment, commit, or push occurred.
