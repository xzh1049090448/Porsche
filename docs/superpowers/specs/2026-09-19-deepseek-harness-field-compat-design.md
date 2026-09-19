# DeepSeek Harness 请求字段兼容与受控上游透传设计

## 状态与基线

- 状态：`APPROVED_FOR_IMPLEMENTATION`（用户在本会话确认：先补齐设计与计划，再按完整 SDD 流程实现）
- 基线：后端 `origin/main` `69f55c1b0aef61d549f1973920268bc4625ebcda`
- 功能归属：`go-018` 的 Gateway/CLI 兼容子项目；不新增第二个 `in_progress` 功能
- 对外协议：`POST /v1/chat/completions`、`POST /v1/responses`，Bearer `sk-gw-...`
- 上游：唯一 JieKou AI 白牌上游，唯一出口 `POST {base}/chat/completions`
- 对应 issue：`xzh1049090448/Porsche#18`

## 问题陈述

DeepSeek Harness（DSH）通过 OpenAI-compatible Chat Completions 调用网关。其线上请求类型固定发送：

- 顶层 `thinking: {type: "enabled" | "disabled"}`
- 顶层 `reasoning_effort: "low" | "high" | "max"`
- assistant 历史消息回传 `reasoning_content: string`（思考模式工具轮必需）

响应侧读取 `choices[].delta.reasoning_content`，以及 usage 中的 `completion_tokens_details.reasoning_tokens`、`prompt_cache_hit_tokens`、`prompt_cache_miss_tokens`、`prompt_tokens_details.cached_tokens`。

当前 `/v1` 网关把请求先交给 `internal/openaicompat` 的严格白名单与 `DisallowUnknownFields`：

- `chatRequestFields`（`internal/openaicompat/chat_request.go:135`）拒绝 `thinking`、`reasoning_effort`，返回 `400 unsupported_parameter`；
- `chatMessageDTO`（`internal/openaicompat/chat_request.go:27`）拒绝 assistant 的 `reasoning_content`，返回 `400 invalid_request`；
- 响应投影 `projectCompletionMessage`（`internal/whitelabel/types.go:144`）与 `projectChunkDeltaDetail`（`internal/whitelabel/sse.go:312`）只保留 role/content/refusal/tool_calls，静默丢弃 `reasoning_content`；
- `ChatCompletionUsage`（`internal/whitelabel/types.go:77`）只有 3 个字段，usage 明细全部被丢弃。

结果：DSH 无法使用思考模式，CoT 无法回传，缓存/推理 token 计数不完整。仓库内 `--include=*.go` 对 `reasoning`、`thinking`、`reasoning_content`、`reasoning_tokens`、`prompt_cache_key` 的检索结果为零，本批为纯新增能力。

## 已确认决策

1. **默认结构化解析 + 字段白名单**：只显式接收并校验列出的字段，绝不默认原样透传客户端 JSON。
2. **按模型/上游能力映射 `reasoning_effort`、`thinking`**：只有被显式声明的模型才接受这些字段并转发；未声明的模型继续返回 `400 unsupported_parameter`（fail-closed，不静默丢弃）。
3. **敏感字段默认过滤**：透传模式也必须先过固定 deny list 与结构约束，并产出可审计的剥离清单。
4. **仅按渠道/模型显式开启原始透传**：通过环境配置按逻辑模型 ID（精确或 `re:` RE2 模式）声明；默认空表示从不透传。
5. **补齐契约、计费、审计与回归测试**：本批交付针对新字段的契约测试、计费/用量不回归测试、审计与脱敏测试，以及既有套件回归。
6. **参考 new-api**：结构化适配 + 受控 `PassThroughBodyEnabled`（`new-api` `relaykit/dto/channel_settings.go:17`、`relay/common/relay_info.go:976`、`relay/compatible_handler.go:76-163`）。

### 关于“计费、审计”的实现边界（显式声明）

- 网关 `/v1` 路径当前**没有任何**用量持久化、计费或审计写入（`internal/handler/gateway_tokens.go` 无 `UsageRecord`、无 `AuditService.Log`）；平台路径 `/api/v1/platform/**` 才有计费。本批不新增网关计费/审计持久化路径，因为那需要新的 `usage_records` 明细列与迁移。
- 本批的“计费”落点是：**保证新 usage 明细字段不改变既有 `total_tokens` 口径**，平台计费继续只用 `TotalTokens`（`internal/service/platform_chat.go:680-696`、`internal/service/platform_single_generation.go:856-889`、`internal/service/platform_generation_persistence.go:434-507`）。
- 本批的“审计”落点是：透传与思考字段只允许进入**无内容**的结构化日志与剥离清单，并有测试证明提示词、思考文本、密钥不进入日志、错误 envelope 或审计字段。
- 真正为 `/v1` 增加逐请求计费与 DB 审计属于独立特性，需要迁移与单独批准，不在本批范围。

