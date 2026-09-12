# OpenAI CLI Tool Compatibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Completed steps use checked boxes for tracking.

**Goal:** Add secure OpenAI-compatible Chat Completions tool round-tripping and a stateless Responses API adapter for text and custom function tools.

**Architecture:** Both public endpoints decode into `internal/openaicompat.Conversation`, run the same call-correlation validator, and encode a fresh allowlisted Chat Completions request for the existing fixed WhiteLabel upstream. Existing WhiteLabel response projection remains the trust boundary; Chat reuses its public projection while Responses converts only the projected structs into Responses objects and SSE events.

**Tech Stack:** Go 1.24, Gin, `encoding/json`, existing `internal/whitelabel`, `httptest`, table-driven Go tests.

**Execution status (2026-09-12):** Tasks 1-8 and the local Task 9 gates are complete. The first independent spec review of integrated revision `28d8f1f` returned `SPEC_FAIL` for four request-boundary and cancellation-test gaps. Fix `1df776d` closes those findings; focused/full/race/vet/build/diff pass, and seven real handler tests on isolated MySQL 8.0.46 pass under race with zero skips. A new review snapshot and ordered spec/security/test re-review are pending. Real OpenCode/Codex/upstream acceptance is not run. See `docs/superpowers/reports/2026-09-12-openai-cli-tool-compat.md`.

---

## File map

- Create `internal/openaicompat/types.go`: normalized request, message, tool, option, and package error types.
- Create `internal/openaicompat/decode.go`: strict JSON helpers and shared scalar/content validators.
- Create `internal/openaicompat/chat_request.go`: Chat Completions DTO decoding and normalization.
- Create `internal/openaicompat/responses_request.go`: Responses DTO decoding and normalization.
- Create `internal/openaicompat/validate.go`: shared call-ID state machine and resource limits.
- Create `internal/openaicompat/upstream.go`: fresh allowlisted upstream Chat Completions encoder.
- Create `internal/openaicompat/responses_response.go`: non-streaming Responses projection and public IDs.
- Create `internal/openaicompat/responses_stream.go`: Responses SSE state machine over projected WhiteLabel chunks.
- Create `internal/openaicompat/types_test.go`, `chat_request_test.go`, `validate_test.go`, `responses_request_test.go`, `upstream_test.go`, `responses_response_test.go`, and `responses_stream_test.go`: unit, boundary, equivalence, and SSE ordering tests.
- Modify `internal/whitelabel/types.go` and `types_test.go`: allow null assistant content only alongside valid function calls.
- Modify `internal/handler/gateway_tokens.go`: common Gateway request/auth/catalog execution, Chat decoder, and `/v1/responses` route.
- Modify `internal/handler/gateway_whitelabel_test.go`: full HTTP two-round loops, zero-upstream rejection, cancellation, and leakage tests.
- Modify `internal/router/router_test.go`: `/v1/responses` route registration contract.
- Add `internal/handler/testdata/opencode-chat-request.json` and `internal/handler/testdata/opencode-responses-request.json`: frozen sanitized OpenCode request fixtures.

### Task 1: Normalized protocol types and stable errors

**Files:**
- Create: `internal/openaicompat/types.go`
- Test: `internal/openaicompat/types_test.go`

- [x] **Step 1: Write the failing type and error contract test**

```go
func TestPublicErrorClassifications(t *testing.T) {
    cases := []struct {
        err  *Error
        code string
        want int
    }{
        {InvalidRequest(), "invalid_request", 400},
        {UnsupportedParameter(), "unsupported_parameter", 400},
        {RequestTooLarge(), "request_too_large", 413},
    }
    for _, tc := range cases {
        if tc.err.Code != tc.code || tc.err.Status != tc.want {
            t.Fatalf("error=%#v", tc.err)
        }
    }
}
```

- [x] **Step 2: Run the focused test and verify RED**

Run: `GOCACHE=/private/tmp/porsche-openai-cli-go-cache go test ./internal/openaicompat -run TestPublicErrorClassifications -count=1`

Expected: FAIL because package/type constructors do not exist.

- [x] **Step 3: Add the normalized types and constructors**

