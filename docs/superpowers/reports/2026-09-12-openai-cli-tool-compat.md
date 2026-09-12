# OpenAI CLI 工具调用兼容实现报告

## 结论

状态：`PENDING_REVIEW`

当前代码完成了既定首期范围，并通过单元测试、回归、竞态、构建、静态检查、协议对抗探针以及隔离 MySQL 上的真实 handler 闭环。首轮独立规格复审发现四项缺口，修复后全部本地门禁通过；新快照的规格、安全和测试复审尚未完成，因此当前不声明最终通过。

初始集成提交为 `28d8f1f`，规格修复代码头为 `1df776d`；本报告和进度记录属于后续文档收尾提交。

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

## 真实 handler 夹具证据

本批使用本地已有的 MySQL 8.0.46 镜像启动独立、tmpfs、无命名卷、仅绑定 loopback 随机端口的临时容器。专用数据库 `porsche_openai_cli_test` 完成 0001–0019 迁移。首次迁移尝试因测试环境缺少有效 `SNOWFLAKE_NODE_ID` 在连接业务流程前退出；补充测试专用节点号后，迁移和测试成功。

以下测试在 normal 与 race 下均 PASS、零 skip：

- `TestGatewayResponsesRejectsStateBeforeUpstream`
- `TestGatewayChatToolRoundTrip`
- `TestGatewayResponsesToolRoundTrip`
- `TestGatewayResponsesStreamUsesResponsesEventsWithoutDoneSentinel`
- `TestGatewayResponsesPostStartFailureEmitsFailed`

测试后精确停止 `porsche-openai-cli-260912-mysql`；容器因 `--rm` 自动删除，未创建命名卷。包含随机 MySQL 密码、测试 HMAC key 和运行脚本的 0700 私有目录已删除。

## 独立规格复审与修复

首轮独立规格复审绑定 final revision `28d8f1f` 和 snapshot `c3ddad2ccb3ee37d87d11597061b4aa99f79dd6e2de923097430307b6dc72b00`，结论为 `SPEC_FAIL`：

- 历史 tool arguments 或 tool output 超限错误地返回 400，而合同要求 413 `request_too_large`。
- assistant 同时包含工具调用时会静默吞掉非法 content shape。
- Responses 嵌套未知字段返回 `invalid_request`，而合同要求 `unsupported_parameter`。
- 缺少完整 Gin 到 WhiteLabel 的取消传播测试。

修复提交 `1df776d` 对四项均完成 RED→GREEN。隔离 MySQL 8.0.46 完成 0001–0019 迁移后，原 5 个真实 Handler 用例加 `TestGatewayToolPayloadLimitsRejectBeforeUpstream`、`TestGatewayResponsesCancellationStopsUpstream` 共 7 项在 race 下全部 PASS、零 skip；413 四个 decoder/四个认证 Gateway 子用例均通过并确认零上游，取消用例确认客户端 context 传播到伪上游。精确测试容器与私有凭据目录已清理。

修复后 snapshot `b9215d00b3ee7faf09d403de895267051d18dcaf9b28634587afa1df20d4aa06` 在 final `503f9d8` 上取得 `SPEC_PASS`。同一快照的安全复审返回 `SECURITY_FAIL`：Responses SSE 只限制单帧和单工具 arguments，未限制累计文本、工具 state 数量和稀疏大索引，异常上游可让单请求持续增长内存。

安全修复 `bb1bc88` 在写入缓冲前强制累计文本不超过 `MaxTextContentBytes`、工具索引位于 `[0, MaxParallelCalls)`、工具 state 不超过 `MaxParallelCalls`，并在拼接前检查累计 arguments 剩余额度。五个新增测试覆盖累计文本、超过 64 个工具、稀疏大索引、累计 arguments 及单一 `response.failed` 终态，均经历 RED→GREEN；affected race、全仓 test、vet、build 和 diff 通过。最终代码再次在独立 MySQL 8.0.46、0001–0019 下运行 7 个真实 Handler race 用例，全部 PASS、零 skip；容器和私有凭据已精确清理。该提交改变快照，必须重新开始规格、安全和测试复审。

第三快照 `d53bc599e72db0b11821235960701b0dc487b3a6aaf7e674f682b9a4bd535bbc` 在 final `b33b0f8` 上取得 `SPEC_PASS`。同一快照的安全复审确认硬上限有效，但发现文本和 arguments 的逐帧字符串拼接导致 O(n²) 复制与 GC 压力，再次返回 `SECURITY_FAIL`。

修复 `9fd6e57` 将累计文本和每个工具 arguments 改为指针 state 内的有界 `strings.Builder`，继续在 Write 前检查剩余额度，仅在完成事件生成最终字符串，delta 事件仍只发送当前片段。4096 个单字节文本/arguments 的分配门禁、精确上限完成内容和超限后缓冲不增长均经历 RED→GREEN；affected race 通过。该提交再次改变快照，必须从规格复审重新开始。

## 未闭环门禁

builder 安全修复后的新快照尚未完成规格复审、安全复审和独立测试复审。初始实现已进入远端 main；三轮修复提交尚未 push/merge。真实 OpenCode/Codex 会话、真实付费上游调用、部署和生产验收均未执行。

## 后续兼容 TODO

后续维护 Codex、OpenCode 和其他 CLI 的客户端版本/协议矩阵；逐步评估 Responses reasoning item、客户端专属可选字段和模型能力差异；保存脱敏真实请求 fixture，结合伪上游回放和真实客户端准入测试持续扩展兼容范围。该 TODO 不改变本轮首期无状态文本与自定义函数工具范围。