## 非目标

- 不新增数据库表、列或迁移；不改 `usage_records`、GORM 实体或 `public_model_configs.capabilities`。
- 不改变 Gateway Token 鉴权、模型/IP ACL、目录校验、错误 envelope、SSE 帧顺序与 `[DONE]` 契约。
- 不改变 `/api/v1/platform/**` 的请求契约，不给平台路径接线推理字段或透传。
- 不实现上游 `/responses` 直连、Files API（`{"type":"file","file_id":...}`）、托管工具、`store:true`、非空 `previous_response_id`。
- 不输出 Responses 的 reasoning item 或 `response.reasoning_*` 事件（Responses 只接受 `reasoning.effort` 输入映射，不回传推理内容）。
- 不实现任意未知字段透传、按 header 的透传开关、全局 `PassThroughRequestEnabled` 等价物。
- 不做真实付费上游验收、部署或生产迁移。

## DSH 线上契约（冻结依据）

来源：本机安装的 `@deepseek-ai/dsh-llm-deepseek` 类型定义（`lib/types/types.d.ts` 的 `WireRequest` / `WireUsage`）。本批把它冻结为测试 fixture。

| 方向 | 字段 | 处理 |
| --- | --- | --- |
| 请求 | `model`、`messages`、`stream:true`、`stream_options.include_usage:true`、`tools`、`temperature`、`max_tokens`、`stop` | 已支持，不变 |
| 请求 | `thinking` | 新增，结构化解析 + 能力门禁 + 转发 |
| 请求 | `reasoning_effort`（`low`/`high`/`max`） | 新增，结构化解析 + 能力门禁 + 转发 |
| 请求 | assistant `reasoning_content` | 新增，结构化解析 + 转发 |
| 响应 | `delta.reasoning_content`、message `reasoning_content` | 新增投影 |
| 响应 | `usage.completion_tokens_details.reasoning_tokens`、`usage.prompt_tokens_details.cached_tokens`、`usage.prompt_cache_hit_tokens`、`usage.prompt_cache_miss_tokens` | 新增可选投影 |

## 架构与文件边界

```text
POST /v1/chat/completions ──┐
                            ├─ gatewayCompletion (internal/handler/gateway_tokens.go)
POST /v1/responses ─────────┘        │
                                     ├─ [默认] DecodeChat/DecodeResponses(policy) -> EncodeUpstream
                                     └─ [显式透传, 仅 Chat] ExtractChatRouting -> SanitizePassthrough
                                                                     │
                                     WhiteLabelService.Chat(ctx, body) ┘  （唯一上游出口）
```

| 文件 | 职责 | 类型 |
| --- | --- | --- |
| `internal/openaicompat/reasoning.go`（新增） | `ReasoningPolicy`、合法 effort 枚举、思考字段校验与映射 | 纯代码 |
| `internal/openaicompat/passthrough.go`（新增） | `ExtractChatRouting`、敏感字段 deny list、`SanitizePassthrough` 与剥离报告 | 纯代码 |
| `internal/openaicompat/chat_request.go` | 白名单新增字段、`reasoning_content`、能力门禁 | 纯代码 |
| `internal/openaicompat/responses_request.go` | `reasoning.effort` 映射、未知嵌套拒绝 | 纯代码 |
| `internal/openaicompat/types.go` | `Conversation` 新增字段与常量 | 纯代码 |
| `internal/openaicompat/upstream.go` | `upstreamRequest` 新增字段与编码 | 纯代码 |
| `internal/openaicompat/validate.go` | `validateConversation` 补充枚举与长度校验 | 纯代码 |
| `internal/whitelabel/types.go` | 非流式 message `reasoning_content`、usage 明细投影 | 纯代码 |
| `internal/whitelabel/sse.go` | 流式 delta `reasoning_content`、usage 明细投影 | 纯代码 |
| `internal/config/config.go` | `parseModelSelectorList` 抽取、`JIEKOU_REASONING_MODELS`、`JIEKOU_PASSTHROUGH_MODELS` | 配置（无 schema） |
| `internal/handler/gateway_tokens.go` | 能力策略装配、透传分支、无内容审计日志 | 纯代码 |
| `.env.example`、`README.md` | 新环境变量与边界说明 | 文档 |
| `docs/superpowers/specs|plans/2026-09-19-*.md`、`progress.md`、`feature_list.json` | 设计与证据 | 文档 |

