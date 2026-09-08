# A05 managed-user nickname edit — Task7 r6 isolated acceptance

**Verdict: BLOCKED.** r6 supersedes r1–r5. It is isolated acceptance evidence only, not a release or production approval.

r5 used separate test/runtime MySQL databases and Redis namespaces. Its A05 service gate passed 40 actual records with zero failures/skips; DTO/handler/router passed 71 records with zero failures/skips. The fresh r5 runtime migrated 0001–0010, bootstrapped exactly one Root, and passed a real HTTP matrix (10 operations): Root-to-User set/clear, Root-to-Admin, Admin-to-User, 409, forged and duplicate-field 400, ordinary-user 403, Admin-to-Root 404, exact DTO/headers, exactly two PATCH keys, no replay, and four audit records.

r6 used a new loopback-only MySQL 8/Redis 7 fixture, separate runtime database, private credentials, and migration ledger 0001–0010. Bootstrap and aggregate verification yielded exactly Root=1, Admin=1, User=1. Controlled backend/frontend listeners and private login/detail HTTP smoke passed.

The initial browser fixture was blocked by cross-origin refresh; a temporary uncommitted same-origin Vite proxy corrected that setup. Visible Chrome then logged in successfully. Its required direct-detail check remained blocked: Root identity response was 200, had `users.edit` among 23 capabilities, target response was 200 active User auth version 1, and the edit predicate evaluated true. A direct browser load of `/users/<guid>` nevertheless performed a full reload, lost in-memory session state, received 403 from `/api/v1/auth/refresh`, and redirected to `/login?redirect=/users/<guid>`. No detail component rendered and no A05 PATCH occurred. This is a session/direct-detail continuity blocker, not a seed, role, capability, target, or predicate failure.

No Root/Admin UI mutation, browser set/clear, conflict, keyboard/focus, width, race, console-clean, adversarial, or privacy PASS is claimed. Evidence contains no credentials, tokens, usernames, nickname values, request bodies, or internal IDs. All r6 listeners, containers, network, temporary proxy, and private files are removed after this record.
