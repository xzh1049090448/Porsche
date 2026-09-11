# A06 Managed User Status Design

## Scope

A06 adds the smallest complete administrator workflow for disabling and enabling an existing visible user. It includes a dedicated v2 API, fresh authorization, optimistic concurrency, session revocation and Gateway owner-state enforcement, management audit, a reasoned disable confirmation, and frontend recovery behavior. It does not add password reset, role change, permission change, deletion, deployment, or production migration.

The accepted A05 branch is the stacked baseline. A05 nickname behavior and A14 delete behavior remain unchanged.

## Contract

The only A06 write endpoint is `PATCH /admin/v2/users/{guid}/status`.

The request is one strict JSON object with exactly `status`, `reason`, and `expected_auth_version`. `status` is `active` or `disabled`. `reason` is required and trims to 1–200 Unicode code points when disabling; it must be JSON `null` when enabling. `expected_auth_version` is a positive INT32. The response is the existing exact `UserReadDTO`; a successful transition increments `auth_version` exactly once.

The capability is selected from the requested transition: `users.disable` for active to disabled and `users.enable` for disabled to active. Repeating the current state is a 409 state conflict. Mutation requests are never retried automatically.

The legacy `PUT /admin/users/{guid}` must reject every request containing `status` before any write. Other legacy fields are outside A06 and retain their existing behavior.

## Authorization and transaction

The service locks and revalidates the actor, actor session, target, and actor policy inside one MySQL transaction. It checks the actor's Redis session revocation marker before evaluating the transition-specific capability against the latest target. Hidden targets return 404; visible authorization denial returns 403; stale target versions and invalid state transitions return distinct safe 409 codes.

Admin may manage User. Root may manage User or Admin. Nobody may manage self, Root, deleted, equal, or higher targets. Authorization is based on stored current state, never the dialog snapshot.

Both transitions revoke every active target session using the existing Redis denial barrier and durable session rows before changing status. The enable path repeats this cleanup so a session left by an older or partial disable path cannot become usable again. Both disable and enable increment `auth_version`, update mandatory audit fields, write the authentication security event, and write one official `audit_logs` row in the same SQL transaction. The management audit stores only normalized reason, target GUID, and before/after status; it does not store credentials, tokens, tickets, headers, or transport errors. Enable records no reason value.

Redis failure fails closed before success. A SQL failure after Redis barriers may leave an extra denial marker, which is safe and does not permit a disabled identity. The endpoint returns 503 and never claims success. A06 does not add an outbox or pretend that Redis and MySQL form one distributed transaction.

## Credential behavior

After disable, old Access tokens fail because current account state/version no longer matches; Refresh sessions are revoked; Gateway API Key calls fail because the Gateway reads the owner's latest status. Enable does not restore any session. A Key that was independently revoked or expired remains unusable. A still-active Key that was blocked only by owner disable may be usable again after enable, subject to the latest account state and the Key's own status, expiry, and ACL.

## Frontend

User detail shows exactly one action for the current state when the current identity, capability, hierarchy, route, target version, and target status all agree. Disable opens a controlled dialog requiring a 1–200 code-point reason and an explicit confirmation checkbox. Enable opens a controlled confirmation and sends `reason: null`.

The workflow owns one attempt, suppresses duplicate submit, retains no reason after close or settlement, and never writes reason or response data to browser storage, URL, analytics, or logs. On 409 it performs one fresh target GET and never replays PATCH. On 401 it clears the session. On 403 or 404 it refreshes authorization/target and closes fail-closed if access is no longer valid. On 503 it shows an explicit retry action for a new user gesture.

Successful reconciliation requires the same route, identity epoch, permission revision, target GUID, old auth version, expected new status, and returned auth version equal to old version plus one. Late responses cannot update another target or a reopened dialog. Focus returns to the initiating control or page heading.

## Acceptance boundary

Local joint acceptance requires strict decoder and route tests, service transaction and concurrency tests, real MySQL and Redis transition tests, Access/Refresh/Gateway Key checks, exact response and safe error tests, frontend unit tests, and visible browser tests for confirmation, visibility, conflict refresh, stale-response ownership, focus, and narrow layout. Production deployment and real business accounts remain outside this slice.
