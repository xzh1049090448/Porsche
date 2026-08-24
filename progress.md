# PRD-260820 白牌上游接入进度

## 当前唯一活动功能

`go-004`：JieKou AI 白牌上游接入（`in_progress`）。本轮已完成隔离环境、前后端实现、自动化验证，以及旧上游路由/配置清理；真实上游冒烟仍待部署环境执行。

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

## 下一步（部署冒烟）

在具备部署环境的白牌配置后，完成真实上游目录、Chat 与 SSE 冒烟。
