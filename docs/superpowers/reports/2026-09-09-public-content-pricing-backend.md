# Public Content and Pricing Backend Task 12 Evidence

**Status:** PASS_BACKEND_LOCAL
**Date:** 2026-09-10
**Starting revision:** `3d01a6664d0c3c18176e1f227382c9c874c2a6d4`

## Boundary

This report covers backend-local integration only. P03–P07 have backend implementation and isolated fixture evidence. P08 remains `BLOCKED_PRODUCT`: safe-draft and truth gates exist, but production claims, terms, privacy, and final content are not approved. Frontend integration, push, merge, production migration, deployment, and production acceptance were not run.

## Fixture identity and migrations

Disposable resources used unique names `pcp-task12-mysql-0909`, `pcp-task12-redis-0909`, and network `pcp-task12-net-0909`, all labeled `codex.task=public-content-pricing-task12`. MySQL was exposed only on loopback port 33317 and Redis on loopback port 36387 DB 11. No `.env` or production secret was read.

The migrated ledger contained exact versions `0001` through `0015`. Focused real-MySQL tests independently removed and reapplied 0012, 0013, 0014, and 0015; their exact schema verifiers failed closed while removed or weakened and the global verifier passed after reapply. All four leaf tests passed with zero skips.

## Verification

- Focused service coverage passed for model CRUD/revision/identity races, immutable price snapshots, content releases and binding, Root alerts, monitor lease/safety retry, public catalog integrity, and ticket/business atomicity. Render lease/fence/replay tests passed separately on a clean fixture. No focused fixture test skipped.
- Handler, router, action-security, white-label catalog observation, app selection, and renderer CLI suites passed. Adversarial cases included duplicate/unknown/trailing JSON, malformed and oversized catalogs, wrong actor/action/resource/intent tickets, immutable snapshot tampering, lease loss, concurrent revision winners, and failed transaction rollback.
- `TEST_DATABASE_URL=… TEST_REDIS_URL=… go test -race ./internal/service ./internal/handler -run '(Public|RootAlert|UpstreamPrice)' -count=1` passed for both packages.
- `GOCACHE=/private/tmp/porsche-go-build-cache go test ./... -count=1` passed for every package when run with loopback listener permission. A sandbox-only attempt failed solely because `httptest` could not bind `[::1]`.
- `GOCACHE=/private/tmp/porsche-go-build-cache go build ./...` and `go vet ./...` passed.
- Intended Go files were formatted and `git diff --check` passed. No `.env`, credential file, or generated binary is included.

## Integration defects corrected

Real fixtures found that MySQL 8 adds predicate parentheses to the 0015 CHECK representation, while the verifier accepted only source formatting; the verifier now accepts exactly the source or MySQL canonical form. The 0012 verifier now permits only the complete, exact 0015 additive column/check set. Pricing publish tickets now use the schema-compatible aggregate target kind. Render renewal validates the locked lease directly so a same-clock idempotent update does not misclassify MySQL `RowsAffected=0` as lease loss. Fixture tests now mutate immutable rows with explicit SQL, seed required state, clean dependency rows in order, and expect the current 15-migration ledger.
