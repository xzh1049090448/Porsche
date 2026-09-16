# Compare History Grouping Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend authenticated conversation detail responses with validated compare-generation grouping metadata so Porsche-Web can reconstruct one aggregate historical reply after re-login.

**Architecture:** Add a service-owned conversation detail projection that loads the existing flat messages plus compare receipt/result rows and validates every reference before exposing GUID-only grouping metadata. Keep database persistence unchanged, serialize the projection through a dedicated DTO function, and use it only for GET/PUT detail responses.

**Tech Stack:** Go 1.22, Gin, GORM, MySQL 8, `go test`, JSON contracts.

---

### Task 1: Pure compare-group projection

**Files:**
- Create: `internal/service/conversation_detail.go`
- Create: `internal/service/conversation_detail_test.go`

- [ ] **Step 1: Write the failing pure projection tests**

Create table-driven tests that build one `models.Conversation`, two valid compare receipts, their ordered results, and the referenced messages without opening a database. The assertions must cover ordered completed/failed results, single-receipt exclusion, duplicate assistant rejection, cross-conversation rejection, and no internal IDs in the public structs.

```go
func TestBuildConversationGenerationGroupsPreservesCompareOrder(t *testing.T) {
	fixture := conversationDetailProjectionFixture()
	groups, omitted := buildConversationGenerationGroups(
		fixture.owner.ID,
		&fixture.conversation,
		fixture.receipts,
		fixture.results,
	)
	if omitted != 0 || len(groups) != 1 {
		t.Fatalf("groups=%#v omitted=%d", groups, omitted)
	}
	group := groups[0]
	if group.GenerationID != fixture.receipts[0].GenerationID || group.Mode != "compare" ||
		group.UserMessageGUID != strconv.FormatInt(fixture.userMessage.Guid, 10) {
		t.Fatalf("unexpected group: %#v", group)
	}
	if got := []string{group.Results[0].Model, group.Results[1].Model}; !reflect.DeepEqual(got, []string{"model-a", "model-b"}) {
		t.Fatalf("order=%v", got)
	}
	if group.Results[0].Status != "completed" || group.Results[0].AssistantMessageGUID == nil ||
		group.Results[1].Status != "failed" || group.Results[1].AssistantMessageGUID != nil || group.Results[1].ErrorCode == nil {
		t.Fatalf("results=%#v", group.Results)
	}
}
```

- [ ] **Step 2: Run the focused test and confirm RED**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service -run 'TestBuildConversationGenerationGroups' -count=1`

Expected: FAIL because `buildConversationGenerationGroups` and projection types do not exist.

- [ ] **Step 3: Add the projection types and validator**

Define the public read model with no database IDs:

```go
type ConversationGenerationResult struct {
	Model                string
	Status               string
	AssistantMessageGUID *string
	Tokens               int64
	ErrorCode            *string
}

type ConversationGenerationGroup struct {
	GenerationID   string
	Mode           string
	UserMessageGUID string
	Results        []ConversationGenerationResult
}

type ConversationDetail struct {
	Conversation                   *models.Conversation
	GenerationGroups               []ConversationGenerationGroup
	OmittedGenerationGroupCount    int
}
```

Implement `buildConversationGenerationGroups(userID int64, conv *models.Conversation, receipts []models.PlatformChatGenerationReceipt, rows []models.PlatformChatGenerationResult) ([]ConversationGenerationGroup, int)`. Reuse the existing `validPlatformGenerationReceiptParent`, `validPlatformGenerationReceiptUserMessage`, `validPlatformGenerationReceiptResultSet`, `validPlatformGenerationReceiptCardinality`, and `validPlatformGenerationReceiptAssistantMessage` functions. Add explicit current-conversation checks, reject reused user/assistant message IDs across groups, exclude single receipts, and return the count of omitted compare groups.

- [ ] **Step 4: Run the focused tests and confirm GREEN**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service -run 'TestBuildConversationGenerationGroups' -count=1`

