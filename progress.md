# Porsche 开发进度

## 当前唯一活动功能

`go-006`：用户注册管理一期（`in_progress`）。Task 1 已完成生产认证配置与追踪；尚未执行迁移、数据库写入或认证端点实现。

## 用户注册管理一期 Task 1（2026-08-28）

- 生产环境认证配置 fail-closed：缺少 Redis、固定登录或示例凭据、默认或复用的 JWT/HMAC/Admin/Metrics 密钥、非 HTTPS 或空可信 Origin、非正认证数值，以及不完整或不合法的一次性 Root 引导均会拒绝启动。
- `LoadMigrationSettings` 保持只加载迁移所需配置，不要求上游或认证配置；本 Task 未新增或执行数据库迁移，也未连接数据库。
- 已验证 `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/config -count=1`、`git diff --check` 与 `GOCACHE=/private/tmp/porsche-go-build-cache go test ./... -count=1`。受限环境首次因禁止 `httptest` 绑定 `[::1]` 失败，在允许回环监听的验证环境复跑后全量通过。

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

## 下一步（部署冒烟）

在具备部署环境的白牌配置后，完成真实上游目录、Chat 与 SSE 冒烟。
