# OpenAI CLI 工具调用与 Responses API 兼容设计

## 状态与基线

- 状态：`APPROVED_FOR_IMPLEMENTATION`
- 基线：后端 `origin/main` `d85c101db3d6898404b9abab677b6fd4c2a4ed87`
- 功能归属：`go-018` 的 Gateway/CLI 兼容子项目；不新增第二个 `in_progress` 功能
- 对外认证：`Authorization: Bearer sk-gw-...`
- 对外 Base URL：`https://aiportcloud.com/v1`

本设计补齐两项能力：通用 OpenAI Chat Completions 工具调用消息往返，以及无状态的 Responses API 文本与函数调用兼容层。目标客户端包括 OpenCode 和使用 OpenAI-compatible Chat Completions/Responses 协议的 CLI。

## 已确认决策

1. 同时支持 `/v1/chat/completions` 与 `/v1/responses`。
2. Responses 首期只覆盖文本、客户端自定义函数工具、并行函数调用、非流式和 SSE 流式输出。
3. Responses 是到现有上游 `/chat/completions` 的协议转换层，不要求 JieKou AI 原生提供 `/responses`。
4. Responses 固定为无状态模式：省略 `store` 等价于 `false`；`store:true` 与非空 `previous_response_id` 均被拒绝。
5. 两种公开协议先转换为统一内部结构，再执行同一套安全校验、上游编码与调用关联检查。
6. 现有 `/api/v1/platform/**` 网页聊天、计费、会话持久化和 SSE 业务事件不在本次改造范围。

## 非目标

- 不实现 Web Search、File Search、Computer Use、Remote MCP 或其他托管工具。
- 不实现文件上传、文件引用、音频、图片或 Responses reasoning item。
- 不实现 `/v1/responses/:id` 查询、取消、删除或服务端 Conversation。
- 不保存提示词、模型正文、工具参数、工具输出或可恢复的 Response 状态。
- 不新增数据库表、Redis 状态或迁移。
- 不增加 `/v1/embeddings`、Anthropic Messages 或 Realtime API。
- 不改变 Gateway Token 的创建、哈希、ACL、IP 白名单、有效期或 owner 权限规则。

## 后续兼容性扩展 TODO

首期实现完成后，建立并持续维护 Codex、OpenCode 及其他主流 Agent/CLI 的兼容性矩阵，在不削弱鉴权、字段投影和敏感信息保护的前提下尽可能扩大兼容范围。后续工作至少包括：

- 固定并跟踪各客户端版本、所用协议、请求字段、SSE 事件和工具回传形状。
- 为 Codex 补充当前 Responses-only provider 所需的 reasoning、text、include、prompt cache、stream options 与无状态 reasoning item 回传能力。
- 同时覆盖 OpenCode 的 `@ai-sdk/openai-compatible` Chat provider 和 `@ai-sdk/openai` Responses provider。
- 收集其他常见 OpenAI-compatible SDK/CLI 自动发送的可选字段，为安全兼容字段采用显式接收、验证、转换或有记录的降级策略。
- 每次客户端或协议升级后重放脱敏 fixture，并执行真实客户端的文本、单工具、并行工具和多轮闭环验收。

本 TODO 不扩大本次已批准的首期实现范围，也不能替代首期 OpenCode fixture 和本地闭环验收。

## 总体架构

新增 `internal/openaicompat` 包，公开协议适配与上游协议之间只交换规范化类型：

```text
/v1/chat/completions -> Chat decoder -----\
                                             -> canonical conversation -> common validation
/v1/responses        -> Responses decoder -/                            -> upstream encoder
                                                                          -> JieKou /chat/completions
                                                                          -> safe upstream projection
                                                   Chat projector <--------/
                                              Responses projector <--------/
```

实现文件边界：

