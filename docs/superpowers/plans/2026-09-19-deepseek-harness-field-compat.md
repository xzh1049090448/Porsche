# DeepSeek Harness 请求字段兼容与受控上游透传实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 DeepSeek Harness 的 `thinking`、`reasoning_effort`、assistant `reasoning_content` 在声明能力的模型上安全往返，并按模型显式开启受控原始上游透传。

**Architecture:** 在 `internal/openaicompat` 中新增 `ReasoningPolicy` 与 `SanitizePassthrough` 纯逻辑；解码器显式接收并校验思考字段，编码器仍从 canonical 结构重建上游 JSON；`internal/config` 新增两个模型选择器环境变量；Handler 只负责顺序编排、透传分支与无内容审计。响应侧在 `internal/whitelabel` 投影中补齐 `reasoning_content` 与 usage 明细。

**Tech Stack:** Go 1.22、Gin、`encoding/json`、`go test`、`regexp`、标准库 `log`。

**Design:** `docs/superpowers/specs/2026-09-19-deepseek-harness-field-compat-design.md`

**Baseline:** `origin/main` `69f55c1`

**Worktree:** `/Users/xuzhihao/code/Porsche/.worktrees/deepseek-harness-field-compat`（分支 `feature/deepseek-harness-field-compat`）

**GOCACHE:** `/private/tmp/porsche-issue18-go-cache`

---

## Task 0: 冻结 DSH fixture 与错误分类契约测试（RED）

**Files:**
- Create: `internal/openaicompat/reasoning_test.go`
- Create: `internal/openaicompat/passthrough_test.go`
- Modify: `internal/openaicompat/chat_request_test.go`
- Modify: `internal/openaicompat/responses_request_test.go`

- [ ] **Step 1: 写 DSH Chat 契约测试**

测试冻结 DSH `WireRequest` 形状：`thinking:{type:"enabled"}`、`reasoning_effort:"high"`、assistant `reasoning_content`、`stream:true`、`stream_options.include_usage:true`。断言能力命中时解析成功、canonical 字段正确、重新编码后上游 JSON 精确包含三个字段。

- [ ] **Step 2: 写能力未命中与错误分类矩阵测试**

未声明模型 + 思考字段 → `unsupported_parameter`；effort 集合外/`thinking` 未知嵌套/非法形状 → 对应 `invalid_request` 或 `unsupported_parameter`；`store:true`、非空 `previous_response_id` 仍为 `unsupported_parameter`。

- [ ] **Step 3: 写透传单元测试**

`ExtractChatRouting` 提取 `model`/`stream`；`SanitizePassthrough` 剥离 deny list、保留允许字段、拒绝非对象/多值 JSON/`model` 不匹配/`messages` 缺失或超限/`tools` 超限。

- [ ] **Step 4: 运行 focused 测试确认 RED**

Run: `GOCACHE=/private/tmp/porsche-issue18-go-cache go test ./internal/openaicompat -run 'TestReasoning|TestPassthrough|TestDecodeChatDeepSeekHarness' -count=1`

Expected: FAIL（类型与函数不存在）。

## Task 1: canonical 类型、能力策略与 Chat/Responses 解码

**Files:**
- Create: `internal/openaicompat/reasoning.go`
- Modify: `internal/openaicompat/types.go`
- Modify: `internal/openaicompat/chat_request.go`
- Modify: `internal/openaicompat/responses_request.go`
- Modify: `internal/openaicompat/validate.go`
- Modify: `internal/openaicompat/upstream.go`
- Test: `internal/openaicompat/reasoning_test.go`、`chat_request_test.go`、`responses_request_test.go`、`upstream_test.go`

- [ ] **Step 1: 定义 `ReasoningPolicy` 与合法枚举**

```go
type ReasoningPolicy struct {
    Models   map[string]struct{}
    Patterns []*regexp.Regexp
}

func (p ReasoningPolicy) Allows(model string) bool
func (p ReasoningPolicy) Empty() bool

var ReasoningEfforts = map[string]struct{}{"minimal": {}, "low": {}, "medium": {}, "high": {}, "max": {}}
```

