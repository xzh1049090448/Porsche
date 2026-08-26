# 带斜杠模型 ID 与空目录契约修复设计

## 目标

解决 Issue #2：使 Porsche 能安全展示、授权和调用上游的 `组织/模型` ID（例如 `zai-org/glm-5.1`、`deepseek/deepseek-v4-pro`），并保证空模型目录返回 JSON 数组而非 `null`。

## 范围

- 支持安全的模型 ID 字符集，其中 `/` 仅作为组织与模型名称之间的路径语义分隔符。
- 覆盖白牌目录、上游详情、平台模型目录/详情/Chat/Compare、公开 `/v1/models` 及其详情、管理员模型健康检查和 Porsche-Web 模型详情请求。
- 白名单、用户 ACL、Gateway Token ACL、审计和计费仍保存上游原始模型 ID 字符串；不新增模型映射表或数据库迁移。
- 目录 API 的 `data` 始终序列化为数组。

## 安全模型

模型 ID 必须是非空 UTF-8 文本，限制最大长度，并且：

- 允许普通模型标识符与单个或多个由 `/` 分隔的非空段；
- 拒绝反斜杠、`?`、`#`、`%`、控制字符、空白、`.` 或 `..` 路径段；
- 上游详情 URL 使用每个 ID 的完整单段 `url.PathEscape`，不得由字符串拼接产生额外路径、查询参数或主机；
- 本地 HTTP 路由不将原始斜杠 ID 直接交给 Gin `:id` 参数。详情请求使用查询参数 `?id=<URL-encoded-model-id>`，并在服务层执行相同的安全校验和 ACL 校验；
- 不存在、未授权、被目录移除或不安全的 ID 对外均返回相同的不可用响应，不产生模型枚举信息。

## API 契约

- `GET /api/v1/platform/models`：保持 `{data: Model[], catalog_stale: boolean}`，`data` 永远为数组。
- `GET /api/v1/platform/models/detail?id=<encoded-id>`：替代旧 `:id` 详情路由；仅返回当前目录、服务白名单和用户 ACL 的交集。
- `GET /v1/models/detail?id=<encoded-id>`：替代公开 Gateway 详情路由；仅返回当前目录、服务白名单和 Token ACL 的交集。
- `POST /admin/models/health-check?id=<encoded-id>`：管理员健康检查使用同一安全 ID 解析和目录门禁。
- 旧 `.../models/:id` 详情端点对无斜杠旧 ID 保持兼容；新前端统一改用 `detail?id=`。

## 执行顺序

1. 更新模型 ID 校验、规范化、目录 clone 与服务级详情安全测试。
2. 改造平台/Gateway/管理员详情路由，确保授权发生在上游调用前。
3. 改造 Porsche-Web API Adapter 和模型详情请求，仅传递 URLSearchParams 编码的 ID。
4. 清理过时本地任务项，将 `go-004` 作为唯一最高优先级任务关联 Issue #2。
5. 执行后端/前端测试、构建和真实上游目录、详情、Chat 与 SSE 冒烟。

## 验收标准

- 配置真实 `组织/模型` ID 后，模型目录包含这些模型，且空目录返回 `data: []`。
- 平台、Gateway 和管理员详情可获取授权的 slash ID，不安全 ID 与未授权 ID 不触发上游详情请求。
- 白名单、用户 ACL、Gateway Token ACL 对 slash ID 的交集语义与普通 ID 一致。
- 非流式 Chat、SSE 和 Compare 可使用授权 slash ID。
- 后端 `go test ./...`、前端 `npm test` 与 `npm run build` 通过；真实上游 smoke 通过后才能关闭 Issue #2。
