# 白牌上游接入设计

## 范围

实现 PRD-260820：以 JieKou AI OpenAI-compatible 白牌作为唯一推理上游，覆盖 Go 网关、平台 API 和 Porsche-Web 的动态模型界面。本设计不实现多渠道、审计、配额、组织/项目或账务；它们仍按 PRD-260819 后续文档推进。

## 架构

新增独立 `WhiteLabelService`，统一承担以下职责：

- 固定区域 Base URL、`JIEKOU_API_KEY` 与 `JIEKOU_ALLOWED_MODELS` 的 fail-closed 配置校验；
- 白牌模型目录（5 分钟）、详情（1 小时）缓存，以及 24 小时 stale 和可信 404 的状态机；
- 模型 ID、服务允许列表、用户/Gateway Token ACL 的交集；
- OpenAI Chat 请求的参数、大小、媒体 URL 校验，非流和 SSE 白牌调用；
- 统一错误映射、内部 request ID 与上游信息脱敏。

Gin handler 保留认证、HTTP 绑定和响应序列化。平台 API 与 Gateway API 都调用同一服务，前端始终调用本地平台 API，绝不直接访问白牌。

旧 `ModelRegistry`、`ModelRoute`、`KeyPool`、`config/models.yaml` 旧厂商路由和相应环境变量读取将被删除，且没有回退开关。白牌 `id` 作为规范逻辑模型 ID；后续多渠道只能建立逻辑 ID 到渠道上游 ID 的映射。

## API 与数据流

`GET /api/v1/platform/models` 与 `/v1/models` 从同一授权交集返回动态目录；各详情接口在授权后从缓存/白牌加载。平台 chat、compare 与公开 Gateway chat 先校验 Token/JWT、IP、模型 ACL、请求参数和大小，才向白牌发送请求。

对比支持最多三个模型。非流响应保留输入顺序和单模型错误；流式响应使用带模型 ID 的 `chunk`、`model_done`、`model_error` 多路 SSE 事件，所有模型结束才发送 `[DONE]`。普通 SSE 在首字节前失败时返回 JSON 502/503，首字节后发送已定稿的 `event: error` 帧和 `[DONE]`。

## 安全与兼容性

密钥只来自部署环境，不进入数据库、前端、日志、审计、错误或测试夹具。上游错误正文、域名和 request ID 不得出现在客户端响应。模型元数据按纯文本处理，媒体 URL 与 data URI 受 PRD 的协议、地址、MIME 和大小限制。MySQL 旧 Python 表不可修改或删除；自动化测试使用 SQLite 与 `httptest`。

## 验证与交付门禁

Explorer 先输出实际改动地图；Worker 与 Security 并行，随后 Test Engineer 执行全量验证。必须满足：无未解决 Critical/High 安全问题、Go `go test ./...` 通过、前端 `npm test` 与 `npm run build` 通过、`git diff --check` 通过。预发布以独立低权限 Key 和一个允许模型完成目录、详情、非流与 SSE 冒烟；真实密钥不得写入仓库。

## 非目标与后续演进

本期不做自动重试、渠道权重或故障切换。后续 PRD-260819-01 在 `WhiteLabelService` 后增加 Channel Selector；PRD-260819-02/03 分别引入审计/幂等与 Redis 配额。多渠道阶段以逻辑模型启用状态和至少一个可用渠道决定可用性，不再把白牌目录当作唯一事实源。