- [ ] **Step 2: 扩展 `Conversation` 与 `Message`**

新增 `ReasoningEffort string`、`Thinking *ThinkingMode`、`Message.ReasoningContent *string`，以及 `ThinkingEnabled/ThinkingDisabled` 常量。

- [ ] **Step 3: Chat 解码新增字段与能力门禁**

`DecodeChat(body []byte, policy ReasoningPolicy)`：白名单加入 `reasoning_effort`、`thinking`；assistant 消息 `reasoningContentDTO` 接收 `reasoning_content`；对未命中模型的思考字段返回 `UnsupportedParameter()`。

- [ ] **Step 4: Responses 解码 `reasoning.effort`**

`DecodeResponses(body []byte, policy ReasoningPolicy)`：白名单加入 `reasoning`；严格对象仅允许 `effort`，映射为 canonical effort；未知嵌套仍 `unsupported_parameter`。

- [ ] **Step 5: `validateConversation` 与上游编码**

`validateConversation` 校验 effort 枚举与 `reasoning_content` 长度/角色；`EncodeUpstream` 输出 `reasoning_effort`、`thinking`、assistant `reasoning_content`。

- [ ] **Step 6: 更新既有调用点与测试**

`internal/handler/gateway_tokens.go` 解码调用与 `internal/handler/gateway_whitelabel_test.go:270-272`、`internal/openaicompat/*_test.go` 全部改为传入策略（既有用例传 `NoReasoning`）。

- [ ] **Step 7: 运行 focused 测试确认 GREEN**

Run: `GOCACHE=/private/tmp/porsche-issue18-go-cache go test ./internal/openaicompat -count=1`

Expected: PASS。

