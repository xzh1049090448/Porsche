# Porsche Go 网关领域文档

本说明对齐 2026-09-02 已发布的用户名认证与可撤销会话架构（后端 `90abbdc49513039fa9218a6ce72d3147fbf1721e`），不是服务器实时状态证明。Porsche 是单一 Go 1.22+ 模型聚合网关；Vue 前端位于独立的 Porsche-Web 仓库。历史 Python、静态厂商配置及 RAG/dataset 路径不是当前服务的扩展入口。

## 开工与证据

1. 确认仓库根目录、工作树、分支和提交，查看已有修改；不要把旧工作树当成已发布代码，也不要擅自切换、reset 或覆盖用户内容。
2. 完整阅读 [AGENTS.md](../../AGENTS.md)、[README.md](../../README.md)、[progress.md](../../progress.md)、[feature_list.json](../../feature_list.json) 和最近提交。按任务查看 `.env.example`，不要为了解配置而读取生产 `.env`。
3. 数据库、持久化、迁移、实体、枚举、时间或用户关联工作必须完整阅读[数据库建模与持久化约束](../conventions/database-standards.md)，在实现、审查和验收中使用其清单。
4. 有写权限的实现/测试角色按仓库要求运行 `./init.sh`，不要设置启动服务的开关。只读角色阅读脚本并请求有权限的角色提供验证证据，不能为运行 `go mod tidy` 或更新跟踪文件突破权限。
5. 代码、旧说明与约束冲突时给出具体文件和差异；不得默默恢复已退役接口或改写既定规则。历史通过记录不等于当前验证，`SKIP` 不等于集成验收通过。

## 当前代码边界

| 位置 | 职责 |
| --- | --- |
| `cmd/server/main.go`、`internal/app/` | 服务入口、依赖装配及应用状态 |
| `internal/router/`、`internal/middleware/` | 路由注册、认证、角色门禁等请求级约束 |
| `internal/handler/`、`internal/dto/` | HTTP 参数绑定、公开响应投影与错误契约 |
| `internal/service/` | 业务规则、用户归属、持久化编排、事务和并发控制 |
| `internal/models/`、`internal/db/` | GORM 实体/枚举与数据库连接；连接不隐式迁移 |
| `internal/persistence/` | 共享雪花 GUID、时钟及审计字段相关基础能力 |
| `internal/config/` | 环境配置加载与校验，不使用旧静态厂商注册表 |
| `internal/whitelabel/` | 固定 JieKou AI 上游、目录缓存、模型 ACL、安全投影及 SSE |
| `internal/httpx/`、`internal/security/` | HTTP/IP 工具和认证相关安全辅助函数 |
| `cmd/migrate/`、`internal/migration/` | 显式迁移、SQL 文件和版本记录 |
| `cmd/bootstrap-root/` | 与长期服务分离的一次性 Root 初始化入口 |
| `deploy/` | 生产/验收部署、回滚及隔离回归脚本 |

Handler 不承载业务规则；Service 不直接依赖 HTTP 上下文；WhiteLabel 处理上游协议和安全投影。优先复用现有 DTO、服务、错误和持久化工具，不为历史目录重新造实现。

## 领域概念