```go
type Conversation struct {
    Model             string
    Instructions      []Message
    Messages          []Message
    Tools             []ToolDefinition
    ToolChoice        ToolChoice
    ParallelToolCalls *bool
    MaxOutputTokens   *int64
    Temperature       *float64
    TopP              *float64
    FrequencyPenalty  *float64
    PresencePenalty   *float64
    Stop              any
    Seed              *int64
    N                 *int64
    ResponseFormat    any
    IncludeUsage      bool
    Stream            bool
}

type Message struct {
    Role       Role
    Content    any
    ToolCalls  []ToolCall
    ToolCallID string
}

type ToolCall struct { ID, Name, Arguments string }
type ToolDefinition struct {
    Name, Description string
    Parameters        json.RawMessage
    Strict            *bool
}
type ToolChoice struct { Mode, Name string }
type Error struct { Code string; Status int }
```

Also define the constants from the design: 12 MiB body, 128 messages, 32 tools, 64 parallel calls, 256 KiB arguments, 1 MiB tool output, and 128-byte call IDs.

- [x] **Step 4: Run the package test and verify GREEN**

Run: `GOCACHE=/private/tmp/porsche-openai-cli-go-cache go test ./internal/openaicompat -count=1`

Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add internal/openaicompat/types.go internal/openaicompat/types_test.go
git commit -m "feat: add normalized OpenAI protocol types"
```

### Task 2: Chat request decoder and call-correlation validator

**Files:**
- Create: `internal/openaicompat/decode.go`
- Create: `internal/openaicompat/chat_request.go`
- Create: `internal/openaicompat/validate.go`
- Test: `internal/openaicompat/chat_request_test.go`
- Test: `internal/openaicompat/validate_test.go`

- [x] **Step 1: Write failing Chat normalization tests**

```go
func TestDecodeChatNormalizesToolRoundTrip(t *testing.T) {
    body := []byte(`{"model":"model-a","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"not-json"}}]},{"role":"tool","tool_call_id":"call_1","content":"result"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`)
    got, err := DecodeChat(body)
    if err != nil || got.Messages[0].ToolCalls[0].Arguments != "not-json" || got.MaxOutputTokens != nil {
        t.Fatalf("conversation=%#v err=%#v", got, err)
    }
}

func TestDecodeChatRejectsBrokenCallSequences(t *testing.T) {
    inputs := []string{
        `{"model":"m","messages":[{"role":"tool","tool_call_id":"missing","content":"x"}]}`,
        `{"model":"m","messages":[{"role":"assistant","tool_calls":[{"id":"same","type":"function","function":{"name":"a","arguments":"{}"}},{"id":"same","type":"function","function":{"name":"b","arguments":"{}"}}]}]}`,
        `{"model":"m","messages":[{"role":"assistant","tool_calls":[{"id":"open","type":"function","function":{"name":"a","arguments":"{}"}}]},{"role":"user","content":"continue"}]}`,
    }
    for _, body := range inputs {
        if _, err := DecodeChat([]byte(body)); err == nil || err.Code != "invalid_request" {
            t.Fatalf("accepted %s", body)
        }
    }
}
```

- [x] **Step 2: Verify RED**

Run: `GOCACHE=/private/tmp/porsche-openai-cli-go-cache go test ./internal/openaicompat -run 'TestDecodeChat|TestValidateCall' -count=1`

Expected: FAIL because `DecodeChat` and the validator are missing.

- [x] **Step 3: Implement strict decoding and validation**

Implement:

```go
func DecodeChat(body []byte) (Conversation, *Error)
func decodeStrict(raw []byte, dst any) error
func validateConversation(c Conversation) *Error
func validCallID(id string) bool
```

Use explicit DTOs with `json.Decoder.DisallowUnknownFields()` and `UseNumber()`. Preserve existing safe text/media content shapes, accept `developer`, `assistant.tool_calls`, and `tool.tool_call_id`, enforce mutually exclusive `max_tokens`/`max_completion_tokens`, and reject unclosed/duplicate/out-of-order call IDs. Tool arguments remain bounded raw UTF-8 strings without JSON parsing.

- [x] **Step 4: Run all new decoder/state-machine tests and verify GREEN**

Run: `GOCACHE=/private/tmp/porsche-openai-cli-go-cache go test ./internal/openaicompat -count=1`

Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add internal/openaicompat/decode.go internal/openaicompat/chat_request.go internal/openaicompat/validate.go internal/openaicompat/chat_request_test.go internal/openaicompat/validate_test.go
git commit -m "feat: decode OpenAI chat tool conversations"
```

