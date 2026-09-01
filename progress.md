# Porsche 开发进度

## 当前唯一活动功能

`go-006`：用户注册管理一期（`in_progress`）。Task 1–5 已完成配置、认证 schema、可撤销会话、用户名/RBAC 与 HTTP 端点；真实隔离 MySQL/Redis HTTP 验收及前端尚未完成。

## 用户注册管理一期 Task 1（2026-08-28）

- 除 development 外的认证配置 fail-closed：缺少或无效 Redis URL、固定登录、SMS 开发模式或示例凭据、短于 32 字节/默认/重复或复用的 JWT/HMAC/Admin/Metrics 密钥、非 HTTPS 或空可信 Origin、无效开关/认证数值，以及不完整或不合法的一次性 Root 引导均会拒绝启动。`APP_ENV` 会 trim/lowercase 并限制为 `development`、`test`、`staging` 或 `production`。
- `LoadMigrationSettings` 保持只加载迁移所需配置，不要求上游或认证配置；本 Task 未新增或执行数据库迁移，也未连接数据库。
- 已验证 `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/config -count=1`、`git diff --check` 与 `GOCACHE=/private/tmp/porsche-go-build-cache go test ./... -count=1`。受限环境首次因禁止 `httptest` 绑定 `[::1]` 失败，在允许回环监听的验证环境复跑后全量通过。

## 用户注册管理一期 Task 2（2026-08-28）

- 新增嵌入式、前向 `0002_auth_core` MySQL 迁移：不修改 `0001`；将既有 `users.phone` 改为可空但保留 `uk_users_phone`，新增可空、全局唯一且软删后永久占用的 `username`，以及 `role`、`auth_version`、`last_login_at`。这使后续用户名注册可以不伪造手机号；现有 Python 数据不会被删除或重写。
- `user_sessions` 与 `auth_audit_events` 均使用有符号 `BIGINT guid`、毫秒 `BIGINT` 审计字段、`INT is_deleted` 与默认活跃查询索引；用户关联只使用 `user_id -> users.id`。会话仅存当前/前一 Refresh HMAC，认证审计不保存密码、令牌、Cookie 或原始 Authorization/Header。
- Go `User.Phone` 为不序列化的 `*string`；旧手机号认证仅作兼容写入，未生成假手机号。新增稳定 `UserRole`、`LoginMethod` 与 `AuthAuditEventType` 整数映射，以及 `Session` / `AuthAuditEvent` 持久化实体。尚未实现会话服务、用户名注册或任何 Task 3+ 业务路径。
- 已验证迁移/实体契约与全量 Go 回归：`GOCACHE=/private/tmp/porsche-go-build-cache go test -v ./internal/migration ./internal/models -run Auth -count=1`、允许回环监听环境中的 `GOCACHE=/private/tmp/porsche-go-build-cache go test ./... -count=1`、`go vet ./...` 与 `git diff --check`。
- 环境阻塞：未设置 `TEST_DATABASE_URL`。受控 MySQL 测试只读取该变量并拒绝非 `*_test` 库，当前按设计跳过；未连接 `DATABASE_URL`、`.env` 或任何生产库，因此真实 MySQL 8 的 `0002` 迁移验证仍待提供隔离测试库后执行。

## 用户注册管理一期 Task 3（2026-08-28）

