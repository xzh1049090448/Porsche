# B1-E implementation baseline

## Commit

- Repository root: `/Users/xuzhihao/code/Porsche/.worktrees/admin-public-260903`
- Baseline HEAD: `8fa276e50a1236d74069b0e96b11a3d04023efe1`
- Approved design commit in parent chain: `4df7482c2e5eb9c3ba829acf8da8f272978b9096`
- Approved design SHA-256: `3e02e12386a994d34e7704110b96185e73cfcbd7ff98a3a20c648069fbaaf9ff`
- The plan's older expected HEAD names the design commit; the clean implementation baseline is the subsequent plan commit `8fa276e5`, which preserves `4df7482c` as its parent.

## Commands and exit codes

`<private-cache-directory>` and `<private-json-output>` are redacted operator-owned paths; substituting writable private paths reproduces the executed command structure without disclosing local path values.

| Command | Exit code | Result |
| --- | ---: | --- |
| `pwd; git rev-parse HEAD; git status --short; git log -5 --oneline; sha256sum docs/superpowers/specs/2026-09-04-b1e-operation-safety-foundation-design.md` | 0 | root, commit chain, clean status, and design digest matched |
| `env -u TEST_DATABASE_URL -u TEST_REDIS_URL -u DATABASE_URL -u REDIS_URL -u APP_ENV -u RUN_START_COMMAND ./init.sh` | 1 | sandbox denied the default Go build cache before tests |
| `GOCACHE=<private-cache-directory> env -u TEST_DATABASE_URL -u TEST_REDIS_URL -u DATABASE_URL -u REDIS_URL -u APP_ENV -u RUN_START_COMMAND ./init.sh` | 1 | sandbox denied `httptest` loopback binds |
| `GOCACHE=<private-cache-directory> env -u TEST_DATABASE_URL -u TEST_REDIS_URL -u DATABASE_URL -u REDIS_URL -u APP_ENV -u RUN_START_COMMAND ./init.sh` executed through approved sandbox escalation | 0 | initialization and all packages passed; start command was not executed |
| `GOCACHE=<private-cache-directory> env -u TEST_DATABASE_URL -u TEST_REDIS_URL go test -p 1 ./... -count=1` executed through approved sandbox escalation | 0 | full no-fixture gate passed |
| `GOCACHE=<private-cache-directory> env -u TEST_DATABASE_URL -u TEST_REDIS_URL go test -json -p 1 ./... -count=1 > <private-json-output>` executed through approved sandbox escalation | 0 | replayable JSON source for counts and skip reasons |
| `GOCACHE=<private-cache-directory> go build ./...` | 0 | passed |
| `GOCACHE=<private-cache-directory> go vet ./...` | 0 | passed |
| `git diff --check` | 0 | passed |

## Package and test counts

- Go packages: 19 total; 15 passed; 4 had no test files; 0 failed.
- Packages with no test files: `cmd/migrate`, `cmd/server`, `internal/httpx`, `internal/security`.
- Terminal test events: 389 passed; 258 skipped; 0 failed.
- Leaf tests: 345 passed; 256 skipped; 0 failed.
- Fixture-variable skips: 255 leaf tests.
- Separate authorization-gated performance NOT_RUN: 1 leaf test.

A terminal event is the final `pass`, `skip`, or `fail` JSON action for any named test or subtest. A leaf is a named test for which no final named event in the same package starts with that test name plus `/`; this removes parent test nodes while retaining the deepest subtests. Final actions are keyed by `(Package, Test)` before counting. Skip classification uses the leaf's emitted `t.Skip` reason: a reason naming `TEST_DATABASE_URL` or `TEST_REDIS_URL` is fixture-variable absent; the opt-in 100k reason is authorization-gated and is not classified as a missing-fixture pass.

The JSON count and classification were reproduced with this structure:

```python
import json, re, sys
from collections import Counter, defaultdict

events = [json.loads(line) for line in sys.stdin]
final = {(event["Package"], event["Test"]): event["Action"]
         for event in events
         if event.get("Test") and event.get("Action") in {"pass", "skip", "fail"}}
leaves = {key: action for key, action in final.items()
          if not any(package == key[0] and test.startswith(key[1] + "/")
                     for package, test in final)}
outputs = defaultdict(list)
for event in events:
    key = (event.get("Package"), event.get("Test"))
    if event.get("Action") == "output" and key in leaves:
        outputs[key].append(event.get("Output", "").strip())

classified = defaultdict(list)
for key, action in leaves.items():
    if action != "skip":
        continue
    lines = [line for line in outputs[key] if "SKIP:" not in line and "=== RUN" not in line]
    reason = re.sub(r"^\S+\.go:\d+:\s*", "", lines[-1])
    if "opt-in 100k" in reason:
        category = "authorization"
    elif "TEST_" in reason:
        category = "fixture-variable"
    else:
        category = "unclassified"
    classified[category].append((key, reason))

print("terminal", dict(Counter(final.values())))
print("leaf", dict(Counter(leaves.values())))
for category in sorted(classified):
    print(category, len(classified[category]))
    for (package, test), reason in sorted(classified[category]):
        print(f"{package}::{test}\t{reason}")
```

