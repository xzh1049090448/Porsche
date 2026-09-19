# DeepSeek Harness 请求字段兼容与受控上游透传实现报告

## 结论

状态：`PASS_LIMITED_SCOPE`

对应 GitHub issue #18（`兼容 DeepSeek Harness 请求字段并建立受控上游透传策略`），归属 `go-018` 的 Gateway/CLI 兼容子项目。设计与计划见 `docs/superpowers/specs/2026-09-19-deepseek-harness-field-compat-design.md` 与 `docs/superpowers/plans/2026-09-19-deepseek-harness-field-compat.md`。

- 受审 revision：`1f60fbf9b8c56bb4577fec8d5b9d86b1392856e8`
- baseline：`25fe55c182691ca7a5c8e5ea3ae21b3961bb3e32`（sha256 `68c4fe160e344e36b83bd9624c9b1a6397dbe62e43b5ee12e94e16eea5421881`）
- review snapshot：`f8031ca84266e1999b6d2acab4c5de1248f9fb077cb0f78cc5f1c09c3d6e9b5b`
- 分支/PR：`feature/deepseek-harness-field-compat` → #19

本报告与 `progress.md` / `feature_list.json` 的门禁记录属于受审 revision 之后的**文档收尾提交**，不改变受审代码；快照仍然绑定 `1f60fbf`。

## 已实现范围

- Chat `/v1/chat/completions`：新增顶层 `reasoning_effort`（`minimal|low|medium|high|max`）与 `thinking`（`{type:"enabled"|"disabled"}`），assistant 消息 `reasoning_content`（仅 assistant、合法 UTF-8、≤ 1 MiB）。
- Responses `/v1/responses`：新增 `reasoning.effort` 映射；`store:true` 与非空 `previous_response_id` 保持拒绝。
- 结构化解析与显式白名单保持不变：未知顶层 `400 unsupported_parameter`、已知字段形状/枚举错误 `400 invalid_request`、超限 `413 request_too_large`。
- 能力门禁：`JIEKOU_REASONING_MODELS`（精确 ID 或 `re:` RE2）。未声明模型携带思考字段时 `400 unsupported_parameter` 且零上游调用；默认空等于原行为。
- 敏感字段默认过滤：`openaicompat.SanitizePassthrough` 删除固定 deny list（`user`、`metadata`、`safety_identifier`、`service_tier`、`inference_geo`、`speed`、`store`、`previous_response_id`、`prompt_cache_key`、`logit_bias`、`logprobs`、`top_logprobs`、`api_key`、`authorization`、`secret`、`password`、`credentials`、`provider` 与 `stream_options.include_obfuscation`），仅删除、不新增，空 `stream_options` 整体移除。
- 受控原始透传：`JIEKOU_PASSTHROUGH_MODELS` 显式声明，仅 `POST /v1/chat/completions`，保留 12 MiB、Bearer 鉴权、`application/json`、模型/目录/IP ACL，输出无内容审计日志；默认空表示从不透传。
- 响应与用量：非流式 message 与 SSE delta 保留 `reasoning_content`；usage 新增可选 `completion_tokens_details.reasoning_tokens`、`prompt_tokens_details.cached_tokens`、`prompt_cache_hit_tokens`、`prompt_cache_miss_tokens`；聚合计数字段语义不变。
- 无数据库迁移；不改 Gateway Token 鉴权/ACL/错误 envelope/SSE 帧顺序；不改 `/api/v1/platform/**` 请求契约。

## 验证证据

隔离 disposable MySQL 8.0.46（`--tmpfs`、loopback-only、无命名卷）+ Redis 7（`--tmpfs`、loopback-only），迁移 `0001–0020`，`GOCACHE=/private/tmp/porsche-issue18-go-cache`：

```text
go test ./internal/openaicompat ./internal/whitelabel ./internal/config ./internal/handler -count=1   # exit 0
go test -race ./internal/openaicompat ./internal/whitelabel ./internal/config ./internal/handler -count=1   # exit 0
go test -p 1 ./... -count=1   # 除 pre-existing internal/migration 用例外全部 PASS
go vet ./...   # exit 0
go build ./... # exit 0
git diff --check   # clean
gofmt -l cmd internal   # 无输出
```

5 个新增真实 MySQL Handler 用例全部 PASS、零 skip：`TestGatewayChatDeepSeekHarnessReasoningRoundTrip`、`TestGatewayChatRejectsReasoningForUndeclaredModel`、`TestGatewayChatPassthroughSanitizesSensitiveFieldsAndAudits`、`TestGatewayChatPassthroughDisabledRejectsUnknownField`、`TestGatewayChatPassthroughDeniedModelNeverReachesUpstream`。

## Pre-existing 失败（与本变更无关）

`internal/migration` 的 `TestBusinessGroupMigrationOnIsolatedMySQL/backfills_active_and_tombstoned_users`、`TestPublicPriceDraftStateMigrationRealMySQLDownAndReapply`、`TestPublicRenderJobTerminalMigrationRealMySQLDownAndReapply`、`TestUpstreamMonitorLeaseMigrationRealMySQLDownAndReapply` 在未改动的基线 `69f55c1` 上以相同错误失败（证据 `baseline-migration4.log` 与 `branch-migration4.log`）。本变更未新增迁移、未改 `internal/migration`。

## 独立门禁（同一 snapshot / revision，verify exit 0）

- 规格 `SPEC_PASS`：10 项需求逐项 PASS。
- 安全 `SECURITY_PASS`：无 Critical/High。3 项 Low + 1 项 Info：
  - L1 `internal/whitelabel/sse.go:337`：流式 `delta.reasoning_content` 未复用非流式的 UTF-8/≤1 MiB 显式校验；受 SSE 单行 1 MiB 上限与 `json.Unmarshal` 的 UTF-8 归一化缓解。
  - L2 `internal/whitelabel/types.go:180` / `sse.go:337`：响应侧 `reasoning_content` 未按 `role=="assistant"` 门禁；利用前提是可信上游被攻陷。请求侧已门禁（`chat_request.go:163`）。
  - L3（no-op）`internal/openaicompat/passthrough.go:96-113`：非对象 `stream_options` 原样转发，无法据此开启对象内 `include_obfuscation`；顶层 deny list 仍完整生效。
  - I1（spec，非安全）`internal/openaicompat/reasoning.go:105-122`：非能力模型收到 `reasoning:{}` 被接受而非 400；未转发任何 effort，也不暴露 CoT。
- 独立测试 `PASS`：verify exit 0；`openaicompat`/`whitelabel`/`config` 单元 0 fail、0 skip；`-race` 通过；5 个真实 MySQL Handler 用例 5 PASS / 0 FAIL / 0 SKIP；`go vet` clean；`git diff --check` clean，工作树无修改。

## 未运行与限定项

真实 JieKou 付费上游对 `reasoning_effort`/`thinking` 的支持、真实 DeepSeek Harness 端到端会话、平台路径推理控制、`/v1` 逐请求计费与 DB 审计、生产迁移/部署/生产验收均 `NOT_RUN`。本地隔离夹具与静态门禁不代表上游或生产验收。