- 新增 fail-closed `go-redis/v9` 认证存储：账户/IP 双维登录失败锁、24 小时会话签发上限、会话否决栅栏，以及仅以 SID-AAD 绑定、用途 KDF 隔离的 AEAD 加密保存 30 秒并发 Refresh 结果。刷新明文不写 MySQL、审计事件或日志；MySQL 只保存 HMAC-SHA256 摘要。
- `SessionService` 在 MySQL 事务内创建、轮换和撤销会话，并复用共享雪花 `guid` 与毫秒审计 helper；常规读取固定 `is_deleted = 0`，第 51 个活跃会话会逻辑吊销最旧会话，窗口外旧 Refresh 重放会先写 Redis 否决栅栏再吊销 MySQL 会话并写审计事件。
- 已按 RED→GREEN 验证：新增 API 不存在时 `go test ./internal/service -run 'Test(AuthSession|RefreshRotation|LoginRateLimit)' -count=1` 编译失败；实现后定向测试通过但在未设置 `TEST_REDIS_URL` 时四项真实 Redis/MySQL 用例显式跳过。允许回环监听的环境中 `GOCACHE=/private/tmp/porsche-go-build-cache go test ./... -count=1`、`go vet ./...` 和 `git diff --check` 通过。
- 环境阻塞：`TEST_REDIS_URL` 与 `TEST_DATABASE_URL` 均未提供。测试从不读取 `.env`、`REDIS_URL` 或 `DATABASE_URL`，因此真实 Redis/MySQL 的 5 次限流、51 会话淘汰、30 秒并发 Refresh 与窗口外重放吊销仍待隔离环境执行。
- 安全返工：Refresh 改为 SID 行锁内先写 Redis 加密 pending 结果、MySQL 提交后可恢复发布；后提交发布失败时，持有旧 Cookie 的并发请求可在验证前一 HMAC 后恢复同一结果，不会误吊销会话。`RevokeOthers` 与创建会话共享 `users` 行锁，避免并发下漏吊销目标会话。
- 状态机返工：Redis public/pending rotation 记录均携带目标 Refresh HMAC 指纹；Lua 脚本只返回匹配当前 MySQL HMAC 的代次，并原子替换 stale public 结果。连续 A→B→C 且 B TTL 尚存时，B Cookie 的并发请求只能恢复 C，不能返回 B。
- 新增真实受控 MySQL/Redis 集成用例覆盖 A→B→C 后 8 个并发旧 B Refresh 全部返回 C，并断言数据库仅保存 C 当前 HMAC 与 B 前一 HMAC。无 `TEST_DATABASE_URL` 或 `TEST_REDIS_URL` 时显式跳过；本地 `go test -race ./internal/service -run 'Test(RefreshRotationConcurrentOldBReturnsC|AuthSession|RefreshRotation|LoginRateLimit|AuthRedis)' -count=1`、全量测试、vet 与 diff 检查通过。

## 用户注册管理一期 Task 4（2026-08-28）

- 新增用户名认证领域收口：用户名 trim 后限制为 3–20 个 ASCII 字母/数字/`_`/`-`；注册在事务中跨墓碑检查永久唯一性，并由既有 MySQL `uk_users_username` 强制兜底。用户名注册不创建或伪造手机号，密码以带参数的 Argon2id 编码存储；弱密码和非 8–20 字符密码会被拒绝。
- Root 仅由部署配置引导：启动时仅在不存在任何 Root（包括软删墓碑）时创建，使用同一 MySQL 连接的命名锁串行多副本引导，成功后清空进程内 bootstrap 值；Root 创建与认证审计在同一事务中写入。常规用户读取仍以 `is_deleted = 0` 限定，后续管理端点必须禁止 Root 软删。
- 新 Access JWT 仅包含 `sub=<用户guid>`、`sid`、`sv`、`av`、`role`；不含内部用户 `id`、密码哈希或 Refresh。`RequireUser`/`RequireUserID` 均严格校验签名、完整 claims、用户 `guid + is_deleted=0`、状态、`auth_version`、持久化角色与 `SessionService.Validate` 的会话版本/否决状态。
- `RequireAdmin` / `RequireRoot` 改为认证会话上的最低持久化角色检查，Analytics 管理权限不再以手机号判断；`ADMIN_TOKEN` 不能绕过管理员门禁，也不再回退为 Metrics 凭据。
- P1 管理权限竞态修复：`mutateManagedUser` 在事务中锁定操作者后通过 `CanManageUser` 重新要求其为 active；即使请求先前已通过 `RequireAdmin`，随后被禁用的管理员也会收到 403，目标用户状态与 `auth_version` 保持不变。真实 MySQL/Redis 回归仅使用显式 `TEST_DATABASE_URL` 与 `TEST_REDIS_URL`；本地未设置时按安全规则跳过。
- 验证：RED 阶段因缺少用户名函数与会话 claims parser 发生预期编译失败；GREEN 后 `GOCACHE=/private/tmp/porsche-go-build-cache go test -v ./internal/service ./internal/middleware -run 'Test(Username|RootBootstrap|LoginUsername|PasswordUsesArgon2id|AccessTokenSubjectUsesUserGUID|SessionClaims|MinimumRole)' -count=1`、管理员旧 `ADMIN_TOKEN` 拒绝测试、`go vet ./...` 与 `git diff --check` 通过。`go test ./... -count=1` 的剩余失败均为既有测试在受限沙箱无法监听 `[::1]`，未出现 Task 4 业务断言失败。
- 环境阻塞：`TEST_DATABASE_URL` 与 `TEST_REDIS_URL` 未提供，永久用户名、Root 首次引导、禁用/软删登录拒绝与实际 `SessionService.Validate` 的集成用例按安全规则显式跳过；未读取 `.env`、`DATABASE_URL` 或生产凭据。

