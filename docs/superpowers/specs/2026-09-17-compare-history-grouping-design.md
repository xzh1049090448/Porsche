# 模型对比历史聚合恢复设计

**日期：** 2026-09-17  
**状态：** 方案 A 已选定，待规格确认  
**关联问题：** Porsche-Web #10  
**仓库：** Porsche-Web 与 Porsche  
**前端基线：** `04039a74554354307631f9ceb378709db2b454ca`  
**后端基线：** `08f657680cb141afc5281d45c3d796a2013b23fa`

## 1. 问题与目标

`platform-chat-sse.v2` 的模型对比在生成期间由前端显示为一条 `multiModel` 聚合消息。后端为了保持每个模型结果的恢复、计费和幂等语义，会把成功结果分别持久化为独立 assistant 消息，并在 `platform_chat_generation_receipts` 与 `platform_chat_generation_results` 中保存一次生成的分组关系。

用户退出并重新登录后，`GET /api/v1/conversations/{guid}` 只返回扁平消息。前端只能识别旧协议的 `__MULTI_MODEL__` 标记，无法从 v2 扁平消息恢复分组，因此把一次对比展示成多个独立对话框。

本任务采用方案 A：后端从现有 receipt/result 数据投影显式分组元数据，前端按消息 GUID 做确定性重建。修复后的目标行为是：

- 当前生成和重新登录后的历史展示一致；
- 一次 compare v2 生成始终显示为一条多模型聚合回复；
- single 消息、旧 `__MULTI_MODEL__` 消息及旧历史保持兼容；
- 恢复流程继续按真实 assistant GUID 校验，不改变生成、计费或持久化语义；
- 分组数据异常时保留原始消息，绝不通过邻接猜测合并或丢弃内容。

## 2. 范围与非目标

本次修改包括：

- Porsche 的会话详情读取投影与 JSON 契约；
- Porsche-Web 的会话映射、历史展示投影和生成恢复匹配；
- 双方契约文件、单元/契约测试和进度证据。

本次不修改：

- compare v2 的写入格式、SSE 事件或生成控制接口；
- 数据库表、迁移、计费、配额和上游调用；
- 已持久化消息内容，不回填或重写历史数据；
- 页面视觉设计、部署脚本和生产配置。

## 3. 后端设计

### 3.1 会话详情投影

新增会话详情服务投影。服务先按当前用户和会话 GUID 加载会话及消息，再读取同一 `user_id`、`conversation_id`、`mode=compare`、`is_deleted=0` 的 generation receipts 和对应 results。详细响应在既有字段之外增加 `generation_groups`：

```json
{
  "guid": "353589505447432192",
  "title": "示例对话",
  "model": null,
  "created_at": "2026-09-17T00:00:00.000Z",
  "updated_at": "2026-09-17T00:00:00.000Z",
  "messages": [],
  "generation_groups": [
    {
      "generation_id": "01234567-89ab-4cde-8f01-23456789abcd",
      "mode": "compare",
      "user_message_guid": "353589505447432193",
      "results": [
        {
          "model": "model-a",
          "status": "completed",
          "assistant_message_guid": "353589505447432194",
          "tokens": 32,
          "error_code": null
        },
        {
          "model": "model-b",
          "status": "failed",
          "assistant_message_guid": null,
          "tokens": 0,
          "error_code": "upstream_error"
        }
      ]
    }
  ]
}
```

字段约束如下：

- `generation_groups` 只出现在包含消息的详细响应中；列表和创建响应保持不变。GET 详情和返回详细会话的 PUT 响应使用同一结构。
- 数组只包含 `mode=compare` 的已提交 generation。single generation 不进入该数组。
- `generation_id` 是规范小写 UUID；`mode` 固定为 `compare`。
- `user_message_guid`、`assistant_message_guid` 是十进制字符串，绝不暴露数据库内部 ID。
- `results` 按原始 `model_index` 升序返回；模型必须唯一，数量为 2 至 3。
- completed 结果必须有 assistant GUID、非负 tokens、`error_code=null`。
- failed 结果必须有 `assistant_message_guid=null`、`tokens=0` 和稳定错误码。
- groups 按 `committed_at`、receipt ID 升序形成确定顺序；消息本身改为 `created_at`、message ID 升序，避免同毫秒时间戳导致顺序漂移。内部 ID 只用于排序和校验，不进入 JSON。

### 3.2 所有权、完整性和失败行为

读取必须绑定当前认证用户与当前会话，禁止只凭 generation UUID 或 receipt ID 取数。服务批量读取 receipts、results，并用已加载消息的内部 ID 建立只读索引，校验：

- receipt 的用户、会话、用户消息、审计字段、提交时间和成功计数；
- user message 确属当前会话且角色为 user；
- result 的连续 model index、唯一模型、状态字段和审计字段；
- completed result 引用的 assistant 确属当前会话，且模型、tokens、角色和消息内容满足现有 receipt reader 规则；
- failed result 不引用 assistant；
- 同一 assistant message 不可被多个 result 或 group 重复引用。

数据库查询失败时，详情接口返回现有的内部错误，不返回部分构造结果。单个持久化 group 的完整性校验失败时，仅省略该 group，仍返回原始 `messages`；这样前端会退回当前的分离显示，避免把不相关消息错误合并或令整个历史不可读。实现应记录不含提示词、回复正文、凭据和内部 ID 的诊断信息。

本设计复用现有 receipt/result 表，不新增数据库迁移，也不改变 `LoadPlatformGenerationReceipt` 的生成恢复职责。

### 3.3 Handler 与 DTO 边界

