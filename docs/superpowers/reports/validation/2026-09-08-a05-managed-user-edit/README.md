# A05 Task 7 isolated joint acceptance

**Verdict: PASS for the local A05 nickname-edit slice.** The final r10 run supersedes the r1-r9 blocked/setup runs and combines the retained r5 backend/service/HTTP evidence with the allowed-browser coverage previously reached in r8 and independently exercised again in r10. This evidence does not approve production migration, deployment, production data, or tracker changes.

## Frozen candidates and runtime

- Backend evidence HEAD: `667639b7446724c5e3ea6109a3ec728d496a4027`; tested code and tests through `6bb54007879adeb16a532d792ab471f16ee9100a`.
- Frontend evidence HEAD: `c32bca8bc5dd6f7d8159ce9ce3106eb590799734`; tested runtime code through `5a41f5679c1c35e5c2850665db54ae83e58db436`.
- r10 used disposable, task-labeled MySQL 8.0.46 and Redis 7.4.11 resources, a distinct `*_test` database and Redis namespace, migrations `0001`-`0010`, localhost-only backend/Vite listeners, Playwright 1.61.1, and visible Chrome 152.0.7977.77.
- Credentials existed only in a mode-0700 private directory with mode-0600 source files. Browser traffic used the temporary same-origin Vite proxy. The refresh-cookie gate queried `/api/v1/auth/refresh`, matching the cookie path, and verified one `porsche_refresh` cookie with HttpOnly, Secure, SameSite=Lax, and Path=`/api/v1/auth`.

The first r10 MySQL process forced an incompatible authentication plugin and failed before migration writes; it was replaced with the image default. The first bootstrap attempt rejected an incorrectly encoded action HMAC key during configuration loading and created no Root. An incorrect fixture Admin enum was caught by aggregate seed validation and corrected before browser testing. The accepted runtime then had one active Root, two active Admins, one active User, and one deleted User.

## Retained backend and HTTP evidence

r5 remains PASS: 40 A05 service records and 71 DTO/handler/router records ran with zero failures and zero skips. Its ten-operation HTTP matrix covered Root-to-User set/clear, Root-to-Admin, Admin-to-User, a real stale-version 409, strict malformed/forged/duplicate input rejection, ordinary-user 403, Admin-to-Root 404, exact success/error DTOs and security headers, exact two-key PATCH bodies, no replay, database facts, rollback, and audit assertions.

## r10 visible-browser and adversarial results

- Allowed UI: login, direct-detail reload, 1440px desktop and 375px mobile layout, initial nickname focus, forward/reverse focus containment, Escape close and focus restoration, disabled duplicate submit with exactly one PATCH, exact two-key PATCH body, 64-code-point nickname, null clear, and success aria-live all passed. Page errors and unexpected console errors were zero.
- Conflict and late ownership: a real stale-version PATCH returned the exact 409 envelope, caused exactly one target GET, and never replayed PATCH. Route, identity, and dialog ownership changes each discarded a delayed successful response without a stale callback. Page errors were zero.
- Visibility and denial: Root-to-Admin and Admin-to-User edit entries were visible. Root self, Admin self, equal Admin, Root target, and deleted target were hidden behind real 404 detail responses. An ordinary user saw no edit entry and generated no `/admin/v2` request. A live `users.edit` deny produced exactly one 403 PATCH, one successful identity refresh, one target refresh, no replay, a closed dialog, and an edit entry that stayed closed under the refreshed projection. The temporary deny rows were removed afterward.
- HTTP adversarial: 22 cases covered unknown keys, six forbidden fields, exact duplicate keys, case-folded keys, trailing JSON, a scalar body, oversized input, five bad GUID forms, query parameters, invalid media type, stale version, and ordinary-user denial. Every error used the exact A05 envelope, matching response/body request IDs, `Cache-Control: no-store`, and no `Retry-After`. Target nickname/auth-version and managed-edit audit count were unchanged across the rejection matrix.
- Privacy: the HttpOnly refresh value was absent from `document.cookie`; legacy token/user storage keys were absent; coordination storage contained only epoch/pending/suppressed metadata; console exact-secret and page-error scans had zero findings; retained network evidence contained only method/category/status/query-presence metadata. MySQL general logging was off. The audit table has no nickname/payload/detail/message/body/password/token/secret column, and all eight managed-user-update audit rows contained only the expected metadata fields with nullable session/login/IP/user-agent fields unset.

All independent requested scenarios completed. No application code, contract, tracker, configuration, deployment file, or production resource changed. Exact r10 processes, containers, network, scripts, results, credentials, and private directory were removed after the evidence commits; final residue checks are recorded in the manifest.
