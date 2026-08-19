# Go Gateway Agent Roles Design

## Goal

为 Porsche 单一 Go 模型聚合网关提供五个项目级 Codex Agent 配置，使架构探索、实现、安全审查、测试和任务编排拥有明确且可执行的职责边界。

## Project Context

- 服务入口：`cmd/server/main.go`
- HTTP：Gin，路由位于 `internal/router/` 和 `internal/handler/`
- 业务：`internal/service/`
- 持久化：GORM，实体位于 `internal/models/`，数据库连接位于 `internal/db/`
- 上游模型与配置：`internal/gateway/`、`internal/registry/`、`config/*.yaml`
- 测试：Go 标准库 `testing`，命令为 `go test ./...`
- 兼容性：必须保留既有 MySQL 数据、API JSON 契约和 SSE 行为。

## Agent Configurations

所有配置位于 `.codex/agents/`，由 Git 跟踪。

### architect_explorer

- 使用 `gpt-5.6-luna` / `high` / `read-only`。
- 扫描路由、Gin 中间件、Handler → Service → Models/DB 链路、模型网关、配置和 RAG。
- 因为只读，不写 `CONTEXT.md`；在最终回复交付结构化上下文包，包含依赖图、API、实体、数据流和风险。

### backend_worker

- 使用 `gpt-5.6-terra` / `medium` / `workspace-write`。
- 实现 Go API 与业务逻辑，遵守现有目录边界和公开 API 契约。
- 先检索并复用现有结构体、DTO、Service、工具函数和错误类型；不重复造轮子。
- 只在无法复用时新增抽象，并说明原因。
- 对关键业务规则、兼容性处理、事务边界和安全判断添加必要的 Go 注释；不为显然的局部代码添加冗余注释。
- 事务只用于必须原子完成的多步写操作；测试使用 `go test ./...` 和同包 `*_test.go` 文件。

### security_reviewer

- 使用 `gpt-5.6-terra` / `xhigh` / `read-only`。
- 审查 JWT、管理员权限、越权、GORM 查询边界、密钥/上游 URL、SSE、日志 PII、上传和 RAG 数据隔离。
- 因为只读，不写 `SECURITY_REPORT.md`；在最终回复交付按严重级别分类的 `SECURITY_REPORT`。

### test_engineer

- 使用 `gpt-5.6-terra` / `medium` / `workspace-write`。
- 使用 `testing`、`httptest`、SQLite 或测试配置覆盖正常、边界和错误路径。
- 不依赖真实 MySQL、Redis 或上游模型服务。
- 测试文件放在受测包目录，最终回复包含测试计划和执行结果；不默认创建根目录 `TEST_PLAN.md`。

### project_manager

- 使用 `gpt-5.6-luna` / `high` / `read-only`。
- 需求分解后，使用可用的子 Agent 机制依次调度 Explorer、Worker、Security Reviewer 和 Test Engineer。
- 仅在 Worker 完成后安排 Test Engineer；若运行环境不支持子 Agent，清楚报告该限制并输出可执行任务顺序。
- 门控条件：无未解决高危安全问题、`go test ./...` 通过、MySQL/API/SSE 兼容性风险已处理或明确记录。

## Non-goals

- 不新增外部依赖或第二套测试框架。
- 不改变 Go 模块名 `github.com/porsche/ai-gateway-go`。
- 不删除 MySQL 数据、Docker 命名卷或现有 Harness 文件。
