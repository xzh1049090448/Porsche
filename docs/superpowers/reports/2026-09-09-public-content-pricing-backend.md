# Public Content and Pricing Backend Task 12 Evidence

**Status:** PASS_BACKEND_LOCAL
**Date:** 2026-09-10
**Starting revision:** `3d01a6664d0c3c18176e1f227382c9c874c2a6d4`

## Boundary

This report covers backend-local integration only. P03–P07 have backend implementation and isolated fixture evidence. P08 remains `BLOCKED_PRODUCT`: safe-draft and truth gates exist, but production claims, terms, privacy, and final content are not approved. Frontend integration, push, merge, production migration, deployment, and production acceptance were not run.

## Fixture identity and migrations

The original implementer run used unique disposable resources named `pcp-task12-mysql-0909`, `pcp-task12-redis-0909`, and `pcp-task12-net-0909`. The 2026-09-10 spec-repair run independently used `pcp-task12-r2-mysql-0910`, `pcp-task12-r2-redis-0910`, and `pcp-task12-r2-net-0910`, all labeled `codex.task=public-content-pricing-task12-r2`. Both data services were exposed only on loopback. No `.env` or production secret was read. The sanitized rerun manifest records image identities, commands, failures, cleanup, and unrelated-resource preservation without fixture credentials or URLs.

The migrated ledger contained exact active versions `0001` through `0015` with the embedded checksums. Each focused real-MySQL case removes one migration and marks its ledger row inactive, requires its dedicated verifier to reject the absent schema, and requires the global verifier to fail. The 0013 and 0014 cases reapply through the migration runner; the 0012 and 0015 cases restore their SQL and ledger state before rerunning the global migration/verifier path. Every case then requires its dedicated verifier, global verifier, and active checksum-bearing ledger row to pass. The 0015 case also rejects a weakened terminal-state CHECK constraint. The fresh four-case migration command passed with zero skips.

## Verification

- Focused service coverage passed for model CRUD/revision/identity races, immutable price snapshots, content releases and binding, Root alerts, monitor lease/safety retry, public catalog integrity, and ticket/business atomicity. Render lease/fence/replay tests passed on the migrated fixture. The fresh four-case migration and verbose handler/security/catalog/renderer commands recorded zero skips; the final passing service command was not emitted in JSON form, so this report does not assign it a machine-counted skip total.
- Handler, router, action-security, white-label catalog observation, app selection, and renderer CLI suites passed. Adversarial cases included duplicate/unknown/trailing JSON, malformed and oversized catalogs, wrong actor/action/resource/intent tickets, immutable snapshot tampering, lease loss, concurrent revision winners, and failed transaction rollback.
- `TEST_DATABASE_URL=… TEST_REDIS_URL=… go test -race ./internal/service ./internal/handler -run '(Public|RootAlert|UpstreamPrice)' -count=1` passed for both packages.
- `GOCACHE=/private/tmp/porsche-go-build-cache go test ./... -count=1` passed for every package when run with loopback listener permission. A sandbox-only attempt failed solely because `httptest` could not bind `[::1]`.
- `GOCACHE=/private/tmp/porsche-go-build-cache go build ./...` and `go vet ./...` passed.
- Intended Go files were formatted and `git diff --check` passed. No `.env`, credential file, or generated binary is included.

The first rerun used a driver DSN where the fixture guard requires a URL, so all four migration cases failed before database access. A second attempt correctly exposed that the new 0012 global assertion was too narrow: `Verify` reports the earlier ledger-level unmigrated error, while the dedicated verifier reports `ErrPublicContentPricingSchema`. The assertion was corrected to require the exact dedicated error and any non-nil global failure. A broad service attempt before migrating the parent fixture failed with missing-table diagnostics. After applying the exact 0001–0015 ledger, the focused service package passed. A diagnostic JSON rerun attempted after the disposable resources had already been removed failed and is not pass evidence. These setup failures are retained in the manifest and excluded from the pass claims.

Sanitized evidence: `docs/superpowers/reports/validation/2026-09-10-public-content-pricing-task12-r2/manifest.json`.

## Integration defects corrected

Real fixtures found that MySQL 8 adds predicate parentheses to the 0015 CHECK representation, while the verifier accepted only source formatting; the verifier now accepts exactly the source or MySQL canonical form. The 0012 verifier now permits only the complete, exact 0015 additive column/check set. Pricing publish tickets now use the schema-compatible aggregate target kind. Render renewal validates the locked lease directly so a same-clock idempotent update does not misclassify MySQL `RowsAffected=0` as lease loss. Fixture tests now mutate immutable rows with explicit SQL, seed required state, clean dependency rows in order, and expect the current 15-migration ledger.