## Task 2: 配置选择器与 Handler 能力接线

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/handler/gateway_tokens.go`
- Modify: `.env.example`
- Modify: `README.md`

- [ ] **Step 1: 抽取 `parseModelSelectorList`**

从 `ParseWhiteLabelSettings` 中抽取既有 `re:` 解析逻辑为可复用函数，保持错误文案与 fail-closed 行为不变。

- [ ] **Step 2: 新增 `JIEKOU_REASONING_MODELS` / `JIEKOU_PASSTHROUGH_MODELS`**

扩展 `WhiteLabelSettings`（`ReasoningModels`、`ReasoningModelPatterns`、`PassthroughModels`、`PassthroughModelPatterns`）与 `ParseWhiteLabelSettings` 签名；新增 `AllowsReasoning`、`AllowsPassthrough`、`PassthroughEnabled`。默认空 = 全部关闭。

- [ ] **Step 3: Handler 装配策略并 fail-closed**

`gatewayCompletion` 由 `Settings.WhiteLabel` 构造 `ReasoningPolicy` 传给解码器；未声明模型的思考字段稳定 400 且零上游调用。

- [ ] **Step 4: 配置测试**

空值默认、精确命中、`re:` 命中、非法 RE2 拒绝、passthrough 默认关闭。

- [ ] **Step 5: 文档**

`.env.example` 与 `README.md` 说明两个变量、默认关闭、fail-closed 与透传边界。

- [ ] **Step 6: 运行 focused 测试**

Run: `GOCACHE=/private/tmp/porsche-issue18-go-cache go test ./internal/config ./internal/handler -run 'TestParseWhiteLabel|TestGatewayChatRejects|TestGatewayChatDeepSeek' -count=1`

Expected: PASS。

## Task 3: 响应投影 `reasoning_content` 与 usage 明细

**Files:**
- Modify: `internal/whitelabel/types.go`
- Modify: `internal/whitelabel/sse.go`
- Test: `internal/whitelabel/types_test.go`、`internal/whitelabel/sse_test.go`

- [ ] **Step 1: 非流式 message 投影**

`ChatCompletionMessage` 与 `projectCompletionMessage` 的本地解码结构加入 `reasoning_content`，校验 UTF-8 与长度。

- [ ] **Step 2: 流式 delta 投影**

`ChatCompletionChunkDelta` 与 `projectChunkDeltaDetail` 加入 `reasoning_content`。

- [ ] **Step 3: usage 明细投影**

`ChatCompletionUsage` 增加 `prompt_tokens_details`、`completion_tokens_details`、`prompt_cache_hit_tokens`、`prompt_cache_miss_tokens` 可选字段；`validCompletionUsage` 校验非负与 `MaxInt32` 上界。

- [ ] **Step 4: 契约与计费不回归测试**

断言 `total_tokens` 保持上游原值、明细为附加信息、缺省时输出 JSON 不出现新键。

- [ ] **Step 5: 运行 focused 测试**

Run: `GOCACHE=/private/tmp/porsche-issue18-go-cache go test ./internal/whitelabel ./internal/openaicompat -count=1`

Expected: PASS。

## Task 4: 受控原始透传与无内容审计

**Files:**
- Create: `internal/openaicompat/passthrough.go`
- Modify: `internal/handler/gateway_tokens.go`
- Test: `internal/openaicompat/passthrough_test.go`、`internal/handler/gateway_whitelabel_test.go`

- [ ] **Step 1: `ExtractChatRouting`**

只提取 `model`/`stream`；拒绝非对象、多值 JSON、空模型；大小上限复用 `MaxRequestBodyBytes`。

- [ ] **Step 2: `SanitizePassthrough`**

顶层 deny list、`stream_options.include_obfuscation`、结构约束（`model` 精确一致、`messages` 非空 ≤ 128、`tools` ≤ 32）、返回 `PassthroughReport`；删除后空 `stream_options` 整体移除。

- [ ] **Step 3: Handler 透传分支**

仅 `/v1/chat/completions` 且 `PassthroughEnabled()` 时启用；顺序保持鉴权 → 媒体类型 → body → 提取 → ACL → 目录 → 选择器 → 剥离 → 上游；`/v1/responses` 与平台路径永不透传。

- [ ] **Step 4: 无内容审计日志**

`log.Printf("gateway passthrough request_id=%s model=%s stream=%t stripped=%s", ...)`，只含元数据。

- [ ] **Step 5: Handler 契约测试**

透传命中/未命中、deny list 剥离、零越权上游调用、日志不含哨兵、透传关闭时未知字段仍 400。

- [ ] **Step 6: 运行 focused 与 race 测试**

Run: `GOCACHE=/private/tmp/porsche-issue18-go-cache go test -race ./internal/openaicompat ./internal/whitelabel ./internal/handler ./internal/config -count=1`

Expected: PASS。

## Task 5: 文档与跟踪文件

**Files:**
- Modify: `progress.md`
- Modify: `feature_list.json`

- [ ] **Step 1: 更新 `progress.md`**

记录本批范围、命令、退出码、限定项（真实上游/DSH/平台推理/`/v1` 计费均 NOT_RUN）。

- [ ] **Step 2: 更新 `feature_list.json`**

在 `go-018` 下追加 evidence 与 notes，保持 `single_active_feature`；不新增 `in_progress` 功能。

## Task 6: 全量门禁与独立复审

- [ ] **Step 1: 全量门禁**

```text
GOCACHE=/private/tmp/porsche-issue18-go-cache go test ./... -count=1
GOCACHE=/private/tmp/porsche-issue18-go-cache go vet ./...
GOCACHE=/private/tmp/porsche-issue18-go-cache go build ./...
git diff --check
```

- [ ] **Step 2: review snapshot**

由授权 writer 用 `python3 docs/agents/review_snapshot.py` 在 worktree 外私有目录保存 baseline/snapshot，记录内容哈希、命令、退出码与 stdout ID。

- [ ] **Step 3: 规格 → 安全 → 测试门禁**

同一 snapshot 依次通过 `spec_compliance_reviewer`、`security_reviewer`、`test_engineer`；任一发现退回原 worker，修复后重建 snapshot 并从规格重审。

- [ ] **Step 4: Controller 汇总**

`project_manager` 在前述门禁关闭后汇总结论，记录未决风险与限定项。
