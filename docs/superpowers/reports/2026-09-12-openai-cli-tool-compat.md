# OpenAI CLI 工具调用兼容实现报告

## 结论

状态：`BLOCKED_FIXTURE`

当前代码完成了既定首期范围，并通过无数据库单元测试、回归、竞态、构建、静态检查和协议对抗探针。依赖隔离 MySQL 的 5 个真实 handler 闭环用例由于未配置 `TEST_DATABASE_URL` 全部跳过，因此不能标记为完整实现验收通过。

实现代码头为 `6ce54bb`；本报告和进度记录属于后续文档收尾提交。

## 已实现范围

- `/v1/chat/completions`：assistant `tool_calls`、tool result、单/并行函数调用往返，保留既有 Chat SSE 与 `[DONE]` 合同。
- `/v1/responses`：无状态文本、消息、`function_call`、`function_call_output`、单/并行函数工具、非流式输出和 Responses SSE。
- 公共验证：严格 JSON、调用 ID 状态机、函数定义和工具选择一致性、资源上限、未知或不支持字段拒绝。
- 上游边界：从规范化结构重新编码允许字段，拒绝向上游透传 Gateway token、Responses 状态字段和未知嵌套字段。
- 输出边界：仅从 WhiteLabel 投影后的数据构造公共响应；Responses SSE 使用严格递增序号与协议终态，不发送 `[DONE]`。
- 路由与错误：注册 `POST /v1/responses`；403 ACL 拒绝返回稳定 `permission_error`。
- OpenCode fixture：冻结 Chat provider 与 Responses provider 的已脱敏请求形状。

## 验证证据

以下命令于 2026-09-12 在 `feature/openai-cli-tool-compat` 上执行并退出 0：

```text
GOCACHE=/private/tmp/porsche-openai-cli-go-cache go test -race ./internal/openaicompat ./internal/whitelabel ./internal/handler ./internal/router -count=1
GOCACHE=/private/tmp/porsche-openai-cli-go-cache go test ./... -count=1
GOCACHE=/private/tmp/porsche-openai-cli-go-cache go vet ./...
GOCACHE=/private/tmp/porsche-openai-cli-go-cache go build ./...
git diff --check origin/main...HEAD
```

额外协议探针确认：

```text
response_format decode=invalid_request encode=invalid_request leaked=false
buffered_arguments emitted="AB" want="AB"
invalid_first_chunk err=invalid Responses stream chunk events_before_error=0
```

敏感数据扫描只命中测试中的 deliberate sentinel 和设计中说明的 `sk-gw-` 公开前缀；Responses 事件扫描确认其终态为 `response.completed` 或 `response.failed`，`[DONE]` 仅保留在既有 Chat 路径。

## 未闭环门禁

以下测试均以 `requires isolated TEST_DATABASE_URL MySQL fixture` 跳过：

- `TestGatewayResponsesRejectsStateBeforeUpstream`
- `TestGatewayChatToolRoundTrip`
- `TestGatewayResponsesToolRoundTrip`
- `TestGatewayResponsesStreamUsesResponsesEventsWithoutDoneSentinel`
- `TestGatewayResponsesPostStartFailureEmitsFailed`

还未执行独立规格复审、独立安全复审、真实 OpenCode/Codex 会话、真实付费上游调用、push、PR、merge、部署或生产验收。

## 后续兼容 TODO

后续维护 Codex、OpenCode 和其他 CLI 的客户端版本/协议矩阵；逐步评估 Responses reasoning item、客户端专属可选字段和模型能力差异；保存脱敏真实请求 fixture，结合伪上游回放和真实客户端准入测试持续扩展兼容范围。该 TODO 不改变本轮首期无状态文本与自定义函数工具范围。