| 文件 | 职责 |
| --- | --- |
| `internal/openaicompat/types.go` | 规范化消息、内容、工具、调用与生成选项 |
| `internal/openaicompat/chat_request.go` | Chat Completions 严格解码与规范化 |
| `internal/openaicompat/responses_request.go` | Responses 严格解码与规范化 |
| `internal/openaicompat/validate.go` | 字段组合、大小、顺序和 call ID 状态机 |
| `internal/openaicompat/upstream.go` | 生成现有上游 Chat Completions JSON |
| `internal/openaicompat/chat_response.go` | Chat 非流式与 SSE 输出投影 |
| `internal/openaicompat/responses_response.go` | Responses 非流式与 SSE 输出投影 |
| `internal/handler/gateway_tokens.go` | 路由、Bearer 鉴权、模型/IP ACL 和调用编排 |

`internal/whitelabel` 继续负责固定上游、目录缓存、网络调用、响应字段安全投影和上游错误归一化。新包不得绕过该层直接透传上游响应。

## 规范化类型

概念结构如下，最终 Go 类型可按职责拆分，但不得退回 `map[string]interface{}` 作为公共协议核心：

```go
type Conversation struct {
    Model             string
    Instructions      []Message
    Messages          []Message
    Tools             []ToolDefinition
    ToolChoice        ToolChoice
    ParallelToolCalls bool
    MaxOutputTokens   *int64
    Stream            bool
}

type Message struct {
    Role       Role
    Content    []ContentPart
    ToolCalls  []ToolCall
    ToolCallID string
}

type ToolCall struct {
    ID        string
    Name      string
    Arguments string
}
```

所有枚举和联合字段必须通过带 `DisallowUnknownFields` 的明确 DTO 解码。规范化类型只包含已经验证且允许发送给上游的字段。

## Chat Completions 请求契约

继续使用 `POST /v1/chat/completions`。允许的顶层字段为：

- `model`
- `messages`
- `max_tokens`
- `max_completion_tokens`
- `temperature`
- `top_p`
- `frequency_penalty`
- `presence_penalty`
- `stop`
- `seed`
- `n`
- `tools`
- `tool_choice`
- `parallel_tool_calls`
- `response_format`
- `stream`
- `stream_options.include_usage`

`max_tokens` 与 `max_completion_tokens` 互斥，内部统一为 `MaxOutputTokens`。两者都省略时不向上游写入输出上限，由上游采用模型默认值。已有字段的数值和大小限制保持向后兼容；未知顶层字段继续返回 `unsupported_parameter`。

### Chat 消息

- `system`、`developer`、`user`：必须具有允许的文本内容；现有安全媒体输入能力保持原契约。
- `assistant`：允许文本、`tool_calls` 或两者同时存在；存在工具调用时允许 `content:null`。
- `tool`：必须具有非空 `tool_call_id` 和输出内容。
- `developer` 在规范化层保留角色；当前上游编码时降级为 `system`，避免依赖供应商是否识别该角色。
- assistant 工具调用严格接受 `id`、固定 `type:function` 和 `function{name,arguments}`。
- 工具结果不得携带未定义的业务字段。

工具调用参数是模型生成的原始 UTF-8 字符串。网关只做类型与大小验证，不要求它是合法 JSON，也不解析、修复或记录其内容。这样可以确保网关输出的调用在下一轮能够原样回传。

### Chat 工具选择

支持：

- `"none"`
- `"auto"`
- `"required"`
- 指定 function 名称的对象形式

`tool_choice` 省略时等价于 `auto`。Chat 的 `parallel_tool_calls` 省略时不向上游发送该字段，以保留现有上游默认行为；Responses 省略时采用其公开默认值 `true` 并向上游显式发送。显式值必须为布尔值。一次 assistant 响应可以包含多个工具调用。

## Responses 请求契约

新增 `POST /v1/responses`。首期允许字段为：

- `model`
- `instructions`
- `input`
- `tools`
- `tool_choice`
- `parallel_tool_calls`
- `max_output_tokens`
- `temperature`
- `top_p`
- `stream`
- `store`
- `previous_response_id`，仅允许省略或 `null`

未知字段和不在首期范围的 item type 均按公开稳定错误拒绝，不允许静默丢弃。

