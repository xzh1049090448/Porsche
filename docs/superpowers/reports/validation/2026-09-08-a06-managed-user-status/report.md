# A06 managed-user status local acceptance

Status: `PASS_LIMITED_SCOPE`

Code under test:

- backend `08617d400228c224fa2312583fde14f54f7a7686`
- frontend `07c7b9e3caa5fe18bd69be74e71f577577943840`
- shared contract SHA-256 `c3662b25500879d67c6811fa270d4a6a39a44db812c7c493d6f02e535940b415`

The frozen A06 slice adds `PATCH /admin/v2/users/{guid}/status`, strict transition input, fresh actor/session/policy/target authorization, optimistic version checks, transactional user/authentication/management audit writes, and Redis plus durable session revocation for both disable and enable. The legacy PUT rejects every body containing `status` before a write. The frontend uses one owned dialog workflow, never replays PATCH, and performs at most one shared detail GET after either 409.

## Evidence

- Real isolated MySQL 8 and Redis 7 A06 service suite passed, including old Access/Refresh/Key behavior, enable without session revival, independently revoked Key behavior, actor and session drift, Redis failure, INT32 overflow, write/auth-audit/management-audit/commit rollback, audit privacy, and concurrent exactly-once.
- Focused DTO/handler/router suites passed. Exact request/response keys, error envelope, path/query/content type, body size, malformed JSON, legacy retirement, and zero-service-call rejection are covered.
- Focused backend race passed for service and handler. The no-fixture full repository suite passed all packages. `go vet ./...`, server build, contract byte comparison, and `git diff --check` passed.
- Frontend full suite passed `364/364`; the final Element Plus and UserDetail mounted subset passed `13/13`. Production build with `VITE_USE_MOCK=false` passed with existing bundle warnings only.
- Headless Chrome layout checks at 375px and 390px produced document/body widths equal to the viewport, wrapped actions, and long-name breaking. Screenshots were generated under `/private/tmp` as transient local evidence.
- Independent backend and frontend reviews both returned `REVIEW_PASS` for the frozen A06 contract.

## Boundaries and residuals

- Production migration, deployment, production acceptance, and real business accounts were not run.
- The broader PRD requirement for request-ID-bearing, result-bearing, redacted audit rows for rejected management actions is outside the frozen A06 contract and remains open for a separately designed audit slice. Successful A06 transitions do have same-transaction authentication and management audits.
- A fixture-backed whole-repository run was not used as final evidence because the reused parent test database had no `business_groups` migration and action-operation suites require an explicit test HMAC key. A06's dedicated child-schema real tests passed; the no-fixture whole-repository regression passed.
- No backend `project_manager` written confirmation was obtained through an external communication channel in this run. The local independent backend review is not represented as that confirmation.
- No push, merge, deployment, or production data operation occurred.