### Task 3: Responses request decoder and protocol equivalence

**Files:**
- Create: `internal/openaicompat/responses_request.go`
- Test: `internal/openaicompat/responses_request_test.go`

- [x] **Step 1: Write failing Responses normalization tests**

```go
func TestDecodeResponsesMatchesChatConversation(t *testing.T) {
    responseBody := []byte(`{"model":"model-a","instructions":"be concise","input":[{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"ok"}],"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}],"store":false}`)
    got, err := DecodeResponses(responseBody)
    if err != nil || got.Messages[0].ToolCalls[0].ID != "call_1" || got.Messages[1].ToolCallID != "call_1" {
        t.Fatalf("conversation=%#v err=%#v", got, err)
    }
}

func TestDecodeResponsesRejectsStatefulOptionsBeforeExecution(t *testing.T) {
    for _, body := range []string{
        `{"model":"m","input":"x","store":true}`,
        `{"model":"m","input":"x","previous_response_id":"resp_1"}`,
        `{"model":"m","input":"x","tools":[{"type":"web_search"}]}`,
    } {
        if _, err := DecodeResponses([]byte(body)); err == nil || err.Code != "unsupported_parameter" {
            t.Fatalf("accepted %s", body)
        }
    }
}
```

- [x] **Step 2: Verify RED**

Run: `GOCACHE=/private/tmp/porsche-openai-cli-go-cache go test ./internal/openaicompat -run TestDecodeResponses -count=1`

Expected: FAIL because `DecodeResponses` does not exist.

- [x] **Step 3: Implement Responses DTO normalization**

Implement `func DecodeResponses(body []byte) (Conversation, *Error)` for string input, text message items, `function_call`, `function_call_output`, flat function definitions, null/absent previous response ID, and false/absent store. Validate optional item `id` and `status`, discard them after validation, and set omitted `parallel_tool_calls` to true.

- [x] **Step 4: Verify GREEN and equivalence**

Run: `GOCACHE=/private/tmp/porsche-openai-cli-go-cache go test ./internal/openaicompat -run 'TestDecodeResponses|TestChatAndResponsesEquivalent' -count=1`

Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add internal/openaicompat/responses_request.go internal/openaicompat/responses_request_test.go
git commit -m "feat: decode stateless Responses requests"
```

### Task 4: Allowlisted upstream encoder

**Files:**
- Create: `internal/openaicompat/upstream.go`
- Test: `internal/openaicompat/upstream_test.go`

- [x] **Step 1: Write a failing exact-projection test**

```go
func TestEncodeUpstreamProjectsOnlyNormalizedFields(t *testing.T) {
    parallel := true
    c := Conversation{Model: "model-a", ParallelToolCalls: &parallel, Messages: []Message{
        {Role: RoleDeveloper, Content: "rules"},
        {Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1", Name: "read_file", Arguments: "{}"}}},
        {Role: RoleTool, ToolCallID: "call_1", Content: "secret-result"},
    }}
    body, err := EncodeUpstream(c)
    if err != nil || !bytes.Contains(body, []byte(`"role":"system"`)) || !bytes.Contains(body, []byte(`"tool_call_id":"call_1"`)) {
        t.Fatalf("body=%s err=%v", body, err)
    }
}
```

- [x] **Step 2: Verify RED**

Run: `GOCACHE=/private/tmp/porsche-openai-cli-go-cache go test ./internal/openaicompat -run TestEncodeUpstream -count=1`

Expected: FAIL because `EncodeUpstream` is missing.

- [x] **Step 3: Implement fresh JSON encoding**

Implement `func EncodeUpstream(Conversation) ([]byte, error)` using private DTO structs. Convert developer to system, Responses calls to assistant tool calls, outputs to tool messages, flat tools to nested tools, and max output tokens to `max_tokens`. Never merge client raw JSON and never encode Gateway credentials, item IDs, store, or previous response ID.

- [x] **Step 4: Verify GREEN including sensitive sentinel absence**