Save the displayed script as `<leaf-counter-script>` and run the complete redacted command `python3 <leaf-counter-script> < <private-json-output>`; no environment value is required by the counter.

## Skip classification and exact leaf names

### Fixture-variable absent — 255 leaf tests

- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminAuthzHTTPCatalogRoleMatrixAndDTO` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL fixtures`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminAuthzHTTPDetailTargetVisibility` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL fixtures`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminAuthzHTTPPolicyProjectionAndCorruption` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL fixtures`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminAuthzHTTPRejectsNoncanonicalGUIDAndQuery` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL fixtures`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminAuthzHTTPRejectsStaleOrRevokedAuthentication` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL fixtures`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminHealthCheckRejectsConcurrentSameModel` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserBehaviorRequiresStrictlyLowerTargetRole` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateAppliesPlanAndRevokesOnlyTheTargetSession` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateNoOpAndAuthenticationPreconditions` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON/acl-empty-model` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON/acl-null-element` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON/acl-wrong-type` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON/array` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON/case-alias` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON/daily-limit-negative` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON/daily-limit-over-int32` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON/daily-limit-wrong-type` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON/duplicate-key` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON/empty-body` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON/over-64-kib` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON/plan-wrong-type` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON/status-wrong-type` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON/top-level-null` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON/trailing-json` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON/unknown-money` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON/unknown-permissions` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON/unknown-plan-enum` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON/unknown-role` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON/unknown-status-enum` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUsersHTTPHierarchy` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUsersReadHTTPExplicitDenyAllAdapters` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL fixtures`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUsersReadHTTPQueryAndVisibility` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL fixtures`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAuthProjectionHTTPFourSources/false` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL fixtures`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAuthProjectionHTTPFourSources/true` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL fixtures`
- `github.com/porsche/ai-gateway-go/internal/handler::TestAuthSessionHTTPFlow` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestGatewayChatAuthenticatesBeforeReadingOrValidatingBody` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestGatewayChatKeepsAuthenticatedRequestBodyLimit` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestGatewayChatProjectsValidatedCompletionAndMasksUpstreamFields` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestGatewayChatRejectsBeforeWhiteLabelUpstream` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestGatewayChatRejectsMalformedUpstreamCompletion` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestGatewayChatRequiresCurrentCatalogAndEnabledModelBeforeChat` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestGatewayChatRequiresExactJSONMediaTypeAndStableErrors` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestGatewayDetailRoutePreservesLegacyDetailModelID` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestGatewayMalformedOrDuplicateDetailQueryDoesNotCallUpstream` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestGatewayModelsUseTokenACLAndDynamicCatalog` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestGatewayOwnerACLAuthenticationUnavailableHTTPEnvelope` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL fixtures`
- `github.com/porsche/ai-gateway-go/internal/handler::TestGatewayOwnerACLManagedUpdateImmediatelyFiltersWarmHTTPMetadata` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL fixtures`
- `github.com/porsche/ai-gateway-go/internal/handler::TestGatewaySSEBeforeFirstPayloadReturnsJSONError` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestGatewaySSEMalformedFirstChunkReturnsJSON503` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestGatewaySSEPostFirstChunkEmitsErrorAndDone` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestGatewaySSEProjectsChunksAndDropsUpstreamFields` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestGatewaySlashModelDetailDoesNotCallUpstreamWhenTokenDenied` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestGatewaySlashModelDetailUsesQueryIDAndTokenACL` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDetailRoutePreservesLegacyDetailModelID` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/429` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/500` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/acl` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/assistant_db` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/cancel_connect` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/cancel_stream` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/conversation_db` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/conversation_missing` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/conversation_other_user` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/early_eof` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/malformed` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/message_db` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/normal` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/quota_db` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/quota_exhausted` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/timeout` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/title_db` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/usage_db` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/validation` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/write_delta` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/write_done` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticPipeline/write_meta` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticRequestIDAndRouteScope` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformDiagnosticSerializationFailureBeforeUpstream` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformMalformedOrDuplicateDetailQueryDoesNotCallUpstream` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformModelDetailHidesUnauthorizedAs404` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformModelsUseWhiteLabelCatalogAndUserACL` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformRejectsLegacyJWTWithoutSessionClaims` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformSlashModelDetailDoesNotCallUpstreamWhenUserDenied` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformSlashModelDetailUsesQueryIDAndUserACL` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestPlatformStreamFailureBeforeFirstFrameReturnsJSON` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/handler::TestRefreshReplayHTTPRejectsAfterCommittedRevocation` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/migration::TestAuthCoreMigrationOnIsolatedMySQL` — `TEST_DATABASE_URL is not set; isolated MySQL migration test skipped`
- `github.com/porsche/ai-gateway-go/internal/migration::TestPermissionMigrationRejectsPartialDDLAndLedgerDrift` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/migration::TestPermissionMigrationRejectsRecordedSchemaDriftAndForwardOnlyDown` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/migration::TestPermissionMigrationRepairsValidPartialDDLOnRerun` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/migration::TestPermissionSchemaMigratesAuthDataAndReruns` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/migration::TestPermissionSchemaRejectsCrossSchemaAndCompositeForeignKeys/composite` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/migration::TestPermissionSchemaRejectsCrossSchemaAndCompositeForeignKeys/cross_schema` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/migration::TestPermissionSchemaRejectsDrift/cascade_delete` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/migration::TestPermissionSchemaRejectsDrift/cascade_update` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/migration::TestPermissionSchemaRejectsDrift/default` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/migration::TestPermissionSchemaRejectsDrift/missing_fk` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/migration::TestPermissionSchemaRejectsDrift/missing_unique` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/migration::TestPermissionSchemaRejectsDrift/nullable` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/migration::TestPermissionSchemaRejectsDrift/unsigned` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/router::TestAnalyticsChartsRejectInvalidQueriesAfterAdminAuthorization` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/router::TestGatewayErrorDoesNotEchoSecretAndSanitizesRequestID` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/router::TestGatewayModelsAreFilteredByDatabaseToken` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/router::TestGatewayRejectsIPBeforeUpstreamAndHonorsTrustedProxy` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/router::TestGatewayRejectsSpoofedForwardedIPFromUntrustedPeer` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/router::TestGatewayRejectsTokenModelBeforeUpstream` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/router::TestGatewayTokenJWTCRUDScopesOwnerAndNeverReturnsPlaintextAgain` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/router::TestGatewayTokenManagementRejectsLegacyJWTWithoutSessionClaims` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/router::TestHealthOK` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/router::TestHostAllowlistAcceptsDomainAndRejectsDirectIPAddress` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBCorruptPolicy/capability` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBCorruptPolicy/catalog` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBCorruptPolicy/count` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBCorruptPolicy/effect` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBCorruptPolicy/head_version` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBCorruptPolicy/orphan` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBCorruptPolicy/row_version` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBCorruptPolicy/tombstone` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBCorruptPolicy/ungrantable` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBFreshActorSessionAndRedis/actor_deleted` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBFreshActorSessionAndRedis/actor_disabled` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBFreshActorSessionAndRedis/actor_missing` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBFreshActorSessionAndRedis/actor_role_corrupt` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBFreshActorSessionAndRedis/actor_stale` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBFreshActorSessionAndRedis/actor_status_corrupt` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBFreshActorSessionAndRedis/actor_version_corrupt` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBFreshActorSessionAndRedis/hidden_target_and_redis_revoked` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBFreshActorSessionAndRedis/redis_error` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBFreshActorSessionAndRedis/redis_revoked` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBFreshActorSessionAndRedis/role_denied_and_redis_revoked` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBFreshActorSessionAndRedis/session_deleted` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBFreshActorSessionAndRedis/session_expired` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBFreshActorSessionAndRedis/session_foreign` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBFreshActorSessionAndRedis/session_missing` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBFreshActorSessionAndRedis/session_revoked` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBFreshActorSessionAndRedis/session_stale` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBFreshActorSessionAndRedis/session_version_corrupt` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBFreshActorSessionAndRedis/target_role_corrupt` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBFreshActorSessionAndRedis/target_status_corrupt` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBFreshActorSessionAndRedis/target_version_corrupt` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBLockOrderAndFreshExpiry` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBReadPolicyAndRoleMatrix` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBRejectsExternalTransactionAndCommitFailure` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminPermissionDBUserWriterSerializesRead` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminUsersReadDBActorPolicyWriterSerializes` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminUsersReadDBCommitFailureAndOuterTransaction/false` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminUsersReadDBCommitFailureAndOuterTransaction/true` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminUsersReadDBDeletedAndPolicy/allow_deleted` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminUsersReadDBDeletedAndPolicy/baseline` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminUsersReadDBDeletedAndPolicy/corrupt` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminUsersReadDBDeletedAndPolicy/deny_read` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminUsersReadDBDeletedAndPolicy/ordinary` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminUsersReadDBListAndDetail` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAdminUsersReadDBStableNullOrderingAcrossPages` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAnalyticsChartBuildsAllApprovedViewsFromFilteredUsage` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestAnalyticsExportCSVEscapesFormulaLikeModelNames` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestAuthProjectionDBAdminOverridesAndRefreshProof` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAuthProjectionDBFreshnessAndOmission/actor_av` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAuthProjectionDBFreshnessAndOmission/actor_deleted` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAuthProjectionDBFreshnessAndOmission/actor_disabled` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAuthProjectionDBFreshnessAndOmission/baseline` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAuthProjectionDBFreshnessAndOmission/empty_head` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAuthProjectionDBFreshnessAndOmission/illegal_root_rule` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAuthProjectionDBFreshnessAndOmission/orphan` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAuthProjectionDBFreshnessAndOmission/redis_error` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAuthProjectionDBFreshnessAndOmission/session_deleted` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAuthProjectionDBFreshnessAndOmission/session_expired` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAuthProjectionDBFreshnessAndOmission/session_revoked` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAuthProjectionDBFreshnessAndOmission/session_sv` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAuthProjectionDBFreshnessAndOmission/unknown_status` — `requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed`
- `github.com/porsche/ai-gateway-go/internal/service::TestAuthRedisGenerationNeverReturnsStaleRotationResult` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestAuthRedisPendingRotationCanBeRecoveredAfterPublishFailure` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestAuthSessionCreateEvictsOldestAt51` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestChangePasswordRehashesCredentialsRevokesSessionsAndAudits` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestChangePasswordRejectsIncorrectOldPasswordWithoutMutation` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestChangePasswordRejectsRedisDenyFailureWithoutMutation` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestChangePasswordRollsBackMySQLWhenPasswordAuditWriteFails` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestComparePendingModelErrorWriteFailureStopsFurtherFrames` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestCompareWhiteLabelStreamsKeepsOtherModelsRunningAfterOneFails` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestCurrentUserWritesRejectAccountsChangedAfterAuthentication/identity_verification/disabled` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestCurrentUserWritesRejectAccountsChangedAfterAuthentication/identity_verification/soft_deleted` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestCurrentUserWritesRejectAccountsChangedAfterAuthentication/password/disabled` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestCurrentUserWritesRejectAccountsChangedAfterAuthentication/password/soft_deleted` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestCurrentUserWritesRejectAccountsChangedAfterAuthentication/profile/disabled` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestCurrentUserWritesRejectAccountsChangedAfterAuthentication/profile/soft_deleted` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestDisableUserRejectsActorDisabledAfterAuthorization` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestGatewayTokenAuthenticationStorageFailuresFailClosed/last_used_write` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestGatewayTokenAuthenticationStorageFailuresFailClosed/owner_read` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestGatewayTokenAuthenticationStorageFailuresFailClosed/token_read` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestGatewayTokenCreateAndAuthenticate` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestGatewayTokenDeletedOwnerIsDisabled` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestGatewayTokenOwnerACLChangeAppliesToNextAuthentication` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestGatewayTokenPersistedACLShapesFailClosed` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestGatewayTokenRejectsDisabledOwner` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestGatewayTokenRejectsExpiredAndRevoked` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestLoginRateLimitRejectsFifthLoginFailure` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestLoginUsernameRejectsDisabledAndSoftDeletedUser` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPayOrderConcurrentSettlement` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPermissionBaselineAndPersistedSnapshot` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPermissionCommitFailureReturnsNilSnapshot` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPermissionCorruptAndOrphanStatesFailClosed/bad_catalog` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPermissionCorruptAndOrphanStatesFailClosed/bad_count` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPermissionCorruptAndOrphanStatesFailClosed/bad_rule_version` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPermissionCorruptAndOrphanStatesFailClosed/bad_version` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPermissionCorruptAndOrphanStatesFailClosed/deleted_head` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPermissionCorruptAndOrphanStatesFailClosed/root_only` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPermissionCorruptAndOrphanStatesFailClosed/unavailable` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPermissionCorruptAndOrphanStatesFailClosed/unknown_capability` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPermissionCorruptAndOrphanStatesFailClosed/unknown_effect` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPermissionDualRootWriterLockSerializesSnapshot/commit` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPermissionDualRootWriterLockSerializesSnapshot/rollback` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPermissionEmptyHeadAndDeletedHistory` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPermissionRejectsExternalTransaction` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPermissionRejectsInactiveActorsAndCancelledRead/deleted` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPermissionRejectsInactiveActorsAndCancelledRead/disabled` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPermissionRejectsInactiveActorsAndCancelledRead/unknown_role` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPermissionRejectsInactiveActorsAndCancelledRead/zero_auth_version` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPermissionRejectsNilAndClosedRoots` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestPermissionWriterLockPreventsMixedSnapshot` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestRefreshReplayAuditFailureRollsBackMySQLAndRetainsRedisBarrier` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestRefreshRotationConcurrentOldBReturnsC` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestRefreshRotationConcurrentRequestsReuseOneResult` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestRefreshRotationReplayOutsideWindowRevokesSession` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestRootBootstrapCreatesOnlyTheFirstRoot` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestRootBootstrapDoesNotReplaceTombstonedRoot` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestRootTestMySQLIsolatesFixturesAndPreservesParent` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserConcurrentSamePlanCommitsOneSecurityTransition` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserPlanChangeRevokesExistingSession` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserRejectsActorChangedAfterAuthentication/disabled` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserRejectsActorChangedAfterAuthentication/downgraded` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserRejectsAuthVersionOverflowBeforeMutation` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserRejectsInvalidTypedInputWithoutMutation/empty_ACL_value` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserRejectsInvalidTypedInputWithoutMutation/negative_limit` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserRejectsInvalidTypedInputWithoutMutation/plan` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserRejectsInvalidTypedInputWithoutMutation/status` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserRejectsRedisFailureWithoutSQLMutation` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserRoleAndAuthVersionGuards/actor_auth_version_zero` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserRoleAndAuthVersionGuards/disabled_actor` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserRoleAndAuthVersionGuards/equal_role` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserRoleAndAuthVersionGuards/ordinary_actor` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserRoleAndAuthVersionGuards/root_manages_admin` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserRoleAndAuthVersionGuards/root_target` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserRoleAndAuthVersionGuards/self` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserRoleAndAuthVersionGuards/target_auth_version_zero` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserRoleAndAuthVersionGuards/unknown_actor_role` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserRoleAndAuthVersionGuards/unknown_target_role` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserRollsBackSQLAndAuditWriteFailures/event-insert` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserRollsBackSQLAndAuditWriteFailures/user-update` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserRollsBackWhenCommitFails` — `requires isolated TEST_DATABASE_URL MySQL fixture`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserSecurityChangeWithNoSessionStillAudits` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserSemanticACLNoopReturnsStoredRepresentation` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserStatusMixedAndDailyLimitTransitions/daily_limit_only` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserStatusMixedAndDailyLimitTransitions/mixed` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUpdateManagedUserStatusMixedAndDailyLimitTransitions/status` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`
- `github.com/porsche/ai-gateway-go/internal/service::TestUsernameRegistrationPermanentlyReservesTrimmedUsername` — `requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped`

### Authorization-gated performance NOT_RUN — 1 leaf test

- `github.com/porsche/ai-gateway-go/internal/handler::TestAdminUsersReadPerformance` — `NOT_RUN: opt-in 100k synthetic-user performance fixture requires this batch authorization`

The 100k performance test remains NOT_RUN and requires separate batch authorization; it is a known validation risk outside this no-fixture baseline and is not evidence of performance acceptance.

## Sandbox classification

- First failure: environment-only cache permission denial, reported as `operation not permitted`; no product test ran and no tracked file changed.
- Second failure: environment-only loopback denial, reported as `listen tcp6 [::1]:0: bind: operation not permitted` in existing `httptest` callers.
- Approved escalation reran the same initialization and full test selections successfully. These failures are sandbox restrictions, not product failures.
- No service, fixture, container, production operation, model call, or SSE call was started.

## Git status

- Before initialization: empty `git status --short`.
- After initialization and all gates: empty `git status --short` before creating this report.