会话 service 返回“会话 + compare groups”的专用详情结果。DTO 只序列化已校验的公开投影；handler 不直接查询 generation 表，也不在 DTO 中访问数据库。

`GET /api/v1/conversations/{guid}` 和 `PUT /api/v1/conversations/{guid}` 的详细响应调用新投影。列表、创建、删除和 Markdown 导出行为保持原样。

## 4. 前端设计

### 4.1 映射与确定性校验

`mapConversation` 先按现有规则生成完整的扁平 `rawMessages`，再验证 `generation_groups`。一个 group 只有在以下条件全部满足时才参与聚合：

- generation ID、mode、模型数量、状态和字段组合符合契约；
- user GUID 在 `rawMessages` 中唯一匹配 user 消息；
- 每个 completed assistant GUID 在 `rawMessages` 中唯一匹配 assistant，且 model、tokens 一致；
- failed result 不声称拥有 assistant；
- group 内及 group 间不重复占用 user 或 assistant GUID。

不得使用“连续三条 assistant”“相同时间”或模型名相邻等启发式规则。任一校验失败时，该 group 不做聚合，其原始消息原样保留。

### 4.2 展示投影

对有效 group，前端只替换该 group 精确引用的 completed assistant 消息，并在第一个被引用 assistant 的位置插入一条稳定的 `multiModel` 消息：

- `guid` 使用第一个 completed assistant GUID，`generationId` 保存 generation UUID；
- `models` 严格使用 `results` 的契约顺序；
- `replies` 为每个模型建立键，completed 使用原消息内容，failed 使用空字符串；
- `modelStates` 映射为 `{ status, code }`；
- `contextReplies` 仅包含 completed 模型，保持后续上下文行为与实时生成一致；
- `tokens` 是 completed 结果 tokens 之和；
- `sourceAssistantGuids` 保存已折叠的 assistant GUID，供调试和确定性测试使用；
- `content` 为 `null`，继续使用现有多模型组件渲染。

用户消息和所有未被有效 group 引用的消息保持原相对顺序。旧 `__MULTI_MODEL__` 标记仍由 `enrichMessage` 处理；新的 group 投影不重新编码或改写服务器内容。

### 4.3 原始消息与恢复流程

存在至少一个 generation group 时，会话对象同时保留：

- `rawMessages`：后端返回的真实逐模型消息；
- `messages`：供 UI 和后续上下文使用的聚合展示消息。

`matchRecoveredAttempt` 必须优先在 `rawMessages` 中按 assistant GUID 校验恢复结果；没有 `rawMessages` 时回退到 `messages`，以兼容 single、旧响应和测试 fixture。这样不会破坏 compare v2 当前的精确 GUID、内容、模型和 tokens 校验。

`projectConversationForPersistence` 必须移除 `rawMessages`，避免 mock/localStorage 重复保存服务器历史或扩大存储；其余持久化投影规则保持不变。服务器模式仍以详情 API 为事实来源。

## 5. 契约兼容与版本

Porsche-Web 的 `interface-contract.json` 需要把 `conversation_detail` 及 `conversation_title` 的响应从笼统的 “Conversation with messages” 更新为明确结构，并记录：

- `generation_groups` 是向后兼容的新增字段；
- 老后端缺少该字段时，前端继续显示扁平消息；
- 老前端会忽略新增字段；
- 数组顺序、GUID 类型、状态组合和错误码约束；
- 当前修改只表示 agreed-for-implementation，真实环境联合验收完成前不得标记为 production accepted。

若仓库存在对应的后端版本化契约文件，同步更新同一字段定义。双方协调记录必须绑定最终前后端 revision 与相同契约内容哈希。

## 6. 测试策略

实现采用 TDD，先增加失败回归，再写生产代码。

后端至少覆盖：

- 一次全成功 compare 投影为一个有序 group；
- 部分失败保留失败模型、空 assistant GUID、零 tokens 和稳定错误码；
- single receipt 不进入 groups；
- 跨用户、跨会话、重复消息引用和损坏结果不能泄露或错误聚合；
- 同毫秒消息按 message ID 稳定排序；
- 数据库错误返回失败，完整性异常省略对应 group 且原始消息仍存在；
- 列表/创建响应不新增 groups，GET/PUT 详情契约一致；
- JSON 不包含 receipt/result/message 的内部数据库 ID。

前端至少覆盖：

- 有效全成功和部分失败 group 重建为一条 `multiModel` 消息；
- 模型顺序、reply、state、context、tokens 与契约一致；
- 缺失、重复、跨 group 或字段不匹配的 GUID 不聚合且不丢消息；
- single 与旧 marker 历史保持不变；
- logout/login 后重新加载详情仍显示一个聚合回复；
- 生成恢复继续用 `rawMessages` 精确匹配每个 assistant GUID；
- 本地持久化投影不包含 `rawMessages`。

最终验证包括前端 `npm test` 与生产构建、后端定向测试与 `go test ./...`、`go vet ./...`、JSON/差异检查，以及登录后真实浏览器回归。生产部署与线上写操作需要独立授权和部署证据。

## 7. 完成条件

只有以下条件全部满足，Issue #10 才能关闭：

1. 两仓实现、测试和接口契约在各自最终 revision 上通过规定门禁；
2. 前后端协调者对相同契约版本和 revision 完成书面确认；
3. 测试环境验证新建 compare、退出登录、重新登录、打开同一历史对话后仍为单个聚合视图；
4. single、旧 marker、部分失败和生成恢复回归通过；
5. 未出现消息丢失、错误合并、跨用户泄露、重复计费或数据库迁移。