Run: `GOCACHE=/private/tmp/porsche-openai-cli-go-cache go test ./internal/openaicompat -run 'TestEncodeUpstream' -count=1`

Expected: PASS, including exact JSON comparison and absence checks.

- [x] **Step 5: Commit**

```bash
git add internal/openaicompat/upstream.go internal/openaicompat/upstream_test.go
git commit -m "feat: encode normalized upstream chat requests"
```

### Task 5: Complete safe Chat tool-call response projection

**Files:**
- Modify: `internal/whitelabel/types.go`
- Modify: `internal/whitelabel/types_test.go`

- [x] **Step 1: Write the failing null-content tool-call test**

```go
func TestProjectChatCompletionAllowsNullContentWithToolCalls(t *testing.T) {
    raw := []byte(`{"id":"safe","object":"chat.completion","created":1,"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"raw"}}]},"finish_reason":"tool_calls"}]}`)
    got, err := (&WhiteLabelService{}).ProjectChatCompletion(raw, "model-a")
    if err != nil || got.Choices[0].Message.Content != nil || len(got.Choices[0].Message.ToolCalls) != 1 {
        t.Fatalf("completion=%#v err=%#v", got, err)
    }
}
```

Also add rejection cases for null content without calls, empty/duplicate IDs, non-function types, empty function names, oversized arguments, and unknown nested supplier fields being dropped rather than reflected.

- [x] **Step 2: Verify RED**

Run: `GOCACHE=/private/tmp/porsche-openai-cli-go-cache go test ./internal/whitelabel -run TestProjectChatCompletionAllowsNullContentWithToolCalls -count=1`

Expected: FAIL because current `projectCompletionContent` rejects null.

- [x] **Step 3: Implement the conditional null projection**

Decode and validate tool calls before deciding content validity. Accept JSON null only when at least one valid projected function call exists; retain the existing string/text-part behavior otherwise. Validate the public tool-call subset and discard all provider extensions.

- [x] **Step 4: Verify GREEN and existing projection regression**

Run: `GOCACHE=/private/tmp/porsche-openai-cli-go-cache go test ./internal/whitelabel -run 'TestProjectChatCompletion' -count=1`

Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add internal/whitelabel/types.go internal/whitelabel/types_test.go
git commit -m "feat: project chat function calls safely"
```

### Task 6: Non-streaming Responses projection

**Files:**
- Create: `internal/openaicompat/responses_response.go`
- Test: `internal/openaicompat/responses_response_test.go`

- [x] **Step 1: Write failing text and parallel-function projection tests**

```go
func TestProjectResponseIncludesTextAndParallelCalls(t *testing.T) {
    completion := whitelabel.ChatCompletion{ID: "upstream", Object: "chat.completion", Created: 10, Model: "model-a", Choices: []whitelabel.ChatCompletionChoice{{Index: 0, Message: whitelabel.ChatCompletionMessage{Role: "assistant", Content: "hello", ToolCalls: []whitelabel.ChatCompletionToolCall{{ID: "call_1", Type: "function", Function: whitelabel.ChatCompletionFunctionCall{Name: "a", Arguments: "{}"}}, {ID: "call_2", Type: "function", Function: whitelabel.ChatCompletionFunctionCall{Name: "b", Arguments: "raw"}}}}}}}
    got, err := ProjectResponse(completion, true, fixedIDSource("01"))
    if err != nil || got.Object != "response" || len(got.Output) != 3 || got.Store {
        t.Fatalf("response=%#v err=%v", got, err)
    }
}
```

- [x] **Step 2: Verify RED**

Run: `GOCACHE=/private/tmp/porsche-openai-cli-go-cache go test ./internal/openaicompat -run TestProjectResponse -count=1`

Expected: FAIL because response types and projector are missing.

- [x] **Step 3: Implement safe Responses objects and random public IDs**

Implement:

```go
func ProjectResponse(c whitelabel.ChatCompletion, parallel bool, ids IDSource) (Response, error)
type IDSource func(prefix string) (string, error)
```

Emit `resp_*`, `msg_*`, and `fc_*` IDs from `crypto/rand`; preserve upstream call IDs only as `call_id`; convert usage to `input_tokens`, `output_tokens`, and `total_tokens`; always return `status:"completed"` and `store:false`.