## 用户注册管理一期 Task 5（2026-08-31）

- 新增 `/api/v1/auth` 的用户名注册、登录、刷新、登出、本人资料、会话列表/本人撤销/撤销其他设备以及密码和实名入口；旧短信与固定账号端点继续明确返回 410。Access JWT 与 Refresh 均不在 DTO 或日志中回显，Refresh 仅通过 `porsche_refresh` 的 `HttpOnly; Secure; SameSite=Lax` Cookie 传输。
- Refresh 与 logout 强制可信 HTTPS Origin，且请求携带 `X-Auth-Session` 时必须与 Cookie 的 SID 一致；会话 SID、内部 `id`/`user_id`、密码哈希和 Refresh HMAC 不会序列化。管理员列表/详情限定严格低角色，删除复用现有事务化软删除服务，Root/同级无法管理。
- RED：新增认证路由/DTO 测试后，因 `dto.AuthUser` 和 `dto.AuthSession` 尚不存在而发生预期编译失败。GREEN：定向 handler/dto/middleware 测试、`go vet ./...` 和 `git diff --check` 通过。
- 测试验收补充：`internal/handler/auth_sessions_integration_test.go` 的真实 HTTP 流仅使用显式 `TEST_DATABASE_URL` 与 `TEST_REDIS_URL`，并在迁移前强制数据库名为 `*_test` 或 `porsche_test`。它覆盖注册、登录、`/self`、会话列表、撤销其他设备、`X-Auth-Session` 与 Cookie SID 不一致拒绝、刷新后的 Access/Cookie SID 一致、登出清 Cookie 及旧 Access 拒绝；另覆盖管理员列表/详情的同级与 Root 拒绝、Root 对低角色放行和响应脱敏。
- 环境阻塞：当前未设置显式 `TEST_DATABASE_URL` 与 `TEST_REDIS_URL`，所以新增真实 HTTP 用例显示 `SKIP`，尚未验收；未读取 `.env`、`DATABASE_URL` 或生产凭据。`GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/handler ./internal/dto ./internal/middleware -run 'Test(AuthSessionHTTPFlow|AdminUsersHTTPHierarchy|AuthSession|AdminUser|LegacyPhone|UserDTO|SessionClaims|MinimumRole)' -count=1`、相同包的 `go vet` 与 `git diff --check` 通过。未加筛选的 handler 包测试仍因既有 `httptest` 无法监听 `[::1]` 而中止，不是 Task 5 断言失败。

`go-004`：JieKou AI 白牌上游接入（`blocked`）。真实部署环境 JieKou 冒烟待办；该外部验证完成前不得标记为通过。

## 已验证基线（2026-08-21）

