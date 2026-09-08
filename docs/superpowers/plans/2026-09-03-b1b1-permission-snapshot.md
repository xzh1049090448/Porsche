# B1-B1 Permission Snapshot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist versioned permission-policy heads and overrides, then load a fail-closed as-of-read evaluator from MySQL.

**Architecture:** An additive 0003 migration creates two policy tables and a strict schema verifier. The loader owns a `READ COMMITTED` transaction and converts only validated stable integer database codes to B1-A overrides; it never writes policy data or crosses an HTTP boundary.

**Tech Stack:** Go 1.22, GORM, MySQL 8, embedded forward migrations, Go testing with an explicit disposable `TEST_DATABASE_URL`.

---

### Task 1: Schema contract, stable encoding, and migration registration

**Files:**
- Create: `internal/migration/sql/0003_permission_policy.up.sql`
- Create: `internal/migration/sql/0003_permission_policy.down.sql`
- Create: `internal/migration/permission_schema.go`
- Create: `internal/models/permission_policy.go`
- Modify: `internal/migration/runner.go`
- Modify: `internal/migration/runner_test.go`
- Test: `internal/migration/permission_schema_test.go`, `internal/models/permission_policy_test.go`

- [ ] **Step 1: Write failing schema and encoding tests.** Assert migration order `0001,0002,0003`; exact two tables; signed BIGINT/INT/null/default/index/FK/InnoDB metadata; catalog codes 1–24; effects 1–3; and wrong shape rejection.
- [ ] **Step 2: Run the focused tests and capture RED.**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/migration ./internal/models -run 'Permission|AuthCoreMigrationContract' -count=1`

Expected: FAIL because 0003, verifier, and model mappings do not exist.

- [ ] **Step 3: Add the exact forward-only DDL, entities, mappings, embeds, and narrow runner checks.** `Up` validates applied 0003 before continue and validates newly-created tables before recording its ledger entry; `Verify` keeps exact ledger checks then validates 0003 tables.
- [ ] **Step 4: Re-run focused tests and real MySQL metadata tests.**

Run: `set -a; . /private/tmp/porsche-authz-persistence-fixture/fixture.env; set +a; GOCACHE=/private/tmp/porsche-go-build-cache go test -p 1 ./internal/migration ./internal/models -run 'Permission|AuthCoreMigrationContract' -count=1`

Expected: PASS with no skipped persistence test.

### Task 2: Read-only permission snapshot loader

**Files:**
- Create: `internal/service/permission_snapshot.go`
- Create: `internal/service/permission_snapshot_test.go`

- [ ] **Step 1: Write failing loader tests.** Cover valid no-head baseline, valid versioned empty head, allow/deny override, deleted history, invalid actor/AuthVersion, invalid head/rules/enums/role, orphan history, root DB constraint, context cancellation, rollback and two-root-connection lock behavior.
- [ ] **Step 2: Run focused tests and capture compile/behavior RED.**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service -run '^TestPermission' -count=1`

Expected: FAIL because `LoadPermissionSnapshot` and its sentinels do not exist.

- [ ] **Step 3: Implement the minimal loader.** Require root `*sql.DB`; use discarded GORM logging and one `READ COMMITTED` transaction; lock the actor `FOR SHARE`; validate actor/head/rules; translate stable codes; return only fixed sentinel errors and nil snapshots on every transaction failure.
- [ ] **Step 4: Re-run targeted real MySQL loader tests.**

Run: `set -a; . /private/tmp/porsche-authz-persistence-fixture/fixture.env; set +a; GOCACHE=/private/tmp/porsche-go-build-cache go test -p 1 ./internal/service -run '^TestPermission' -count=1`

Expected: PASS with all loader persistence cases executed.

### Task 3: Migration failure boundaries and complete regression

**Files:**
- Modify: `internal/migration/permission_schema_test.go`
- Modify: `internal/service/permission_snapshot_test.go`
- Modify: `progress.md`
- Modify: `feature_list.json`

- [ ] **Step 1: Write failing real-MySQL tests for rerun, 0002 data preservation, partial DDL failure/ledger absence, and concurrent loader lock sequencing.** Use isolated child `_test` databases for destructive shape tests; restore nothing in the shared fixture.
- [ ] **Step 2: Run and capture RED.**

Run: `set -a; . /private/tmp/porsche-authz-persistence-fixture/fixture.env; set +a; GOCACHE=/private/tmp/porsche-go-build-cache go test -p 1 ./internal/migration ./internal/service -run 'Permission|AuthCoreMigration' -count=1`

Expected: FAIL until the implementation meets all fail-closed contracts.

- [ ] **Step 3: Make only the smallest schema/loader corrections required by the tests.** Do not change 0001/0002, use AutoMigrate, add policy writers, or expose persistence fields.
- [ ] **Step 4: Run final fixture and repository verification.**

Run: `set -a; . /private/tmp/porsche-authz-persistence-fixture/fixture.env; set +a; GOCACHE=/private/tmp/porsche-go-build-cache go test -p 1 ./... -count=1 && GOCACHE=/private/tmp/porsche-go-build-cache go vet ./... && git diff --check && jq empty feature_list.json`

Expected: PASS; report any skipped test separately. Do not commit, push, deploy, or remove non-task containers.

**Release gate:** 0003 recorded after deployment must cause an old binary to fail its exact ledger check. This plan contains no production migration or rollback authorization; recovery requires a separately reviewed forward plan preserving policy history and ledger evidence.