- [x] **Step 4: Verify GREEN and malformed-output rejection**

Run: `GOCACHE=/private/tmp/porsche-openai-cli-go-cache go test ./internal/openaicompat -run TestProjectResponse -count=1`

Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add internal/openaicompat/responses_response.go internal/openaicompat/responses_response_test.go
git commit -m "feat: project non-streaming Responses output"
```

### Task 7: Responses SSE conversion

**Files:**
- Create: `internal/openaicompat/responses_stream.go`
- Test: `internal/openaicompat/responses_stream_test.go`

- [x] **Step 1: Write failing deterministic event-order tests**

```go
func TestResponsesStreamOrdersTextAndToolEvents(t *testing.T) {
    s := NewResponsesStream("model-a", true, fixedIDSource("01"))
    var events []Event
    emit := func(e Event) error { events = append(events, e); return nil }
    chunks := []whitelabel.ChatCompletionChunk{
        chunkWithText("hel"),
        chunkWithToolDelta(0, "call_1", "lookup", "{\"q\":"),
        chunkWithToolDelta(0, "", "", "\"x\"}"),
        chunkDone("tool_calls"),
    }
    for _, chunk := range chunks { if err := s.Accept(chunk, emit); err != nil { t.Fatal(err) } }
    if err := s.Complete(emit); err != nil { t.Fatal(err) }
    assertEventTypes(t, events, "response.created", "response.in_progress", "response.output_item.added", "response.content_part.added", "response.output_text.delta", "response.output_item.added", "response.function_call_arguments.delta", "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.output_item.done", "response.completed")
    assertStrictSequence(t, events)
}
```

- [x] **Step 2: Verify RED**

Run: `GOCACHE=/private/tmp/porsche-openai-cli-go-cache go test ./internal/openaicompat -run TestResponsesStream -count=1`

Expected: FAIL because the stream converter does not exist.

- [x] **Step 3: Implement bounded per-index stream state**

Implement `NewResponsesStream`, `Accept`, `Complete`, and `Failed`. Buffer incomplete function identity by tool index, keep arguments separated by index, start sequence numbers at one, send no `[DONE]`, and return exactly one completed or failed terminal event. Do not emit `response.created` before the first validated projected chunk.

- [x] **Step 4: Verify GREEN across fragmentation and cancellation cases**

Run: `GOCACHE=/private/tmp/porsche-openai-cli-go-cache go test ./internal/openaicompat -run 'TestResponsesStream' -count=1`

Expected: PASS for CRLF/multiline upstream fixtures, interleaved tool indices, malformed first frame, post-start failure, and canceled context.

- [x] **Step 5: Commit**

```bash
git add internal/openaicompat/responses_stream.go internal/openaicompat/responses_stream_test.go
git commit -m "feat: translate chat streams to Responses SSE"
```

### Task 8: Gateway endpoint integration and OpenCode fixtures

**Files:**
- Modify: `internal/handler/gateway_tokens.go`
- Modify: `internal/handler/gateway_whitelabel_test.go`
- Modify: `internal/router/router_test.go`
- Create: `internal/handler/testdata/opencode-chat-request.json`
- Create: `internal/handler/testdata/opencode-responses-request.json`

- [x] **Step 1: Add failing HTTP contract tests**

Add an executable zero-upstream test first:

```go
func TestGatewayResponsesRejectsStateBeforeUpstream(t *testing.T) {
    state, _, calls := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a"}]}`)
    user := gatewayWhiteLabelUser(t, state, "13900200021")
    if err := state.DB.Create(user).Error; err != nil { t.Fatal(err) }
    _, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "responses", AllowedModels: models.JSONSlice{"model-a"}})
    if err != nil { t.Fatal(err) }

    for _, body := range []string{
        `{"model":"model-a","input":"hello","store":true}`,
        `{"model":"model-a","input":"hello","previous_response_id":"resp_1"}`,
    } {
        req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
        req.Header.Set("Authorization", "Bearer "+secret)
        req.Header.Set("Content-Type", "application/json")
        rec := httptest.NewRecorder()
        router.New(state).ServeHTTP(rec, req)
        if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"unsupported_parameter"`) {
            t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
        }
    }
    if got := calls.Load(); got != 0 { t.Fatalf("upstream calls=%d", got) }
}
```

Then add `TestGatewayChatToolRoundTrip` and `TestGatewayResponsesToolRoundTrip` using the existing authenticated `gatewayWhiteLabelState` fixture: first requests must return `call_1` and `call_2`, second requests must send matching outputs and return final text. Add `TestGatewayResponsesPostStartFailureEmitsFailed` asserting one terminal `response.failed` and no `[DONE]`, plus `TestGatewayResponsesCancellationStopsUpstream` asserting the upstream request context is canceled. Register `/v1/responses` in the expected public-route table. Load both sanitized JSON fixture files from `testdata` and assert their selected model IDs match the test catalog.

- [x] **Step 2: Verify RED**

Run: `GOCACHE=/private/tmp/porsche-openai-cli-go-cache go test ./internal/handler ./internal/router -run 'Gateway(ChatTool|Responses)' -count=1`

Expected: FAIL because `/v1/responses` is unregistered and Chat still rejects tool messages.

- [x] **Step 3: Integrate both decoders with one execution helper**

Refactor the route body around:

```go
type gatewayProtocol int
const (
    gatewayChat gatewayProtocol = iota
    gatewayResponses
)

func gatewayCompletion(c *gin.Context, state *app.State, protocol gatewayProtocol)
func decodeGatewayConversation(protocol gatewayProtocol, body []byte) (openaicompat.Conversation, *openaicompat.Error)
```

Both routes must authenticate before reading the body, enforce `application/json`, decode and validate before catalog/upstream access, authorize the normalized model, encode a fresh upstream body, and call `state.WhiteLabel.Chat`. Chat uses existing WhiteLabel projection/SSE; Responses uses the new response projectors. Map package errors into the existing fixed public error envelope without copying internal details.

- [x] **Step 4: Verify GREEN and regression behavior**

Run: `GOCACHE=/private/tmp/porsche-openai-cli-go-cache go test ./internal/handler ./internal/router -count=1`

Expected: PASS, including existing models, ACL, authentication-before-body, Chat error-frame, and `[DONE]` tests.

- [x] **Step 5: Commit**

```bash
git add internal/handler/gateway_tokens.go internal/handler/gateway_whitelabel_test.go internal/handler/testdata internal/router/router_test.go
git commit -m "feat: expose stateless Responses gateway"
```

### Task 9: Security, full regression, and acceptance evidence

**Files:**
- Modify only files required to fix failures exposed by the commands below.

- [x] **Step 1: Run focused tests with race detection**

Run: `GOCACHE=/private/tmp/porsche-openai-cli-go-cache go test -race ./internal/openaicompat ./internal/whitelabel ./internal/handler ./internal/router -count=1`

Expected: PASS with no race reports.

- [x] **Step 2: Run full test, vet, build, and diff gates**

```bash
GOCACHE=/private/tmp/porsche-openai-cli-go-cache go test ./... -count=1
GOCACHE=/private/tmp/porsche-openai-cli-go-cache go vet ./...
GOCACHE=/private/tmp/porsche-openai-cli-go-cache go build ./...
git diff --check
```

Expected: all commands exit 0.

- [x] **Step 3: Run sensitive-data and protocol-boundary scans**

```bash
rg -n 'sk-gw-|top-secret|function-secret|tool-secret' internal/openaicompat internal/handler --glob='*.go' --glob='*.json'
rg -n 'response\.(created|in_progress|completed|failed)|\[DONE\]' internal/openaicompat internal/handler
```

Expected: credentials occur only as deliberate test sentinels or documented prefixes; Responses terminal paths never emit `[DONE]`; Chat keeps `[DONE]`.

- [x] **Step 4: Review final diff against every completion criterion**

Confirm Chat and Responses two-round tool loops, parallel calls, zero-upstream invalid paths, cancel propagation, field projection, existing Platform Chat regression, no migration, and no persistence dependency.

- [x] **Step 5: Commit any verification-only corrections**

```bash
git add internal/openaicompat internal/handler/gateway_tokens.go internal/handler/gateway_whitelabel_test.go internal/handler/testdata internal/router/router_test.go
git commit -m "test: close OpenAI CLI compatibility gates"
```

Skip this commit when verification required no corrections.