### Responses 输入

`input` 支持：

1. 字符串，规范化为一条 user 文本消息。
2. `message` item，角色限 `system`、`developer`、`user`、`assistant`，内容限文本。
3. `function_call` item，包含 `call_id`、`name` 和原始 `arguments` 字符串；允许客户端原样带回可选 `id` 与 `status`，两者经类型/枚举校验后丢弃，不发送给上游。
4. `function_call_output` item，包含 `call_id` 和文本输出；可选 `id` 与 `status` 采用相同的校验后丢弃规则。

相邻且尚未产生输出的多个 `function_call` 规范化为同一并行 assistant 调用组；对应的 `function_call_output` 规范化为 tool 消息。`instructions` 作为高优先级 instruction 消息加入上游上下文。

### Responses 无状态规则

- `store` 省略或为 `false`：正常执行，响应始终返回 `store:false`。
- `store:true`：`400 unsupported_parameter`，且上游调用次数为零。
- `previous_response_id` 省略或为 `null`：正常执行。
- 非空 `previous_response_id`：`400 unsupported_parameter`，且上游调用次数为零。
- 返回的 `resp_*`、`msg_*`、`fc_*` 仅用于本次响应内关联和排障，不创建可查询资源。
- 客户端必须在后续请求中完整回传需要的消息、`function_call` 与 `function_call_output`。

### Responses 工具定义

Responses 的扁平 function 定义转换为与 Chat 嵌套 function 定义相同的 `ToolDefinition`。只接受 `type:function`、名称、可选描述、JSON Object `parameters` 和兼容的 strict 标记；任何托管工具类型均被拒绝。

## 工具调用关联状态机

关联检查在单个无状态请求携带的完整上下文内执行：

```text
assistant tool_calls / function_call -> OPEN
OPEN + 部分并行结果             -> PARTIAL
OPEN/PARTIAL + 所有匹配结果      -> COMPLETE
COMPLETE                         -> 允许下一条 user/assistant 消息
```

规则：

1. `call_id`/Chat `tool_calls[].id` 必须为受限 ASCII，长度 `1–128`。
2. 同一请求中的调用 ID 不得重复声明。
3. 每个 tool output 必须对应上下文中先前处于 OPEN 状态的调用。
4. 同一调用只能完成一次。
5. 并行结果可以按任意顺序回传。
6. 未完成整组并行调用前不得出现新的 user/assistant 消息。
7. 无对应声明、结果先于声明、重复结果、跨组复用 ID，或请求结束时仍有未完成调用，均返回 `invalid_request`。
8. Chat 和 Responses 必须调用同一个状态机实现。

限制：

- 单次最多 32 个工具定义。
- 单个 assistant/response 最多 64 个并行调用。
- 单个 `arguments` 最大 256 KiB。
- 单个工具输出最大 1 MiB。
- 总请求继续受现有 12 MiB 上限约束。

## 上游编码

规范化请求编码为现有 JieKou AI Chat Completions 请求：

- Responses `instructions` 前置为 system 消息。
- `developer` 降级为 system。
- Responses `function_call` 转为 assistant `tool_calls`。
- Responses `function_call_output` 转为带 `tool_call_id` 的 tool 消息。
- Responses 扁平工具定义转为 Chat 嵌套 function 定义。
- `max_output_tokens` 转为 `max_tokens`。
- `tool_choice` 和 `parallel_tool_calls` 按等价 Chat 形式发送。
- 不发送 `store`、`previous_response_id`、Responses item ID 或任何客户端 Gateway Token。

编码器从规范化类型生成全新的 JSON，不修改或合并客户端原始 JSON，防止未知字段绕过白名单。

## 非流式响应

### Chat

保持既有 `chat.completion` 输出合同，扩展 assistant message，使其可以合法返回 `content:null` 与一个或多个 `tool_calls`。只有存在至少一个合法工具调用时才允许 null content。上游供应商扩展字段继续被丢弃。

### Responses