Handler 不承载业务规则：能力判定在 `openaicompat.ReasoningPolicy`，选择器解析在 `config`，Handler 只做顺序编排。

## 请求契约

### Chat Completions 新增顶层字段

- `reasoning_effort`：JSON 字符串，取值集合 `minimal|low|medium|high|max`（DSH 使用 `low|high|max`）。非字符串、空串或集合外取值 → `400 invalid_request`。
- `thinking`：JSON 对象，**仅**允许 `type` 字段，取值 `enabled|disabled`；未知嵌套字段、缺 `type`、类型错误或额外字段 → `400 unsupported_parameter`。

### Chat assistant 消息新增字段

- `reasoning_content`：JSON 字符串，仅 `assistant` 角色允许；必须为合法 UTF-8 且长度 ≤ `MaxTextContentBytes`（1 MiB）。其他角色出现该字段 → `400 invalid_request`。

### Responses 新增字段

- `reasoning`：JSON 对象，**仅**允许 `effort` 字段，取值同 `reasoning_effort`；`summary`、`generate_summary` 或未知嵌套字段 → `400 unsupported_parameter`（沿用既有嵌套未知字段分类）。`reasoning.effort` 映射到同一 canonical effort。
- Responses **不**接受 `include`、`text`、`prompt_cache_key`：继续按未知顶层字段返回 `400 unsupported_parameter`。

### 错误分类保持不变

- 未知顶层字段 → `400 unsupported_parameter`
- 已知字段形状/枚举/长度错误 → `400 invalid_request`
- 超限 → `413 request_too_large`
- `store:true`、非空 `previous_response_id`、托管工具 → `400 unsupported_parameter`（不放松）

## 模型能力映射

新增环境变量 `JIEKOU_REASONING_MODELS`：逗号分隔的逻辑模型选择器，语法与 `JIEKOU_ALLOWED_MODELS` 完全一致（精确 ID，或 `re:` 前缀 RE2 模式）。

- 命中选择器的模型：`thinking`、`reasoning_effort`（Chat）与 `reasoning.effort`（Responses）被接受，并转发给上游。
- 未命中的模型：请求中出现任一思考字段 → `400 unsupported_parameter`，且**上游调用次数为零**。
- 默认空：没有任何模型接受思考字段，行为与当前完全一致（向后兼容）。
- 能力声明不改变 allowlist/ACL：模型仍需通过全局 allowlist、上游目录、用户与 Gateway Token ACL。

canonical 表示：

```go
type ThinkingMode string
const (
    ThinkingEnabled  ThinkingMode = "enabled"
    ThinkingDisabled ThinkingMode = "disabled"
)

type Conversation struct {
    // ... 既有字段
    ReasoningEffort string        // "" 表示未提供
    Thinking        *ThinkingMode // nil 表示未提供
}

type Message struct {
    // ... 既有字段
    ReasoningContent *string // 仅 assistant，可空串
}
```

## 敏感字段默认过滤

结构化模式下，未列入白名单的字段一律拒绝（既有行为，不放宽）。原始透传模式下，在转发前**必须**剥离固定 deny list：

顶层：`user`、`metadata`、`safety_identifier`、`service_tier`、`inference_geo`、`speed`、`store`、`previous_response_id`、`prompt_cache_key`、`logit_bias`、`logprobs`、`top_logprobs`、`api_key`、`authorization`、`secret`、`password`、`credentials`、`provider`。
嵌套：`stream_options.include_obfuscation`。

剥离规则：

- 只删除，绝不新增或改写字段；`model` 必须与已授权模型精确一致，不匹配即 `400 invalid_request`。
- 删除后若 `stream_options` 为空对象，则整体删除该键。
- 其余字段按客户端原始值转发（除必要的 JSON 重编码外不做语义解释）。
- 返回 `PassthroughReport{StrippedFields []string, MessageCount int, ToolCount int}` 供无内容审计使用。

## 受控原始透传