- 后端：在 `/Users/xuzhihao/code/Porsche/.worktrees/white-label-upstream` 运行 `GOCACHE=/private/tmp/porsche-go-build-cache ./init.sh` 成功。环境为 Go 1.22.12，脚本中的 `go test ./...` 全部通过；未设置 `RUN_START_COMMAND=1`，因此未启动服务。
- 前端隔离：`Porsche-Web/.worktrees` 已由 Git 忽略，已创建 `feature/white-label-upstream` 工作树，主工作区未改动。
- 前端恢复：在 `llm-platform` 工作树中，`npm ping` 成功；`npm install --package-lock=false` 完成且未产生 `package-lock.json` 变更；`npm test` 8/8 通过；`npm run build` 通过，仅输出既有警告。

## Task 2 配置、错误与请求校验（2026-08-22，完成）

- RED：在 `0fe9b36` 上加入回归测试后，`GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/whitelabel -run 'Test(PublicInvalidRequestErrorMatchesContract|ValidateRequestEnforcesChatContract|ValidateMediaURLRejectsLocalAndMappedAddresses|ValidateRequestAcceptsSafeVideoURLAndRejectsUnsafeSources)' -count=1` 失败：大于 16384 的正 `max_tokens` 被拒绝、单标签十六进制 IPv4-like host 被接受，且合法 `video_url` content part 被拒绝。
- GREEN：`GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/whitelabel -count=1` 与 `GOCACHE=/private/tmp/porsche-go-build-cache go test ./...` 均通过。
- 校验范围：未知顶层参数返回 `unsupported_parameter`；`max_tokens` 仅要求正整数并交由上游实施上下文限制；图片与视频 HTTPS URL 均拒绝 userinfo 和非公网字面地址，且验证不进行 DNS 解析；视频不接受 data URI。
- P2 收尾：数据图片解码上限为 8 MiB；回归测试确认 8 MiB 图片 data URI 的完整请求体小于 12 MiB 并可接受，而 8 MiB + 1 字节被拒绝。测试先在旧实现上失败（两/三标签十六进制 IPv4-like host 与超限图片均被接受），随后 `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/whitelabel -count=1`、`GOCACHE=/private/tmp/porsche-go-build-cache go test ./...` 与 `git diff --check` 全部通过。

## 阻塞与未验证项

- 未进行任何真实 JieKou AI 上游目录、Chat 或 SSE 冒烟；该验证仍需要部署环境的白牌配置。

## 全局模型 allowlist 正则（2026-08-27）

- `JIEKOU_ALLOWED_MODELS` 支持逗号分隔的精确模型 ID，以及仅限全局配置的显式
  `re:` RE2 模式；用户与 Gateway Token 的 `allowed_models` 仍只按精确 ID 匹配。
- 已验证 `go test ./internal/config -count=1`、`go test ./internal/whitelabel -count=1`、
  `go test ./... -count=1` 与 `go vet ./...`；无效或空的正则会在启动配置解析时失败。
  未执行真实 JieKou 上游目录、Chat 或 SSE 冒烟，`go-004` 保持 `blocked`。

## Task 6 旧上游清理（2026-08-24）

- 删除静态 `config/models.yaml`、`config/clients.yaml`、旧厂商密钥加载、旧 Gateway/Registry 运行时代码与静态客户端回退。
- `.env.example`、README 与领域文档仅保留 `UPSTREAM_REGION`、`JIEKOU_API_KEY`、`JIEKOU_ALLOWED_MODELS` 白牌配置说明。
- 不修改数据库连接、GORM 模型或迁移，因此不会删除或改写 Python 服务共享的 MySQL 数据。

## 预发布 Nginx 代理安全修复（2026-08-24）

- `deploy/nginx/aiportcloud.conf` 不再转发客户端可控的 XFF 链；它以
  `$remote_addr` 覆盖 `X-Forwarded-For`，避免在应用信任 Nginx 时绕过
  Gateway Token IP allowlist。
