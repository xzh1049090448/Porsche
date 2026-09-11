# A07 managed-user credentials and entitlements local joint acceptance

Status: `PASS_LIMITED_SCOPE`

Code under test:

- backend `a600a0815b5eab5203333755a2466788fe67d61a` (implementation and tests through `9d9a660540c3b497b974545f82d2a2fbf884f0d8`)
- frontend `41648181ab42fb46fe7d45663e746121956b50b8`
- shared contract SHA-256 `9e1969b238911b6eee5b6aa85ed364e795a6854f0a026daac5d15e2ab78851be`

A07 delivers three local administrator actions: password reset, business-group change, and plan grant/adjustment. Reset uses the verified operation workflow and stable Query recovery. Group and plan are direct optimistic PATCH actions. Every actual transition revokes existing sessions, advances `auth_version`, and writes the frozen authentication and management audits. Plan changes reset the canonical daily limit to 100 while preserving usage; the existing Bearer Access runtime immediately enforces free-plan limits and keeps professional/enterprise unlimited.

## Evidence

- MySQL 8.0.46 and Redis 7.4.11 real-fixture coverage passed for reset of active and disabled targets, disabled-to-enabled follow-up, old/new password login, old Access/Refresh rejection, three target-session revocations and per-session audits, active/revoked Gateway Key preservation, stable replay, and `result_auth_version`.
- Real-fixture race tests passed for one-shot same-key reset, stale-version group/plan concurrency, and Bearer Access free/professional/enterprise quota behavior.
- Adversarial fault tests passed for reset and direct group/plan Redis denial failures, authentication-audit failures, and management-audit failures. Redis failures leave zero committed SQL facts. Later SQL failures roll back all MySQL facts and may retain only a safe Redis denial marker.
- Migration 0011 up/down/schema verification passed. The earlier full real-fixture repository run recorded 2188 leaf passes, one explicitly opt-in 100k performance skip, and zero failures. The final no-fixture repository regression, `go vet ./...`, `go build ./...`, gofmt diff, and `git diff --check` passed after all A07 test commits.
- Frontend tests passed 395/395 with explicit A03/A05/A06/A14 backend contract paths. The first unconfigured full command failed its four deliberate `missing_*_BACKEND_CONTRACT` selection guards; the configured rerun passed with zero skips. `VITE_USE_MOCK=false npm run build` passed with existing chunk warnings only.
- Visible system Chrome checks at 375px and 390px passed for all three dialogs: panels remained within both viewports, first Escape closed group/plan selects, focus stayed trapped and returned to the owned trigger, only one dialog remained visible, password fields cleared after reopen, and no page errors occurred.
- Conflict recovery now single-flights one owned detail GET. A late response cannot cross a new owner. Mounted tests cover direct success, duplicate 409, late ownership, 401 reset, focus restoration, and pending-recovery read-only behavior.
- Backend review returned `BACKEND_REVIEW_PASS`, frontend review returned `FRONTEND_REVIEW_PASS`, and document review returned `DOC_PASS`.
- Every task-created MySQL schema was dropped and Redis DB 15 was flushed after each real-fixture run. Final residue checks returned `0` and `0`.

## Matrix result and boundaries

A07 moves from `BLOCKED_NOT_IMPLEMENTED` to `PASS_LIMITED_SCOPE`. The 26-item matrix becomes 14 `PASS_LIMITED_SCOPE`, 10 `BLOCKED_NOT_IMPLEMENTED`, one `BLOCKED_PRODUCT`, and one `BLOCKED_ENV`, leaving 12 blocked items. `web-012` remains `in_progress` with overall result `PARTIAL_ACCEPTANCE_14_LIMITED_12_BLOCKED`.

Gateway Key owner plan/quota atomic reload and consumption remains `BLOCKED_NOT_IMPLEMENTED`; this acceptance does not claim complete API-Key entitlement consistency. The reset action does preserve existing active/revoked Key state and current owner-state authentication semantics.

Production migration, deployment, production acceptance, and real business accounts are `NOT_RUN`. No external backend `project_manager` written confirmation was obtained in this run. No push, merge, deployment, or production data operation occurred.