- 开关：`JIEKOU_PASSTHROUGH_MODELS`，语法同上；默认空表示禁用。
- 仅作用于 `POST /v1/chat/completions`；`/v1/responses`、`/api/v1/platform/**`、健康检查路径永不透传。
- 执行顺序严格保持：12 MiB 上限 → Bearer 鉴权 → `Content-Type: application/json` → 读取 body → `ExtractChatRouting` 提取 `model`/`stream` → Gateway Token 模型 ACL → 全局/用户/目录校验 → 透传选择器判定 → `SanitizePassthrough` → `WhiteLabel.Chat`。
- 透传分支**不**经过 `EncodeUpstream`，但鉴权、ACL、大小限制、媒体类型与敏感字段剥离一个都不能省。
- 透传结构约束：顶层必须是单个 JSON 对象；`model` 为字符串；`messages` 非空数组且 ≤ `MaxMessages`（128）；`tools` 若存在 ≤ `MaxTools`（32）；整体 ≤ `MaxRequestBodyBytes`（12 MiB）。
- 透传时 `reasoning_effort`、`thinking`、`reasoning_content` **不**被剥离（这正是透传的用途）；操作者通过显式声明该模型承担上游协议责任。

## 上游编码

`upstreamRequest` 新增：

```go
ReasoningEffort *string         `json:"reasoning_effort,omitempty"`
Thinking        *upstreamThinking `json:"thinking,omitempty"` // {"type":"enabled"}
```

assistant 上游消息新增 `reasoning_content,omitempty`。编码器从 canonical 结构重新生成 JSON，不合并原始请求体；`EncodeUpstream` 继续先调用 `validateConversation`，因此手工构造的非法 effort/超长 `reasoning_content` 无法绕过校验。

## 响应投影与用量

- 非流式 `ChatCompletionMessage` 新增 `reasoning_content,omitempty`；`projectCompletionMessage` 的本地解码结构同步接收并做 UTF-8/长度校验。
- 流式 `ChatCompletionChunkDelta` 新增 `reasoning_content,omitempty`；`projectChunkDeltaDetail` 同步接收。
- `ChatCompletionUsage` 新增可选字段：

```go
PromptTokensDetails     *ChatCompletionPromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
CompletionTokensDetails *ChatCompletionCompletionTokensDetails `json:"completion_tokens_details,omitempty"`
PromptCacheHitTokens    *int                                   `json:"prompt_cache_hit_tokens,omitempty"`
PromptCacheMissTokens   *int                                   `json:"prompt_cache_miss_tokens,omitempty"`
```

- `validCompletionUsage` 扩展为：三个主字段与全部明细字段非负且 ≤ `math.MaxInt32`；负值或超界视为上游畸形并沿用既有失败分类。
- `total_tokens`、`prompt_tokens`、`completion_tokens` 语义**不变**；明细字段是附加信息。平台计费继续只读 `TotalTokens`，因此推理/缓存明细不可能改变计费口径（有契约测试固定）。
- Responses 投影不使用 `reasoning_content`；`ResponseUsage` 结构不变。

## 审计（无内容）

透传命中时输出一行结构化日志，内容仅限元数据：

```text
gateway passthrough request_id=<id> model=<logical id> stream=<bool> stripped=<comma-separated deny-list names or "-">
```

日志**绝不**包含：提示词、消息正文、`reasoning_content`、工具参数/输出、`prompt_cache_key` 值、原始 body、密钥或 token。思考字段命中（非透传）不新增日志，避免噪声。测试通过重定向标准 logger 输出断言日志既包含元数据、又不含哨兵文本。

## 向后兼容

- 现有不带思考字段的 Chat/Responses 请求、响应 JSON、SSE 帧与 `[DONE]` 顺序逐字节保持不变。
- 新增响应字段均为 `omitempty` 的可选字段；不改既有字段顺序语义与类型。
- 新增请求字段默认全部拒绝（能力选择器为空），因此默认部署行为与当前一致。
- 不新增/修改任何数据库对象，应用回滚不面对 schema 差异。
- `/api/v1/platform/**` 的请求白名单、计费与配额行为不变。

## 测试与验收

### 单元与契约测试（`internal/openaicompat`）