Expected: PASS with every pure projection case executed and no database fixture skips.

- [ ] **Step 5: Commit the pure projection**

```bash
git add internal/service/conversation_detail.go internal/service/conversation_detail_test.go
git commit -m "feat: project compare generation groups"
```

### Task 2: Owned database detail loader

**Files:**
- Modify: `internal/service/conversation.go`
- Modify: `internal/service/conversation_detail.go`
- Create: `internal/service/conversation_detail_db_test.go`

- [ ] **Step 1: Write the isolated MySQL loader tests**

Use the existing `TEST_DATABASE_URL` safety checks and migrations. Seed two users, two conversations, one compare receipt with a completed and failed result, and one foreign receipt. Assert that only the current user's current-conversation group is returned, raw messages remain present, and database failures propagate.

```go
func TestGetConversationDetailLoadsOnlyOwnedCompareGroups(t *testing.T) {
	fixture := seedConversationDetailMySQL(t)
	detail, err := GetConversationDetail(context.Background(), fixture.db, &fixture.owner, fixture.conversation.Guid)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Conversation.Guid != fixture.conversation.Guid || len(detail.Conversation.Messages) != 2 {
		t.Fatalf("conversation=%#v", detail.Conversation)
	}
	if len(detail.GenerationGroups) != 1 || detail.GenerationGroups[0].GenerationID != fixture.generationID {
		t.Fatalf("groups=%#v", detail.GenerationGroups)
	}
}
```

- [ ] **Step 2: Run the database test and confirm RED or explicit fixture skip**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service -run 'TestGetConversationDetail' -count=1`

Expected with `TEST_DATABASE_URL`: FAIL because the loader does not exist. Expected without it: one explicit `BLOCKED_FIXTURE` skip while Task 1 pure tests remain GREEN.

- [ ] **Step 3: Implement `GetConversationDetail` and stable message ordering**

Change the message preload order in `GetConversation` to:

```go
return tx.Where("is_deleted = 0").Order("created_at asc, id asc")
```

Implement:

```go
func GetConversationDetail(ctx context.Context, db *gorm.DB, user *models.User, guid int64) (*ConversationDetail, error) {
	if ctx == nil || db == nil || user == nil || user.ID <= 0 {
		return nil, errUnavailable("会话详情不可用")
	}
	conv, err := GetConversation(db.WithContext(ctx), user, guid, true)
	if err != nil {
		return nil, err
	}
	var receipts []models.PlatformChatGenerationReceipt
	if err := db.WithContext(ctx).Where(
		"user_id = ? AND conversation_id = ? AND mode = ? AND is_deleted = 0",
		user.ID, conv.ID, models.PlatformGenerationReceiptModeCompare,
	).Order("committed_at asc, id asc").Find(&receipts).Error; err != nil {
		return nil, errUnavailable("会话详情不可用")
	}
	rows := make([]models.PlatformChatGenerationResult, 0)
	if len(receipts) > 0 {
		ids := make([]int64, len(receipts))
		for index := range receipts { ids[index] = receipts[index].ID }
		if err := db.WithContext(ctx).Where("receipt_id IN ?", ids).Order("receipt_id asc, model_index asc").Find(&rows).Error; err != nil {
			return nil, errUnavailable("会话详情不可用")
		}
	}
	groups, omitted := buildConversationGenerationGroups(user.ID, conv, receipts, rows)
	return &ConversationDetail{Conversation: conv, GenerationGroups: groups, OmittedGenerationGroupCount: omitted}, nil
}
```

Wrap receipt/result query failures with `errUnavailable("会话详情不可用")`; do not return SQL text.

- [ ] **Step 4: Run focused service tests**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service -run 'Test(BuildConversationGenerationGroups|GetConversationDetail)' -count=1`

Expected: pure tests PASS; isolated database tests PASS when the explicit fixture is present and otherwise report only their declared fixture skip.

- [ ] **Step 5: Commit the loader**