返回标准 `object:"response"`，至少包含：

- 随机 `resp_*` ID
- `status:"completed"`
- 实际逻辑模型 ID
- `output` item 列表
- `parallel_tool_calls`
- `store:false`
- 规范化 usage

文本输出生成 `message`/`output_text` item。工具调用生成独立 `function_call` item，使用随机 `fc_*` item ID并保留上游 `call_id`。一次响应可以同时包含文本和多个函数调用。

## SSE 输出

### Chat SSE

保持既有 OpenAI chunk 与 `[DONE]` 合同，允许 `delta.tool_calls` 的多个 index、ID、函数名和参数增量。上游未知字段继续丢弃。流启动后的错误保留既有脱敏 error 帧并以 `[DONE]` 终止，避免破坏当前客户端。

### Responses SSE

在收到并验证第一个上游数据帧之前不发送 `response.created`，以保留“首帧前失败返回 HTTP JSON 错误”的现有安全性质。第一个上游帧有效后依次输出：

1. `response.created`
2. `response.in_progress`
3. `response.output_item.added`
4. 文本的 `response.content_part.added`、`response.output_text.delta/done` 与 `response.content_part.done`
5. 工具的 `response.function_call_arguments.delta/done`
6. `response.output_item.done`
7. `response.completed`

每个事件带从 1 开始严格递增的 `sequence_number`。工具调用的 ID 或名称尚未完整出现时，转换器可以做有界缓冲，但不得在不同 tool index 之间混合参数。Responses 流以 `response.completed` 结束，不发送 `[DONE]`。

上游在 Responses 流启动后失败时发送单个终态 `response.failed`；事件仅包含公开脱敏错误。客户端断开必须取消上游 context，禁止继续读取、生成事件或持久化任何内容。

## 错误契约

| 场景 | 公开结果 |
| --- | --- |
| 无效、重复、未声明或未完成的 call ID | `400 invalid_request` |
| 不支持的字段、item、`store:true`、非空 previous ID | `400 unsupported_parameter` |
| 请求、参数或工具输出超过限制 | `413 request_too_large` |
| Gateway Token 无效、过期或撤销 | `401 authentication_error` |
| IP、模型或 owner ACL 拒绝 | `403 permission_error` |
| 模型不在可用目录 | `404 model_unavailable` |
| 首个流事件前上游失败 | `503` JSON 安全错误 |
| Chat 流启动后失败 | 既有脱敏 error 帧和 `[DONE]` |
| Responses 流启动后失败 | `response.failed` 终态事件 |

鉴权继续发生在解析大请求体之前。所有解析、ACL 或关联失败都必须证明上游调用次数为零。

## 安全与隐私

- 客户端 `sk-gw-*` 只用于本平台鉴权，绝不发送给上游。
- 每次请求重新读取 Token、owner、模型和 IP ACL；不缓存授权结论跨请求复用。
- 不记录提示词、文本正文、工具参数、工具输出或完整调用 ID。
- 允许日志字段限请求 ID、模型、工具/调用数量、参数与输出字节数、公开错误类别和耗时。
- 测试使用敏感哨兵证明请求、上游扩展字段、错误消息和日志均不泄露。
- 参数和工具输出只作为有界 UTF-8 字符串处理；业务 JSON 解释权属于客户端工具执行器。
- 所有上游非流式响应和 SSE 帧继续经过显式字段投影，不透传供应商 header、错误正文或未知 JSON。
- 无状态实现不得创建 DB/Redis 写入，也不得复用平台网页登录会话持久化。

## 向后兼容

- `/v1/models`、`/v1/models/:id`、`/v1/models/detail` 保持不变。
- 现有不带工具的 Chat 请求和响应必须保持可执行兼容。
- 现有 Chat SSE 正常帧、错误帧和 `[DONE]` 顺序保持不变。
- 现有严格拒绝未知字段的原则保持；新增字段仅限本文明确列出的兼容字段。
- `/api/v1/platform/**` 的 JSON、业务 SSE、会话保存、quota 和 billing 行为保持不变。
- 该功能无数据库迁移，应用回滚不会面对 schema 差异。