| 概念 | 含义 | 主要位置 |
| --- | --- | --- |
| 用户（User） | 用户名密码认证的账户，拥有持久化角色、状态、套餐、模型授权与用量；手机号不是当前登录入口。 | `internal/models/models.go`、`internal/service/auth_account.go` |
| 登录会话（Session） | 可过期、轮换、撤销的设备状态；MySQL 保存设备/IP、版本和 Refresh HMAC，Redis 提供限流、撤销栅栏与短期刷新协调。 | `internal/service/auth_session.go`、`internal/service/auth_redis.go` |
| 认证审计 | 登录与会话等认证事件，区别于一般业务审计，不保存原始密码、Token 或 Cookie。 | `internal/models/models.go`、`internal/service/auth_account.go` |
| Root / 管理员 / 用户 | 数据库持久化角色层级；管理操作遵守严格低角色边界及当前用户状态。 | `internal/middleware/`、`internal/service/auth_account.go` |
| 对话 / 消息 | 对话保存归属、标题、模型，消息保存角色、正文与 Token 等；业务对话不是登录会话。 | `internal/service/conversation.go`、`internal/models/models.go` |
| 白牌模型目录 | 固定 JieKou 上游目录与全局 allowlist、用户及 Gateway Token ACL 的交集。 | `internal/whitelabel/` |
| Gateway API Token | 网关调用凭据，带模型/IP ACL 和有效期；完整密钥仅创建时返回，数据库保存哈希及前缀。 | `internal/service/gateway_token.go` |
| 平台对话 | 已登录用户的 `/api/v1/platform/*` 对话与多模型比较接口。 | `internal/service/platform_chat.go` |
| 用量、订单与计费 | 调用记录、套餐额度、订单与支付状态；登录验收不能替代真实上游或支付验收。 | `internal/service/billing.go`、`internal/models/models.go` |

## 认证与权限

- 用户名注册不自动创建登录会话。登录签发短期 Access JWT，前端仅保存在内存中；Refresh 由 `HttpOnly; Secure; SameSite=Lax` Cookie 携带。
- JWT 携带用户 GUID、会话 SID、会话版本、用户认证版本及角色信息；鉴权仍检查持久化用户/角色、状态和可撤销会话，不以签名有效代替服务端授权。
- Refresh 和 logout 要求可信 HTTPS Origin；不得加入不安全 Origin 或放松 Cookie 属性来规避失败。携带 `X-Auth-Session` 时按当前接口检查 SID 一致性。
- MySQL `user_sessions` 保存 Refresh HMAC，不保存原始 Refresh Token。Redis 的短期刷新恢复结果经过加密，同时承载限流、签发限额和撤销栅栏；不是可任意清空的无关缓存。
- 旧 `/api/v1/auth/send-code`、`/api/v1/auth/login/password`、`/api/v1/auth/login/code` 明确退役并返回 410，不得以“向后兼容”为由恢复。
- `ADMIN_TOKEN` 不能绕过会话 RBAC，Metrics 凭据不能与管理/会话凭据混用。区分网站 Access JWT、Refresh Cookie、Gateway API Token 和运维凭据。
- 长期服务不自动创建 Root，并拒绝任何 `ROOT_BOOTSTRAP_` 环境声明，包括空值。Root 只能经明确授权的隔离 one-shot 流程初始化；不得为修复登录失败重复引导或重置 Root。
- Root credential 文件及源路径必须通过属主、权限和非符号链接校验，再以私有快照只读挂载给一次性容器；以实际 wrapper 的分支/提交前置条件为准，不得绕过其 feature 分支限制在 `main` 上运行。

## 数据与兼容性

- 只支持 MySQL 8 的显式 schema；SQLite、GORM `AutoMigrate` 或启动建表不能替代真实迁移。数据库连接与 schema 迁移是不同职责。
- 业务对外标识是字符串雪花 `guid`；内部 `id` 和 `user_id -> users.id` 仅用于关联。登录协议内部 SID 不是业务 GUID，也不应暴露到会话列表 DTO。
- 遵守共享 GUID/审计工具、Unix 毫秒 `BIGINT` 时间、稳定 `INT` 枚举和 `is_deleted` 过滤；创建、更新、事务和锁满足归属与并发安全。
- 保留已确立的用户名永久唯一规则：软删后仍占用用户名，不得套用一般“软删后可复用”的唯一索引策略。规范与业务契约冲突时先报告，不得自行改迁移。
- GORM 映射与 SQL 对齐，例如 `Session.SID` 显式映射到 `user_sessions.sid`，不能依赖默认命名生成另一列。
- 已执行迁移不得原地改写；变更采用经审核的后续迁移。应用/前端回滚不自动撤销迁移，也不授权删除迁移记录、会话或用户数据。
- API、JSON 和 SSE 兼容性以当前实现及显式退役契约为准，未经授权不改变公共边界。

