# B1-A Pure Authorization Implementation Plan

## Goal

Implement the default-deny, 24-capability internal authorization catalog and four fixed pure-Go evaluator entrypoints. The module is an in-memory projection for future administrative work only; it changes no public endpoint.

## Architecture

`internal/authz` receives a trusted account snapshot and copied admin-only overrides, and performs no I/O. Evaluation order is: active manager; known and available capability; entrypoint scope; Root/self/same-or-higher target boundary; tombstone rule; explicit deny; grantable allow; inherit/default. `Decision` defaults to `Denied`; future authenticated adapters may map `Hidden` to their uniform 404 and `Denied` to 403.

The evaluator cannot prove authentication, session validity, actor or target freshness, policy version, database visibility, transaction correctness, ticketing, auditing, idempotency, outbox delivery, or action completion. Those remain integration work. Root-only and unavailable capabilities never accept admin overrides. `users.create_admin` is deliberately not a capability.

## Tech Stack

Go 1.22, existing `internal/models` role/status enums, and standard-library `errors`, `reflect`, and `testing`. No new dependency, database model, migration, router, handler, DTO, frontend code, SSE behavior, deployment, or model call.

## Files

| File | Responsibility |
| --- | --- |
| `internal/authz/catalog.go` | Fixed 24 definitions, scope flags, copied catalog output, private lookup. |
| `internal/authz/evaluator.go` | Snapshot validation, copied overrides, decisions, target boundaries, copied capability projection. |
| `internal/authz/evaluator_test.go` | Test-first contract, failure boundaries, copy and race coverage. |
| `docs/superpowers/specs/2026-09-03-b1a-pure-authz-design.md` | Scope and integration boundary record. |

## Task 1: Test-first contract

- [x] Create `internal/authz/evaluator_test.go` for active User/Admin/Root snapshots.
- [x] Specify 24 names, Admin defaults, four Root-only definitions, unavailable `users.quota.adjust`, and ordinary-user empty projection.
- [x] Run `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/authz -count=1`. Compile RED expected missing `Account`, `Override`, `Evaluator`, `NewEvaluator`, `Catalog`, `Effect`, and `Decision`; actual exit 1, `/private/tmp/admin-public-260903-b1a-compile-red.log`.
- [x] Add only an API-complete deny-all skeleton; compile recovery is not behavioral success.
- [x] Re-run focused test. Behavior RED expected empty catalog, denied override allow, accepted invalid actor, missing hidden target boundary, and empty projection; actual exit 1, `/private/tmp/admin-public-260903-b1a-behavior-red.log`.

## Task 2: Fixed catalog and evaluator

- [x] Add, in order: `users.read`, `users.create`, `users.edit`, `users.enable`, `users.disable`, `users.reset_password`, `users.sessions.read`, `users.sessions.revoke`, `users.plan.change`, `users.group.change`, `users.quota.adjust`, `users.delete`, `users.deleted.read`, `users.promote`, `users.demote`, `users.permissions.write`, `users.audit.read`, `groups.read`, `groups.write`, `public_content.read`, `public_content.edit`, `public_content.preview`, `public_content.publish`, `public_content.rollback`.
- [x] Mark `users.promote`, `users.demote`, `users.permissions.write`, and `groups.write` Root-only and ungrantable; mark `users.quota.adjust` unavailable and ungrantable; copy `Catalog` output.
- [x] Implement `NewEvaluator(Account, []Override) (*Evaluator, error)`: require positive ID/GUID, known role, active non-deleted actor, and valid status/deletion. Permit valid ordinary User with no overrides but deny every management decision. Reject non-Admin, unknown, duplicate, unavailable, Root-only, ungrantable, and invalid-effect overrides. Copy accepted overrides.
- [x] Implement `User`, `Create`, `Collection`, `Resource`, and `CapabilityNames`. User actions require a target and hide self, Root, same-or-higher role, and invalid targets. Tombstones require `users.deleted.read` and permit only read/audit/deleted-read. Root may create User/Admin but never Root.
- [x] Document every exported type, function, variable, and method with snapshot/integration obligations.
- [x] Format `catalog.go`, `evaluator.go`, and `evaluator_test.go` with `gofmt -w`.
- [x] Run `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/authz -count=1`: expected GREEN/no skips; actual exit 0, `/private/tmp/admin-public-260903-b1a-green.log`.

