# MySQL 持久化标准化与 RAG 移除设计

## 目标

将 Porsche 重构为仅使用 MySQL 8 的 AI 模型中转服务：以新版数据库建模与持久化约束重建全新 schema，彻底移除 RAG/知识库能力，并同步将前后端业务资源标识切换为雪花 `guid`。

这是新环境部署，不兼容已退役 Python 服务的表、数据、接口或迁移路径。

## 已确认的决策

- 运行、集成测试与迁移目标数据库仅为 MySQL 8；移除 SQLite 运行时支持。
- HTTP JSON、路径参数与前端状态中的业务资源标识统一使用 `guid` 字符串；内部主键 `id` 不对外暴露。
- 采用显式、版本化 migration runner；应用启动不执行 `AutoMigrate`，也不隐式建表或改表。
- 系统初始化、种子和无登录管理员身份的操作允许 `created_by` / `updated_by` 为 `NULL`，不得使用伪造用户或 `0`。
- 用户手机号、订单号、Token 哈希、模型健康模型名保持全局唯一；RAG 数据集已移除，无 slug 策略。
- 所有受控状态（用户、套餐、Token、订单、消息角色、用量类型）在数据库中保存为 `INT`，由 Go 显式映射。
- migration 元数据表同样完整遵守业务表的字段规范。
- 雪花节点号来自 `SNOWFLAKE_NODE_ID`，范围 0–1023；生产多实例必须唯一。
- RAG 完整移除；保留认证、会话/消息历史、用量、订单、平台 Chat/Compare/SSE 和 `context_window`。
- Porsche-Web 同批修改，移除知识库参数与界面，并使用 `guid` 字符串。
- 更新当前有效文档；历史 `docs/superpowers/specs/` 与 `plans/` 仅保留作审计记录。

## 数据模型

新 schema 包含：`users`、`conversations`、`messages`、`usage_records`、`orders`、`audit_logs`、`model_health`、`gateway_api_tokens` 与 `schema_migrations`。

每张表均具备以下列：

| 列 | 类型 | 约束 |
| --- | --- | --- |
| `id` | `BIGINT` | 内部自增主键；只用于外键和服务端内部查询。 |
| `guid` | `BIGINT` | 雪花算法生成；`NOT NULL UNIQUE`；对外业务标识。 |
| `created_at` | `BIGINT` | UTC Unix 毫秒。 |
| `created_by` | `BIGINT NULL` | 创建者 `users.id`；系统操作为 `NULL`。 |
| `updated_at` | `BIGINT` | UTC Unix 毫秒。 |
| `updated_by` | `BIGINT NULL` | 更新者 `users.id`；系统操作为 `NULL`。 |
| `is_deleted` | `INT` | `NOT NULL DEFAULT 0`；0 有效、1 已逻辑删除。 |

所有用户归属或审计关联均使用 `BIGINT user_id -> users.id`。常规查询、统计、授权和关联默认限定 `is_deleted = 0`。业务删除不执行物理 `DELETE`；会话删除在一个事务内同步逻辑删除其消息。

全局唯一字段：`users.phone`、`orders.order_no`、`gateway_api_tokens.token_hash`、`model_health.model_name`。所有业务资源 lookup 先按活动记录的 `guid` 找到内部 `id`，再进行关联处理。

## 枚举与时间

持久化实体中的受控枚举使用显式整数值和双向代码映射，不使用字符串、数据库 ENUM 或可重排的 `iota`。API 可继续根据 DTO 输出语义化文本。

所有持久化时间包括创建、更新、失效、最后使用、支付和健康检查时间均为 `BIGINT` Unix 毫秒；Go 实体使用 `int64` / `*int64`，仅在接口边界转换为 RFC 3339 文本。

## 迁移与运行方式

新增内嵌 SQL migration runner，提供：

```text
go run ./cmd/migrate up
go run ./cmd/migrate status
```

每个 migration 在事务内执行并记录到合规的 `schema_migrations` 表。应用启动只校验连接与已迁移版本，不调用 GORM `AutoMigrate`。服务配置校验 MySQL 8 `DATABASE_URL`，并校验 `SNOWFLAKE_NODE_ID`。

测试使用独立 MySQL 8 测试库；不保留 SQLite database dialect 或 SQLite schema 路径。单元测试覆盖 Snowflake、枚举、审计写入、soft-delete scope 与 DTO GUID 转换；集成测试覆盖真实 migration、事务、删除、授权和 API。

## RAG 清理

删除 `internal/rag`、Dataset/DatasetVersion 实体和表、数据集服务/种子、上传和 Chroma 配置、平台请求/响应中的 dataset 字段，以及对应的测试。

删除以下字段和行为：`dataset_enabled`、`dataset_ids`、`dataset_used`、`dataset_attribution`、用户数据集权限与调用计数、RAG prompt 拼接及 RAG SSE meta。后端严格拒绝这些已移除的请求字段。

平台 Chat、Compare 和 SSE 继续将裁剪后的会话消息直接转发到白牌上游，并保留会话、消息、用量和账务写入。

## API 与前端

- 所有业务 DTO 仅输出 `guid`，不输出内部 `id` 或 `user_id`。
- 所有资源 URL 与请求体的业务标识使用 GUID 字符串。
- JWT subject 与数据库内部外键可继续使用内部用户主键，但不得向 HTTP 客户端暴露。
- Porsche-Web 删除所有数据集/RAG state、调用、字段、路由和 mock；会话、Token、订单等资源的状态、路由与请求改为 GUID 字符串，避免 JavaScript `int64` 精度丢失。
- RAG 相关环境变量、README 与部署资料删除；历史设计资料保留并标为历史记录。

## 安全与交付门禁

- 不执行删除数据库、删除卷、无条件 `DROP TABLE` 或隐式迁移。
- schema 初始化只面向新建、明确指定的 MySQL database；生产部署前必须执行 migration status 和备份/目标确认。
- 所有写操作必须获得 actor（用户内部 ID 或系统 `NULL`），并写入审计字段。
- 已逻辑删除的用户、Token、资源不得认证、授权、参与统计或被常规关联读取。
- 交付前要求：无未解决 High/Critical 安全问题；Go 单元/集成测试、前端测试/构建、迁移 MySQL 8 冒烟、`go vet ./...` 和 diff 检查均通过。
