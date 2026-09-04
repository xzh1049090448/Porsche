# B1-B2 Managed User Security Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Revoke existing sessions, advance AuthVersion, and persist atomic security audit records for actual managed plan/ACL changes, while rejecting ambiguous administrator PUT JSON.

**Architecture:** Keep the existing route and service boundary. `admin_user_update_decode.go` owns strict top-level JSON parsing; `admin.go` only translates validated external strings. `UpdateManagedUser` compares locked persisted target values and, only for frozen security changes, reuses `revokeUserSessionsLocked` before a single user update and event-10 audit insert in its MySQL transaction.

**Tech Stack:** Go 1.22, Gin, encoding/json, GORM, MySQL 8, Redis 7, existing disposable `TEST_DATABASE_URL` / `TEST_REDIS_URL` fixture.

**Status:** AGREED_FOR_IMPLEMENTATION. PM froze: actual status/plan/ACL changes revoke and advance once; actual daily-limit-only changes write account plus event 10 without revocation; semantic no-ops have no security side effects; exact ACL sets ignore order/duplicates and treat nil/empty as equal; `{}` and explicit null are no-ops.

---

### Task 1: Establish the current session-survival failure and event-10 contract

**Files:**
- Create: `internal/service/managed_user_security_test.go`
- Modify: `internal/models/models.go`

- [ ] **Step 1: Add the first real persistence RED test for a plan change.**

```go
func TestUpdateManagedUserPlanChangeRevokesSessionsAndAudits(t *testing.T) {
	ctx, db, redisStore, auth, target, issued := changePasswordFixture(t)
	actor := createAuthSessionTestUser(t, db)
	if err := db.Model(&models.User{}).Where("id = ?", actor.ID).Update("role", models.UserRoleAdmin).Error; err != nil { t.Fatal(err) }
	plan := models.PlanProfessional
	if _, err := auth.UpdateManagedUser(ctx, actor.ID, target.Guid, ManagedUserUpdateInput{PlanType: &plan}); err != nil { t.Fatal(err) }
	stored := loadChangePasswordUser(t, db, target.ID)
	if stored.AuthVersion != target.AuthVersion+1 { t.Fatalf("auth version = %d", stored.AuthVersion) }
	assertChangePasswordSessionRevoked(t, db, issued.Session.ID)
	if revoked, err := redisStore.IsSessionRevoked(ctx, issued.Session.SID); err != nil || !revoked { t.Fatalf("revoked=%t err=%v", revoked, err) }
	assertChangePasswordAuditCount(t, db, target.ID, models.AuthAuditEventType(10), 1)
}
```

- [ ] **Step 2: Run the first test and record its actual behavior RED.**

Run: `set -a; . /private/tmp/porsche-next-slice-fixture/fixture.env; set +a; GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service -run '^TestUpdateManagedUserPlanChangeRevokesSessionsAndAudits$' -count=1 -v`

Expected before implementation: behavior RED: current code accepts the plan change while the issued session remains live and AuthVersion does not change. Event-10 mapping tests may separately be compile RED; they do not substitute for this behavior evidence.

- [ ] **Step 3: Define the stable event-10 bidirectional mapping.**

```go
const AuthAuditEventManagedUserUpdated AuthAuditEventType = 10

case AuthAuditEventManagedUserUpdated:
	return "managed_user_updated"
// ParseAuthAuditEventType:
case "managed_user_updated":
	return AuthAuditEventManagedUserUpdated, true
```

Add model contract assertions that integer 10, `String`, and `ParseAuthAuditEventType` round-trip and unknown values remain rejected.