- DSH `WireRequest` fixture（`thinking` + `reasoning_effort` + assistant `reasoning_content`）解析为 canonical 并重新编码，上游 JSON 精确匹配。
- 能力命中/未命中：未命中时返回 `unsupported_parameter` 且不产生 canonical 值。
- 枚举/类型/长度边界：effort 集合外、`thinking` 未知嵌套、`reasoning_content` 非 assistant、超长、非法 UTF-8。
- Responses `reasoning.effort` 映射与嵌套未知拒绝。
- 透传：deny list 每个字段被剥离且 `PassthroughReport` 记录；非对象/多值 JSON/`model` 不匹配/`messages` 缺失或超限/tools 超限被拒绝；允许字段原样保留。
- `ExtractChatRouting` 只提取 `model`/`stream`，忽略未知字段但拒绝畸形 JSON。
- 错误分类矩阵：unsupported vs invalid vs too_large 不变。

### 投影与计费契约测试（`internal/whitelabel`）

- 非流式 message 与 SSE delta 的 `reasoning_content` 投影。
- usage 明细字段投影、负值/超界拒绝。
- `total_tokens` 在存在推理/缓存明细时保持上游原值。
- 既有 chunk/message 投影回归逐字节不变（无新字段时不出现新键）。

### Handler 契约测试（`internal/handler`）

- DSH 形状请求经真实 Gateway Token + 伪上游：chat 非流式与 SSE 均透出 `reasoning_content`，上游 body 含 `thinking`/`reasoning_effort`/`reasoning_content`。
- 未声明能力的模型携带思考字段 → 400 `unsupported_parameter`，零上游调用。
- 透传开启时：客户端 body 的未知安全字段到达上游；deny list 字段被剥离；鉴权/ACL/目录失败仍零上游调用。
- 透传关闭时：未知字段仍 400 `unsupported_parameter`（回归）。
- 审计日志含 request_id/model 且不含哨兵。
- 既有 `gateway_whitelabel_test.go` 全部用例保持通过。

### 门禁命令

```text
GOCACHE=/private/tmp/porsche-issue18-go-cache go test ./internal/openaicompat ./internal/whitelabel ./internal/handler ./internal/config -count=1
GOCACHE=/private/tmp/porsche-issue18-go-cache go test -race ./internal/openaicompat ./internal/whitelabel ./internal/handler ./internal/config -count=1
GOCACHE=/private/tmp/porsche-issue18-go-cache go test ./... -count=1
GOCACHE=/private/tmp/porsche-issue18-go-cache go vet ./...
GOCACHE=/private/tmp/porsche-issue18-go-cache go build ./...
git diff --check
```

### 独立门禁

- 规格复审：逐条核对本设计的允许字段、错误分类、能力门禁、透传顺序与非目标。
- 安全复审：透传剥离、无内容日志、敏感哨兵、鉴权先于 body、零越权上游调用。
- 测试验证：独立复跑上述命令并区分 PASS / FAIL / SKIPPED。

### 明确未覆盖

- 真实 JieKou 上游对 `reasoning_effort`/`thinking` 的支持未验证（本地不调用付费上游）。
- 真实 DSH 端到端会话未运行。
- 平台路径推理控制、`/v1` 计费/DB 审计、Responses reasoning item 均为后续独立工作。

## 交付顺序

1. 冻结 DSH fixture 与错误分类契约测试（RED）。
2. `ReasoningPolicy` + canonical 字段 + Chat/Responses 解码与编码（GREEN）。
3. 能力配置与 Handler 接线；未声明模型 fail-closed。
4. 响应投影：`reasoning_content` 与 usage 明细。
5. 透传：`ExtractChatRouting` + `SanitizePassthrough` + Handler 分支 + 无内容审计。
6. 文档、`.env.example`、`progress.md`、`feature_list.json`。
7. 全量/race/vet/build/diff 与独立规格、安全、测试复审。

## 完成判定

只有以下条件全部满足才视为本批实现完成：

- DSH 形状的 chat 请求在声明能力的模型上往返成功，未声明模型稳定 400 且零上游调用。
- `thinking`/`reasoning_effort`/`reasoning_content` 契约、能力映射、错误分类均有测试。
- 透传仅在显式声明模型且通过剥离与结构约束后生效，鉴权/ACL/大小/媒体类型一个不少。
- usage 明细不改变 `total_tokens`，平台计费口径有契约测试固定。
- 审计日志与错误 envelope 无内容泄露，敏感哨兵测试通过。
- 既有 openaicompat/whitelabel/handler/config 全量回归、race、vet、build、diff 通过。
- 规格、安全、测试三项独立门禁对同一 review snapshot 通过。

生产部署、真实付费上游、真实 DSH 端到端与平台推理控制为后续单独授权阶段，不能由本地实现结果替代。