- 为 OpenAI-compatible SSE 配置 HTTP/1.1、`proxy_buffering off` 和 300 秒
  read/send timeout。
- `deploy/nginx/test-aiportcloud-conf.sh` 静态校验配置与部署说明；本机未安装
  Nginx 二进制，因此未执行 `nginx -t`。Go 全量测试与 `go vet ./...` 已通过。
- 部署时必须将 `TRUSTED_PROXY_CIDRS` 配置为 Nginx 实际连接容器时的源 IP/CIDR，
  不可从其他主机照抄 Docker gateway；需要使用 XFF 时还必须设置
  `TRUST_PROXY_HEADERS=true`。

## Task 3 生产部署文档与静态验证（2026-08-24）

- README 记录默认部署命令 `sudo bash deploy/production-deploy.sh`，以及当 `.env`
  使用 Docker host 时的 `APP_DOCKER_NETWORK=... sudo -E bash
  deploy/production-deploy.sh`。部署会短暂中断应用；候选容器启动或健康检查失败时
  脚本自动恢复旧应用容器。
- README 明确脚本会 fetch、switch、hard-reset `main` 到 `origin/main`，只替换应用
  容器、不管理 MySQL，拒绝生产 `ENV_FILE` 覆盖；部署前必须执行 `sudo nginx -t`。
- 部署拓扑仍须正确设置 `TRUST_PROXY_HEADERS=true` 和 Nginx 实际源 IP/CIDR 的
  `TRUSTED_PROXY_CIDRS`。真实 JieKou 目录、Chat 与 SSE 冒烟必须在部署环境另行执行，
  未标记为已通过。

## 生产部署预发布加固（2026-08-24）

- 部署脚本只读取所在 checkout 的 `.env`；mock 回归测试在临时 fixture repository 中
  创建该文件并 symlink 真实脚本，因此不会触碰真实 checkout 的 `.env`。
- 每次健康探测使用 2 秒连接和 3 秒总超时，至多 30 次；超时会删除候选并恢复旧容器。
  `/var/lock/${APP_NAME}.deploy.lock` 的非阻塞 `flock` 会拒绝并发部署，且竞争者没有
  Docker 写操作。
- 新增严格 `.dockerignore`，排除 `.env`、Git/worktree/agent 本地目录、data 与测试/IDE
  输出，同时保留 Go 源码、Dockerfile 和 `.env.example`。成功部署输出容器 ID 与 Git revision。

## 前后端一键更新重启（2026-08-26）

- 新增生产入口 `sudo /opt/Porsche/deploy/restart-all.sh`，固定使用后端
  `/opt/Porsche`、前端 `/opt/Porsche-Web`、静态目录 `/var/www/porsche-web` 与
  Docker 网络 `porsche-app`。脚本先拉取并构建前端，再复用后端可回滚发布脚本；
  后端成功后才同步静态资源，并在 Nginx 配置校验通过后重载服务。
- 前端依赖明确使用 `npm install --package-lock=false`，因为仓库未提交 lockfile。
  静态资源以 `rsync --archive --delete --delay-updates` 发布；该方式清理过期文件，
  但不是跨文件原子切换，短时间内可能出现新旧资源混合响应。
- 脚本不创建、迁移、停止或删除 MySQL、数据库卷、Docker 网络或无关容器。Nginx
  从 `/var/www/porsche-web` 提供 SPA，并将 `/api/`、`/v1/`、`/admin/` 和
  `/health` 反代至 loopback 应用；不带尾斜杠的 `/api`、`/v1` 与 `/admin`
  也保留为后端路由，避免被 SPA fallback 吞掉。