## 测试与验收

### 单元与属性测试

- Chat 与 Responses 等价输入映射为相同规范化结构。
- system/developer/user/assistant/tool 的允许和拒绝组合。
- 单工具、多工具、并行乱序、重复结果、缺失声明、重复声明和部分完成。
- 模型生成非 JSON arguments 后能够原样回传。
- 两种工具定义和 tool choice 的等价转换。
- `store`、previous ID、托管工具、未知字段和所有大小边界。
- 随机 ID 格式、唯一性与零持久化依赖。

### Handler 与伪上游测试

- 使用真实 Gateway Token service 和伪上游完成两轮工具循环。
- 鉴权、ACL、目录和关联失败均断言零上游调用。
- 非流式纯文本、纯工具和文本加并行工具输出。
- Chat 与 Responses 的请求投影不携带客户端密钥或未知字段。
- 客户端取消传播到上游。

### SSE 测试

- 任意网络分片、CRLF、多行 data、工具参数跨帧和多 index 交错。
- Responses sequence number 严格单调。
- 文本与函数调用 item 的事件开始、增量、完成顺序。
- 首帧前错误保持 HTTP JSON；首帧后仅发送协议对应终态。
- Chat 既有帧和错误合同逐字节回归。

### 安全与回归门禁

- `go test ./internal/openaicompat ./internal/whitelabel ./internal/handler ./internal/router -count=1`
- 上述受影响包 race 测试。
- `go test ./... -count=1`
- `go vet ./...`
- `go build ./...`
- `git diff --check`
- 敏感哨兵与公开错误 envelope 扫描。
- 现有 `/api/v1/platform/**` focused regression。

### OpenCode 验收

1. 以固定 JSON fixture 冻结 OpenCode 当前 Chat provider 和 Responses provider 的请求形状。
2. 本地伪上游执行“读取文件 -> 返回工具结果 -> 模型最终文本”完整两轮。
3. 本地伪上游执行两个并行工具调用，乱序回传结果并获得最终回答。
4. 完成代码、规格和安全复审后，才准备真实 OpenCode 会话。
5. 真实上游验收只执行无副作用读取和命令，使用单独批准的调用预算；本设计或本地 PASS 不授权付费调用、部署或生产变更。

## 交付顺序

1. 冻结 Chat 工具消息和 Responses 请求/响应合同测试。
2. 建立规范化类型与关联状态机。
3. 扩展 Chat 请求、非流响应和 SSE 投影。
4. 实现 Responses 请求转换与非流输出。
5. 实现 Responses SSE 转换和取消。
6. 补齐 handler/router 与 OpenCode fixture 测试。
7. 运行全量、race、build、vet、diff 和安全门禁。
8. 独立规格与安全复审后准备真实 OpenCode 联调计划。

## 完成判定

只有以下条件全部满足，才能将该兼容子项目标记为实现完成：

- Chat 和 Responses 的文本及单/并行函数工具两轮闭环通过。
- OpenCode Chat provider 与 Responses provider fixture 均通过。
- 所有失败路径证明零越权上游调用和零敏感日志泄露。
- 现有 Gateway、WhiteLabel 和 Platform Chat 回归通过。
- 受影响 race、全仓测试、build、vet 和 diff 通过。
- 规格与安全复审覆盖最终提交。

生产部署、真实付费上游调用和外部客户端生产验收是后续单独授权阶段，不能由本地实现结果替代。

## 参考

- OpenCode custom provider 使用 `@ai-sdk/openai-compatible` 对接 `/v1/chat/completions`，Responses provider 使用 OpenAI provider：https://opencode.ai/docs/providers
- OpenAI Responses SSE 事件结构：https://platform.openai.com/docs/api-reference/responses-streaming
- OpenAI Responses 应用状态与 `store` 数据控制：https://platform.openai.com/docs/models/default-usage-policies-by-endpoint
