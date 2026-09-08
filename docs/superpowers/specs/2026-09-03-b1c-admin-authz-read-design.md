# B1-C Admin Authorization Read APIs

**Status: AGREED_FOR_IMPLEMENTATION.** This local slice adds two read-only
admin endpoints. It adds no schema, permission writer, ticket, outbox, frontend
UI, deployment, commit, or push.

## Endpoints and visibility

`GET /admin/v2/authz/catalog` is available only to active, non-deleted Admin or
Root actors. `GET /admin/v2/users/{guid}/permissions` is available only to an
exact Root actor for a non-deleted Admin target, including a disabled target.
Self, Root, User, missing, and deleted targets all produce the same 404. GUID
is positive canonical decimal int64 only: no sign, whitespace, empty input or
leading zero. On the two matched routes, any nonempty RawQuery or noncanonical GUID produces
400. Unmatched paths use existing Gin router semantics (404/standard redirect);
no third endpoint or global path handling is added. Known
unauthorized actors receive 403; unavailable dependencies or corrupt persisted
authorization data receive fixed 503. Existing middleware 401 behavior remains
unchanged. A fresh service read that finds a missing, disabled/deleted, stale,
revoked or expired actor/session returns 401 with `认证会话无效`; Redis revocation
also returns 401. Known current actors lacking endpoint role eligibility return
403. Nonpositive persisted security versions and unknown roles/statuses remain
corrupt data (503), except for targets already excluded by the hiding rules below.

Both registered endpoints run `gatewayRequestID()` before authentication and set
`Cache-Control: no-store`. Their success and errors carry `X-Request-ID`; error bodies
do not add a request-id field. DTOs omit id, AuthVersion, SID, audit fields and
password data. After fresh session validation, target hiding takes precedence:
missing, non-visible deletion state, self, known Root or User produces the same
404 even when that hidden target has a bad status/AuthVersion. Only targets not
already excluded by those rules expose corruption as fixed 503 for unknown
role/status or a nonpositive security version.

## Read and projection contract

The catalog is catalog version 1, effects `[inherit, allow, deny]`, and the 24
stable capabilities in catalog order. Every item contains only name,
admin_default, grantable, root_only, and `available = !Unavailable`.

Detail returns only decimal `user_guid`, role `admin`, status `active` or
`disabled`, catalog_version 1, decimal permissions_version, and a non-null
capabilities array. An absent head with no override history has version `"0"`.
Each capability exposes name, baseline, override (default inherit),
policy_effective, and `effective = target active && policy_effective`. A valid
head with no rows retains its nonzero policy version. Root-only
or unavailable capability is always false for an Admin. Policy evaluation keeps
deny precedence, then grantable allow, then baseline.

## Fresh authenticated read

The service accepts only a root `*sql.DB` GORM pool and owns a READ COMMITTED
transaction. It locks actor user SHARE, optional target user SHARE, then actor
session SHARE; it validates actor state/AuthVersion and session SID ownership,
version, deletion, revocation and expiry, plus the Redis revocation barrier.
Role-denied actors skip target queries but still validate their current session
and Redis barrier before 403. Hidden/corrupt target results are also deferred
until this fresh authentication check; authentication loss takes precedence.
Actor/session record-not-found is 401; SQL/Redis I/O errors remain fixed 503.
The transaction commits before returning a DTO. Reads are as-of-load only and
do not revoke an in-flight request.

Middleware carries only the authenticated session version through a private
context key/getter. The service's internal actor input is UserID, AuthVersion,
SessionSID and SessionVersion. It never accepts session version or identity from
an HTTP DTO. SQL logging is discarded and selects only needed fields.

`readPermissionPolicyRows(tx, userID)` is extracted for reuse by the existing
snapshot loader and this projection. It preserves strict permission-head and
row validation. A disabled target uses a read-only projection and is never
treated as an active authorization evaluator.

## Error envelope

The fixed public messages are: 400 `无效请求参数`; fresh-service 401 `认证会话无效`; 403 `无权限访问`; 404 `权限目标不存在`; and 503 `权限信息暂不可用`. Existing middleware authentication failures remain 401 and are not globally changed. Underlying SQL, Redis, policy and role details are not exposed.

## Validation boundary

Start with pure projection and strict GUID tests. Real fixture coverage follows
only after a separately authorized disposable MySQL/Redis fixture is verified.
It covers transaction/lock ordering, commit failure, corrupt policy/session
states, Redis failure, Root/Admin/User targeting, disabled target projection,
and actual HTTP envelopes. B1-B3 wording is corrected: list still requests the
Key/global catalog before owner filtering; detail/chat/SSE denials are the
zero-upstream paths.