- [ ] **Step 4: Re-run enum and current-behavior tests.**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/models ./internal/service -run 'ManagedUser|AuthAuditEvent' -count=1`

Expected: enum contract PASS; plan-change test remains behavior RED until Task 2.

### Task 2: Make frozen managed-user transitions atomic and fail closed

**Files:**
- Modify: `internal/service/auth_account.go:131-199`
- Modify: `internal/service/managed_user_security_test.go`
- Modify: `internal/service/auth_registration_test.go`

- [ ] **Step 1: Add failing actual-change and rollback tests.** Include plan change, ACL change, Redis failure before MySQL mutation, and an isolated `auth_audit_events` check constraint which rejects event 10. The rollback test must read target, sessions, AuthVersion, and audit rows after the error.

```go
if stored.PlanType != target.PlanType || stored.AuthVersion != target.AuthVersion || issued.Session.RevokedAt != nil {
	t.Fatalf("failed security transition partially committed: %#v", stored)
}
assertChangePasswordAuditCount(t, db, target.ID, models.AuthAuditEventManagedUserUpdated, 0)
```

- [ ] **Step 2: Run focused real MySQL/Redis tests and capture behavior RED.**

Run: `set -a; . /private/tmp/porsche-next-slice-fixture/fixture.env; set +a; GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service -run '^TestUpdateManagedUser' -count=1 -v`

Expected: actual plan/ACL cases fail because current code only revokes on disabled status.

- [ ] **Step 3: Implement the final locked-value transition.**

```go
statusChanged := input.Status != nil && target.Status != *input.Status
planChanged := input.PlanType != nil && target.PlanType != *input.PlanType
aclChanged := input.AllowedModels != nil && !sameManagedUserACL(target.AllowedModels, *input.AllowedModels)
limitChanged := input.DailyCallLimit != nil && target.DailyCallLimit != *input.DailyCallLimit
securityChanged := statusChanged || planChanged || aclChanged
if securityChanged {
	if target.AuthVersion >= 2147483647 { return errUnavailable("管理员用户更新暂不可用") }
	if err := a.requireAuthRedis(ctx); err != nil { return errUnavailable("管理员用户更新暂不可用") }
	event := models.AuthAuditEventSessionRevoked
	if statusChanged && *input.Status == models.UserStatusDisabled { event = models.AuthAuditEventUserDisabled }
	if err := a.revokeUserSessionsLocked(ctx, tx, &target, actor.ID, event); err != nil { return errUnavailable("管理员用户更新暂不可用") }
	target.AuthVersion++
}
if !statusChanged && !planChanged && !aclChanged && !limitChanged { updated = target; return nil }
// after the account update, once for every actual managed change:
if err := tx.Create(&models.AuthAuditEvent{AuditFields: auditFields(&actor.ID), UserID: &target.ID, EventType: models.AuthAuditEventManagedUserUpdated}).Error; err != nil { return err }
```

Use a service-local set-equality helper that copies values into maps, so order and duplicate entries do not alter authorization meaning while neither caller slice is mutated. Do not add schema, outbox, writer, or HTTP route changes.

- [ ] **Step 4: Add frozen status, daily quota, equal-value, ACL-set, and empty-input branches.** `status`/plan/ACL actual change shares one revoke/version increment; daily-limit-only actual change writes the account plus one event 10 without revoke/version; all semantic no-ops return the locked target without TouchAudit/UPDATE/event/Redis. Typed service inputs reject invalid enum with 422, daily limit outside `0..2147483647` with 400, and ACL null/empty-string items with 400 before mutation.

- [ ] **Step 5: Re-run focused service tests.**

Run: `set -a; . /private/tmp/porsche-next-slice-fixture/fixture.env; set +a; GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service -run '^(TestUpdateManagedUser|TestChangePassword)' -count=1 -v`

Expected: PASS with no skipped MySQL or Redis case.

### Task 3: Strict administrator update JSON decoder

**Files:**
- Create: `internal/handler/admin_user_update_decode.go`
- Test (owned by test worker): `internal/handler/admin_user_update_test.go`
- Modify: `internal/handler/admin.go:82-122`

- [ ] **Step 1: Write failing decoder tests for duplicate, unknown, trailing, non-object, malformed, and valid JSON.**

```go
for _, raw := range []string{
	`{"plan_type":"free","plan_type":"enterprise"}`,
	`{"plan_type":"free","unexpected":true}`,
	`{"plan_type":"free"} {"status":"active"}`,
	`["free"]`,
} {
	if _, err := decodeAdminUserUpdate(strings.NewReader(raw)); err == nil {
		t.Fatalf("accepted %s", raw)
	}
}
```

- [ ] **Step 2: Run decoder tests and capture RED.**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/handler -run '^(TestAdminUserUpdate|TestAdminUsers)' -count=1`