- 验证证据：`bash -n deploy/production-deploy.sh deploy/test-production-deploy.sh
  deploy/restart-all.sh deploy/test-restart-all.sh`、`bash deploy/test-production-deploy.sh`、
  `bash deploy/test-restart-all.sh`、`bash deploy/nginx/test-aiportcloud-conf.sh`、
  `GOCACHE=/private/tmp/porsche-go-build-cache go test ./... -count=1`、
  `GOCACHE=/private/tmp/porsche-go-build-cache go vet ./...` 与 `git diff --check`
  于 2026-08-26 通过。Go 全量测试在受限沙箱中因禁止绑定 `[::1]` 临时端口失败，
  在允许 loopback listener 的执行环境中复跑后全部包通过。

`go-004` 的真实 JieKou 目录、Chat 与 SSE 冒烟仍需部署环境的白牌配置；上述
部署编排验证不替代该上游验收，故其状态保持 `blocked`。

## 认证生产域验收部署工具（2026-09-01）

- 新增内部 Redis bootstrap、显式确认的认证 schema 迁移、候选部署和 manifest
  回滚入口。部署只接受两个指定 feature 分支的干净远端一致 checkout，不切换或
  reset Git；失败会恢复旧应用容器和已变更的静态文件，数据库迁移不会自动回滚。
- Shell 行为测试改为在无网络、无 Docker socket、只挂载临时 fixture 的
  `bash:5.2` 容器内运行目标脚本；结构化 argv 日志仅用于行为断言，不再依赖手写
  Shell 词法扫描器作为安全边界。
- 已验证新旧 Shell 回归、Nginx 静态检查、`go test ./... -count=1`、
  `go vet ./...` 与 `git diff --check`；空/短 Redis 密码、错误确认、错误分支、脏
  checkout、远端 SHA 不一致、候选健康失败、rsync/reload 失败均被隔离夹具拒绝或回滚。
- 浏览器生产域验收尚未执行，`go-006` 继续保持唯一 `in_progress`。

## 认证 Root 引导安全返工（2026-09-01）

- GORM SQL logger 已加泄露防护，Root bootstrap 凭据、环境值和其派生的敏感参数不应写入 SQL 日志。一次性 Root wrapper 只从已验证、远端一致的 feature SHA 创建 Git archive，并以构建返回的不可变 Docker image ID 执行，不依赖可变标签或工作树中的未跟踪输入。
- Root runtime environment startup path 已退役：生产服务拒绝任何 `ROOT_BOOTSTRAP_` 声明并且绝不自动引导；唯一流程是 root-controlled one-shot wrapper。Root credential 与 `/opt/Porsche/.env` 的 snapshot 源路径 metadata 校验只属于该 wrapper：凭据文件及其父目录、backend 目录和 `.env` 必须通过属主、权限、non-symlink 校验；复制到私有 `0700` snapshot 后才以只读 mount 传入 disposable `--rm` bootstrap 容器。
- 候选 deploy 与 manifest rollback 在任何 npm/build、Nginx、container stop/remove/rename、rsync 或 static write 前，均 fail-closed 扫描 `docker ps -a` 中 exact `ai-gateway-go` 与 `ai-gateway-go-acceptance-rollback-<digits>` 的运行/停止容器；rollback manifest target 未出现在列表时也会显式 inspect。`ROOT_BOOTSTRAP_` 命中和 inspect/list 失败均不回显值并拒绝继续；fixture contract 覆盖 current/stopped rollback、inspect/list error、unrelated helper 与 stdout/stderr/argv 脱敏。
- 已以 disposable no-network fixture containers 验证 `docs deploy rollback`：当前/停止 rollback 容器的 Root key、inspect/list failure、manifest target 的显式 inspect、helper 排除和敏感值不进入 stdout/stderr/argv 均通过；`bash -n`、`jq` 的唯一 `in_progress` 检查与 `git diff --check` 也通过。真实 test machine 的 one-shot/bootstrap、candidate deploy 及 browser acceptance 仍待受控生产域/隔离依赖环境执行，不能据此把生产验收标为 passing。`go-006` 保持唯一 `in_progress`。

## 下一步（部署冒烟）

在具备部署环境的白牌配置后，完成真实上游目录、Chat 与 SSE 冒烟。