## 测试与验收

- 单元/HTTP/协议测试使用 Go `testing`、`httptest` 和隔离 mock。常规命令为 `go test ./... -count=1`、`go vet ./...` 和 `git diff --check`。
- 集成测试只使用显式指定、确认可处置且与生产隔离的 MySQL 8 / Redis。入口为 `TEST_DATABASE_URL`、`TEST_REDIS_URL`；不得读取生产 `.env` 或回退到 `DATABASE_URL`、`REDIS_URL`。
- MySQL 使用 `*_test` 或 `porsche_test` 库并通过现有安全检查，确认实际连接目标；仅有名称后缀不能证明隔离。Redis 也必须独立可处置，不能只换逻辑库号就视为与生产隔离。
- 缺少 fixture 时列出跳过的用例和剩余验收项。测试进程退出 0、mock 成功或 `/health` 正常都不能证明数据库、浏览器和真实上游验收通过。
- `deploy/test-auth-acceptance-deploy.sh` 在 `bash:5.2` 临时容器内执行目标脚本：rootfs 只读、无网络、无 Docker socket，仅挂载临时 fixture；不得用手写 Shell 词法扫描器或黑白名单替代隔离。
- 按改动范围运行 `deploy/test-production-deploy.sh`、`deploy/test-restart-all.sh`、`deploy/test-dockerfile.sh` 和 `deploy/nginx/test-aiportcloud-conf.sh`，先阅读 fixture 依赖和限制；不得运行真实部署入口来“测试”。
- 认证/持久化测试覆盖相关的失败、权限、边界和并发场景；发布测试覆盖回滚、脱敏、严格 umask 下静态目录/文件权限。安全审查须覆盖最终 diff，提前做过审查不代表实现后已获批准。

## 生产与凭据保护

- 实现、测试或审查任务不自动授权生产部署、迁移、Root 初始化、回滚、密钥轮换、push 或破坏性清理；执行前核实授权、目标主机/版本、当前状态和可用备份。
- 不在输出、Git、普通日志、命令参数或报告中暴露密码、完整 API Key、Access/Refresh Token、Cookie、连接串、Root credential 或原始生产 `.env` / Docker inspect 内容。诊断优先输出状态、数量和安全摘要。
- `deploy/production-deploy.sh` 和 `deploy/restart-all.sh` 面向 `main`，会获取远端并重置 checkout；先保护未提交内容与本地提交。后端失败可尝试恢复旧容器，但成功后会删除旧容器；全栈静态同步不是跨文件原子发布，也没有覆盖所有阶段的统一自动回滚。发布前另存镜像身份、运行配置和前端备份，核验恢复路径。
- `deploy/auth-acceptance-deploy.sh` / `deploy/auth-acceptance-rollback.sh` 是有特定 feature 分支或 manifest 前置条件的验收工具，不能假定与生产脚本互换。保留仍需使用的回滚材料，不自动清理镜像或卷。
- Nginx 模板不等于实际服务器配置。先确认 Cloudflare/TLS 终止方式、已安装站点、证书和代理来源，再修改或重载；不得盲目覆盖配置。
- 真实 IP 只信任已核实的代理链：Cloudflare 场景核对官方网段及实际配置，Nginx 覆盖 XFF，应用 `TRUST_PROXY_HEADERS` / `TRUSTED_PROXY_CIDRS` 限定到已核实的代理来源。不得信任客户端任意转发头或照抄其他主机的 Docker gateway。
- 前端目录须可遍历、文件须对 Nginx 可读（通常目录 0755、文件 0644）；这不适用于凭据，严禁对整个仓库或 secret 目录宽泛放开权限。
- 不执行 `docker compose down -v`、`docker volume rm` 或宽泛递归清理。供用户在 SSH 中运行的命令将严格模式和失败退出放入子 Shell，避免普通检查失败直接关闭交互会话。