Expected: FAIL because the strict decoder does not exist and current `ShouldBindJSON` ignores its error.

- [ ] **Step 3: Implement `decodeAdminUserUpdate`.** Read exactly one top-level `{...}` with `json.Decoder`; reject duplicate keys through `seen[key]`, reject keys outside `adminUserUpdateFields`, store each value as `json.RawMessage`, decode it into `adminUserUpdateRequest` only after validation, then require the next decode to return `io.EOF`.

```go
if _, exists := seen[key]; exists { return managedUserUpdateRequest{}, errInvalidAdminUserUpdateJSON }
if !allowedManagedUserUpdateFields[key] { return managedUserUpdateRequest{}, errInvalidAdminUserUpdateJSON }
seen[key] = struct{}{}
```

Replace `_ = c.ShouldBindJSON(&body)` with the decoder call. A positive-int64 guid is required before it. Return fixed 400 for JSON/body violations and out-of-range daily limit, 413 for a body over 64KiB, and 422 for valid JSON with invalid known enum values before service call.

- [ ] **Step 4: Prove malformed requests do not mutate security state.** Route each invalid payload through `RegisterAdminUsers`, then assert unchanged target fields, active session, unchanged AuthVersion, absent event 10, and unchanged session-revocation audit count.

- [ ] **Step 5: Re-run handler tests.**

Run: `set -a; . /private/tmp/porsche-next-slice-fixture/fixture.env; set +a; GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/handler -run '^(TestAdminUserUpdate|TestAdminUsers)' -count=1 -v`

Expected: PASS; every invalid JSON shape is rejected before `UpdateManagedUser`.

### Task 4: Fixture-backed regression and acceptance record

**Files:**
- Modify: `progress.md`
- Modify: `feature_list.json`
- Create: `docs/superpowers/reports/2026-09-03-b1b2-managed-user-security.md`
- Create: `docs/superpowers/reports/validation/2026-09-03-b1b2-managed-user-security/validation.json`

- [ ] **Step 1: Run the complete affected test matrix with the private fixture env.**

Run: `set -a; . /private/tmp/porsche-next-slice-fixture/fixture.env; set +a; GOCACHE=/private/tmp/porsche-go-build-cache go test -p 1 ./internal/models ./internal/service ./internal/handler -count=1 -json`

Expected: every started test passes and no authorized persistence test skips.

- [ ] **Step 2: Run repository verification.**

Run: `set -a; . /private/tmp/porsche-next-slice-fixture/fixture.env; set +a; GOCACHE=/private/tmp/porsche-go-build-cache go test -p 1 ./... -count=1 -json && GOCACHE=/private/tmp/porsche-go-build-cache go build ./... && GOCACHE=/private/tmp/porsche-go-build-cache go vet ./... && git diff --check && jq empty feature_list.json`

Expected: PASS. Preserve raw output in `/private/tmp`; reports record only redacted counts and commands.

- [ ] **Step 3: Record scope and review gates.** Mark the B1-B2 feature state only after PM contract review and independent verification. Record that the slice contains no HTTP route addition, permission writer, ticket, outbox, schema migration, front-end integration, production deployment, commit, or push.

**Release gate:** This agreed local implementation uses only the existing isolated local fixture. It authorizes no new route, DTO, schema migration, permission-override writer, ticket, outbox, front-end wiring, production deployment, commit, or push. Fine-grained-permission write debt remains outside B1-B2.
