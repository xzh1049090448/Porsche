# Agent Instructions

Porsche 是一个位于仓库根目录的 Go 模型聚合网关。入口为 `cmd/server/main.go`，业务代码位于 `internal/`，运行配置位于 `config/`。

## 开工流程

开始任何改动前：

1. 用 `pwd` 确认位于仓库根目录。
2. 阅读 `progress.md`、`feature_list.json` 与最近 5 条提交。
3. 运行 `./init.sh`；若基础验证失败，先记录并修复基础状态。
4. 一次只将一个功能设为 `in_progress`。

## 项目命令

- 安装/同步依赖：`go mod tidy`
- 验证：`go test ./...`
- 启动：`go run ./cmd/server`
- 构建镜像：`docker build -t ai-gateway-go .`

## 数据库安全

- 生产环境仅支持 MySQL 8；通过 `.env` 的 `DATABASE_URL` 连接新环境业务数据库，不兼容历史 Python 共享表或 SQLite。
- 可直接使用 `mysql+aiomysql://...` 或 `mysql://...` URL。
- 服务启动前必须执行 `go run ./cmd/migrate up`；服务不会自动建表或执行隐式迁移。
- 不得执行 `docker compose down -v` 或 `docker volume rm`；它们可能删除 MySQL 命名卷中的数据。
- **任何数据库模型、迁移、持久化写路径或用户关联改动前，必须完整阅读并严格遵守 [数据库建模与持久化约束](docs/conventions/database-standards.md)。** 该文档规定 `id`/雪花 `guid`、审计字段、非 `TIMESTAMP` 时间类型、整数枚举及用户关联规则；冲突必须先取得明确批准。

## 工作规则

- 不要因代码已存在就标记功能完成；必须运行并记录验证证据。
- 不要在未验证或失败的基础状态上叠加无关改动。
- 保持 API 路径和 JSON 契约向后兼容，除非用户明确要求变更。
- 不提交 `.env`、`data/` 或 Harness 日志文件。

## 完成与收尾

一个功能只有在实现、验证命令和证据均已记录后才能标为 `passing`。结束会话前：

1. 更新 `progress.md`、`feature_list.json` 与必要时的 `session-handoff.md`。
2. 记录未解决风险和下一步。
3. 仅提交本轮目标相关的文件。
