# B1-A Pure Authorization Design

## Scope

`internal/authz` provides a fixed, in-memory capability catalog and immutable
authorization evaluator for future administrative work. It has no HTTP, DTO,
database, migration, configuration, session, model-provider, or frontend
integration. Existing public APIs retain their current behavior.

## Contract

The package exposes a 24-capability catalog and four fixed decision entrypoints:
`User`, `Create`, `Collection`, and `Resource`. `NewEvaluator` accepts a
trusted service-layer account snapshot and admin-only overrides. It copies the
overrides and returns an error for invalid actors or policies; callers must not
fall back to an empty evaluator after an error.

`Decision` is deny by default. A future HTTP adapter maps `Denied` to 403 and
`Hidden` to its uniform 404 response only after authentication has succeeded.
Authentication, actor/target freshness, session validity, policy-version
consistency, transactions, tickets, audit, and business completion remain
integration obligations and are not established by this package.

## Policy

Only active, non-deleted Admin and Root snapshots can make management
decisions. Root allows every available catalog capability; Admin starts with
the documented defaults and may apply valid grantable allow/deny/inherit
overrides. Unknown, duplicate, root-only, unavailable, or non-admin overrides
are rejected. Ordinary users can construct an evaluator with no overrides but
receive no management capability.

Targets are required for user actions. Self, same-or-higher-role, Root, and
invalid targets are hidden. Tombstones are visible only with `users.deleted.read`
and then only through the read/audit/deleted-read actions. The catalog itself
and returned projections are copied so callers cannot mutate evaluator state.

## Verification

Tests are written first and cover the catalog, default deny behavior, override
precedence, invalid inputs, user role and tombstone boundaries, entrypoint
scope separation, defensive copies, and concurrent read-only evaluation. They
run without database or Redis fixtures.