## Task 3: Verification and review handoff

- [x] `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/authz -race -count=1`: exit 0, `/private/tmp/admin-public-260903-b1a-race.log`.
- [x] `GOCACHE=/private/tmp/porsche-go-build-cache go vet ./internal/authz`: exit 0, `/private/tmp/admin-public-260903-b1a-vet.log`.
- [x] `GOCACHE=/private/tmp/porsche-go-build-cache go build ./...`: exit 0, `/private/tmp/admin-public-260903-b1a-build.log`.
- [x] `env -u TEST_DATABASE_URL -u TEST_REDIS_URL -u RUN_START_COMMAND GOCACHE=/private/tmp/porsche-go-build-cache go test ./... -count=1`: exit 0. JSON evidence records 98 test fixture skips and four no-test-file package skips, `/private/tmp/admin-public-260903-b1a-full-go-test-json.log`.
- [x] `git diff --check`: exit 0; `go.mod` and `go.sum` have no diff. `git status --short` contains only B1-A files.
- [x] Backend Project Manager specification review: PASS for the limited B1-A contract (24 capabilities, three decisions, four entrypoints, Root/peer/self boundaries, tombstones, defensive copies, and no integration wiring). `scope` and `Decision` are non-persisted internals; future work must not use their current numeric values as stable database/API encodings.
- [x] `admin_authz_security_verify` independently returned PASS with no Critical, High, or Medium finding. It verified the reviewed source hashes, full JSON counts, focused race/build/vet, and an external consumer probe with two tests and 32 concurrent readers.

## Required test behavior

Tests cover catalog shape and Admin/Root baseline, override precedence/validation, invalid actor fail-closed behavior, role/self boundaries, tombstone read-only behavior, fixed-entrypoint scope separation, copied inputs/projections, and concurrent read-only evaluation. The focused package uses no MySQL, Redis, `.env`, or `TEST_*` fixture and must have zero skips. Full-suite fixture skips are not integration acceptance.

## Approved implementation reference

> For agentic workers: REQUIRED SUB-SKILL: Use `executing-plans` to execute this plan task-by-task. REQUIRED SUB-SKILL: Use `test-driven-development` before production implementation. Preserve the stated order: test compile RED, API-complete deny-all behavior RED, then minimal GREEN. Do not add dependencies or integration wiring.

The following three complete source references are deliberately pinned to the
review diff. They are the implementation contract for a resumed worker; any
semantic change requires Root/PM review first.

### `internal/authz/catalog.go`

```go
// Complete implementation reference: internal/authz/catalog.go in this B1-A review diff.
// It declares scope, Definition, all 24 fixed definitions, Catalog (defensive copy),
// and lookup. The ordered capability list and flags are specified in Task 2 above.
```

### `internal/authz/evaluator.go`

```go
// Complete implementation reference: internal/authz/evaluator.go in this B1-A review diff.
// It declares Effect, Decision, Account, Override, Evaluator, errors, NewEvaluator,
// knownRole, validAccount, manager, capability, User, Create, Collection, Resource,
// and CapabilityNames. Its full decision order and integration obligations are specified
// in Architecture and Task 2 above.
```

### `internal/authz/evaluator_test.go`

```go
// Complete implementation reference: internal/authz/evaluator_test.go in this B1-A review diff.
// It contains TestCatalogAndBaseline, TestOverridePrecedenceAndValidation,
// TestBadActorsFailClosed, TestUserHardBoundariesAndInvalidTargets,
// TestTombstonesAreReadOnly, TestEntrypointsCannotBypassTargets,
// TestInputsAndProjectionAreIsolated, and TestConcurrentEvaluation.
```
