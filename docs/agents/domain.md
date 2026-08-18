# Porsche Go 网关领域文档

本仓库是单一 Go 模型聚合网关，源码直接位于仓库根目录；不再包含 Python 网关、Java 服务或多个子项目上下文。

## 探索代码前应阅读的内容

1. [`README.md`](../../README.md)：运行方式、环境变量和对外 API 范围。
2. [`AGENTS.md`](../../AGENTS.md)：开发规则、验证命令与 MySQL 数据安全要求。
3. `config/models.yaml` 与 `config/clients.yaml`：模型路由、上游密钥环境变量和下游客户端权限。
4. `internal/models/models.go`：持久化实体及其状态字段。
5. 与任务匹配的 `internal/handler/`、`internal/service/` 与 `internal/gateway/` 代码。

如果未来新增 `docs/adr/`，仅在其与当前改动有关时阅读；该目录尚不存在时直接继续，不必创建占位文件。

## 领域概念与用词

| 概念 | 含义 | 主要位置 |
| --- | --- | --- |
| 用户（User） | 通过手机号认证的账户，拥有套餐、状态、模型授权和用量。 | `internal/models/models.go`、`internal/service/auth.go` |
| 对话（Conversation） | 用户与模型的会话容器，包含标题、默认模型、数据集选择与消息历史。 | `internal/service/conversation.go` |
| 消息（Message） | 对话中的用户或助手内容；助手消息记录模型、token 与数据集归因。 | `internal/models/models.go` |
| 数据集（Dataset） | 用户拥有的知识库子集；启用后由 RAG 生成带上下文的模型请求。 | `internal/rag/`、`internal/service/platform_chat.go` |
| 模型路由（Model Route） | 逻辑模型名到供应商、上游模型名、Base URL 与 API 密钥环境变量的映射。 | `config/models.yaml`、`internal/registry/` |
| 下游客户端（Client） | 调用 OpenAI 兼容网关的客户端密钥及其模型权限、IP 白名单和限额。 | `config/clients.yaml`、`internal/registry/` |
| 平台对话 | 面向已登录用户的 `/api/v1/platform/*` 对话与多模型比较接口。 | `internal/service/platform_chat.go` |
| 计费与订单 | 套餐、调用额度、订单、支付和发票等状态流转。 | `internal/service/billing.go` |

输出接口、测试名称和提交信息时，优先使用上述术语，避免将“下游客户端”与“用户账户”、将“逻辑模型”与“上游模型”混为一谈。

## 代码边界

```text
cmd/server/       服务入口
internal/handler/ HTTP 请求绑定与响应
internal/service/ 业务规则、持久化编排和权限检查
internal/gateway/ 上游模型请求与流式转发
internal/registry/模型和客户端配置加载
internal/models/  GORM 实体与枚举
internal/db/      数据库连接与迁移
internal/rag/     数据集检索与上下文构建
```

跨层修改应保持该边界：Handler 不承载业务规则；Service 不直接依赖 HTTP 上下文；Gateway 只处理上游协议转发。

## 数据与兼容性

- 生产环境可复用既有 MySQL 的 `DATABASE_URL`，包括 `mysql+aiomysql://...` 格式。
- 数据库中可能保留 Python 服务写入的枚举名称，例如 `ACTIVE`；读取逻辑必须兼容该历史格式。
- 不得通过 `docker compose down -v` 或 `docker volume rm` 清理环境，以免删除 MySQL 命名卷。
- 调整 API 时优先保持既有路径与 JSON 契约；有意的不兼容变更应在 README 或未来 ADR 中记录。