```bash
git add internal/service/conversation.go internal/service/conversation_detail.go internal/service/conversation_detail_db_test.go
git commit -m "feat: load compare groups with conversation detail"
```

### Task 3: Detail DTO and authenticated handlers

**Files:**
- Modify: `internal/dto/serializers.go`
- Create: `internal/dto/conversation_detail_test.go`
- Modify: `internal/handler/conversations_datasets.go`
- Create: `internal/handler/conversation_detail_contract_test.go`

- [ ] **Step 1: Write failing DTO and route contract tests**

Assert exact snake-case output, nil fields encoded as JSON null, no internal IDs, GET and PUT use `GetConversationDetail`, and list/create/export remain on their existing paths.

```go
func TestConversationDetailSerializesGenerationGroupsWithoutInternalIDs(t *testing.T) {
	assistant := "353589505447432194"
	detail := &service.ConversationDetail{
		Conversation: &models.Conversation{AuditFields: models.AuditFields{Guid: 353589505447432192}},
		GenerationGroups: []service.ConversationGenerationGroup{{
			GenerationID: "01234567-89ab-4cde-8f01-23456789abcd", Mode: "compare", UserMessageGUID: "353589505447432193",
			Results: []service.ConversationGenerationResult{{Model: "model-a", Status: "completed", AssistantMessageGUID: &assistant, Tokens: 32}},
		}},
	}
	out := ConversationDetail(detail)
	encoded, err := json.Marshal(out)
	if err != nil { t.Fatal(err) }
	if bytes.Contains(encoded, []byte(`"id"`)) || !bytes.Contains(encoded, []byte(`"generation_groups"`)) {
		t.Fatalf("body=%s", encoded)
	}
}
```

- [ ] **Step 2: Run focused DTO/handler tests and confirm RED**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/dto ./internal/handler -run 'TestConversationDetail' -count=1`

Expected: FAIL because the detail serializer and handler wiring do not exist.

- [ ] **Step 3: Add the serializer and wire GET/PUT**

Add `dto.ConversationDetail(detail *service.ConversationDetail) map[string]interface{}`. Start with `Conversation(detail.Conversation, true)`, always add a non-nil `generation_groups` array, serialize results in service order, and use explicit `nil` values for absent assistant GUID/error code.

In GET, call:

```go
detail, err := service.GetConversationDetail(c.Request.Context(), state.DB, user, int64(id))
c.JSON(http.StatusOK, dto.ConversationDetail(detail))
```

In PUT, apply the title mutation first when present, then reload through `GetConversationDetail` and return the same DTO. Log only `omitted_generation_group_count`, request ID, and the public conversation GUID when the count is nonzero; never log message content, model replies, credentials, or internal IDs.

- [ ] **Step 4: Run focused DTO/handler tests and confirm GREEN**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/dto ./internal/handler -run 'TestConversationDetail' -count=1`

Expected: PASS and exact GET/PUT detail shape assertions succeed.

- [ ] **Step 5: Commit the transport change**

```bash
git add internal/dto/serializers.go internal/dto/conversation_detail_test.go internal/handler/conversations_datasets.go internal/handler/conversation_detail_contract_test.go
git commit -m "feat: expose compare groups in conversation detail"
```

### Task 4: Versioned cross-repository contract

**Files:**
- Create: `docs/agents/contracts/platform-compare-history-grouping-v1.json`

- [ ] **Step 1: Add the exact backend contract artifact**

Create valid JSON with `version` equal to `platform-compare-history-grouping.v1`, `status` equal to `agreed_for_implementation`, endpoint `GET|PUT /api/v1/conversations/{guid}`, and the exact `generation_groups` schema from the approved design. Encode these invariants as data: compare-only groups, request order, decimal-string GUIDs, completed/failed field combinations, omission of corrupt groups, raw messages preserved, and no database IDs.

Use this complete canonical content:

```json
{
  "version": "platform-compare-history-grouping.v1",
  "status": "agreed_for_implementation",
  "endpoints": [
    "GET /api/v1/conversations/{guid}",
    "PUT /api/v1/conversations/{guid}"
  ],
  "response_extension": {
    "field": "generation_groups",
    "detail_responses_only": true,
    "list_and_create_unchanged": true,
    "value_when_empty": []
  },
  "generation_group": {
    "mode": "compare",
    "generation_id": "canonical_lowercase_uuid",
    "user_message_guid": "positive_decimal_string_int64",
    "results_order": "model_index_ascending",
    "result_count": { "minimum": 2, "maximum": 3 },
    "models_unique": true,
    "completed": {
      "assistant_message_guid": "positive_decimal_string_int64",
      "tokens": "nonnegative_integer",
      "error_code": null
    },
    "failed": {
      "assistant_message_guid": null,
      "tokens": 0,
      "error_code": "stable_lower_snake_case"
    }
  },
  "integrity": {
    "ownership": "authenticated_user_and_current_conversation",
    "invalid_group": "omit_group_and_preserve_flat_messages",
    "database_error": "fail_detail_request",
    "database_ids_exposed": false
  },
  "compatibility": {
    "old_backend": "frontend_preserves_flat_messages",
    "old_frontend": "ignores_generation_groups",
    "legacy_multi_model_marker": "preserved",
    "database_migration_required": false
  },
  "acceptance": {
    "production": "pending",
    "required": "create_compare_logout_login_reopen_one_aggregate_reply"
  }
}
```

- [ ] **Step 2: Validate the contract**

Run:

```bash
python3 -m json.tool docs/agents/contracts/platform-compare-history-grouping-v1.json >/dev/null
rg -n 'generation_groups|assistant_message_guid|user_message_guid|agreed_for_implementation' docs/agents/contracts/platform-compare-history-grouping-v1.json
git diff --check
```

Expected: JSON validation succeeds, all four contract terms are present, and diff check is clean.

- [ ] **Step 3: Commit the backend contract copy**

```bash
git add docs/agents/contracts/platform-compare-history-grouping-v1.json
git commit -m "docs: define compare history grouping contract"
```

### Task 5: Backend verification and tracker evidence

**Files:**
- Modify: `feature_list.json`
- Modify: `progress.md`

- [ ] **Step 1: Run focused, full, race, vet, build, and format gates**

Run:

```bash
gofmt -w internal/service/conversation.go internal/service/conversation_detail.go internal/service/conversation_detail_test.go internal/service/conversation_detail_db_test.go internal/dto/serializers.go internal/dto/conversation_detail_test.go internal/handler/conversations_datasets.go internal/handler/conversation_detail_contract_test.go
GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service ./internal/dto ./internal/handler -count=1
GOCACHE=/private/tmp/porsche-go-build-cache go test -race ./internal/service ./internal/dto ./internal/handler -count=1
GOCACHE=/private/tmp/porsche-go-build-cache go test ./... -count=1
GOCACHE=/private/tmp/porsche-go-build-cache go vet ./...
GOCACHE=/private/tmp/porsche-go-build-cache go build ./...
git diff --check
```

Expected: all non-fixture tests PASS, race/vet/build/diff are clean, and any missing MySQL fixture is reported separately rather than counted as executed acceptance.

- [ ] **Step 2: Run the explicit disposable MySQL tests**

Provide only a validated disposable `TEST_DATABASE_URL` ending in `_test`, run `go test ./internal/service -run 'TestGetConversationDetail' -count=1`, and capture zero-skip output. Do not read `.env` or connect to production.

- [ ] **Step 3: Record exact evidence without changing the release boundary**

Append the commands, counts, skips, contract hash, backend revision, and remaining frontend/browser acceptance to `progress.md`. Add evidence to `go-018` in `feature_list.json`; keep its release status unchanged until cross-repository and environment acceptance complete.

- [ ] **Step 4: Validate trackers and commit evidence**

```bash
python3 -m json.tool feature_list.json >/dev/null
git diff --check
git add feature_list.json progress.md
git commit -m "docs: record compare history backend verification"
```
