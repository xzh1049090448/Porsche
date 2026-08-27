# ai-gateway-go

Go 语言实现的 **国内大模型聚合平台 API**。服务路径与既有前端契约保持一致。

## 功能对齐

| 模块 | 路径前缀 | 状态 |
|------|----------|------|
| 健康检查 | `GET /health` | ✅ |
| OpenAI 兼容网关 | `GET /v1/models`、`POST /v1/chat/completions` | ✅ |
| 网关 API Token | `/api/v1/tokens` | ✅ |
| 用户认证 | `/api/v1/auth/*` | ✅ |
| 用户资料 | `/api/v1/users/*` | ✅ |
| 对话 CRUD | `/api/v1/conversations/*` | ✅ |
| 套餐/订单 | `/api/v1/billing/*` | ✅ |
| 平台对话/对比 | `/api/v1/platform/*` | ✅ |
| 模型分析 | `/api/v1/billing/analytics/*` | ✅（图表序列简化实现） |
| 管理端 | `/admin/*` | ✅ |
| Prometheus | `GET /metrics` | ✅ |

## 快速开始

### 1. 环境要求

- Go 1.22+
- Redis：生产认证的会话撤销与限流必需依赖

### 2. 配置

```bash
cp .env.example .env
# 填写 JIEKOU_API_KEY、JIEKOU_ALLOWED_MODELS、JWT_SECRET_KEY、ADMIN_TOKEN 等
```

服务只支持 JieKou AI OpenAI-compatible 白牌上游。`UPSTREAM_REGION` 仅可为
`cn` 或 `global`，并由服务选择固定上游地址；`JIEKOU_API_KEY` 与非空
`JIEKOU_ALLOWED_MODELS` 缺失或无效时服务会拒绝启动。网关调用使用用户在
`POST /api/v1/tokens` 创建的 `sk-gw-...` API Token；完整密钥仅在创建响应中
返回一次，数据库只保存 SHA-256 哈希。静态客户端和旧厂商 API Key 配置不再支持。

### 一期认证安全配置

用户名密码认证与可撤销会话使用 `REGISTER_ENABLED`、
`PASSWORD_REGISTER_ENABLED`、`PASSWORD_LOGIN_ENABLED` 和会话时长/上限配置。
除 `development` 外的环境会拒绝启动，除非同时配置有效 `redis://` 或 `rediss://`
`REDIS_URL`、非默认
`JWT_SECRET_KEY` 与 `AUTH_HMAC_KEY`，并且 `AUTH_TRUSTED_ORIGINS` 仅包含明确的
HTTPS Origin（逗号分隔）。默认 Access JWT 时长为 15 分钟、会话为 30 天、每用户
最多 50 个活跃会话、24 小时最多签发 100 次会话，Refresh 重放窗口为 30 秒；
`SESSION_ACCESS_MINUTES` 固定为 15，认证开关和数值配置不能使用无效值。
非开发环境还必须关闭 `FIXED_LOGIN_ENABLED`，并为
`JWT_SECRET_KEY`、`AUTH_HMAC_KEY`、`ADMIN_TOKEN` 和 `METRICS_TOKEN` 分别设置
至少 32 字节、非默认、非重复且互不复用的密钥。`APP_ENV` 仅允许
`development`、`test`、`staging` 或 `production`（大小写和首尾空白会规范化）。

首次部署可同时设置 `ROOT_BOOTSTRAP_USERNAME` 与
`ROOT_BOOTSTRAP_PASSWORD` 创建一次性 Root；两项必须同时配置。Root 创建成功后必须
立即从生产环境删除这两个变量并重启服务，不能把引导凭据保留为长期登录方式。

`JIEKOU_ALLOWED_MODELS` 使用逗号分隔的精确模型 ID，例如
`zai-org/glm-5.1,deepseek/deepseek-v4-pro`。全局 `.env` allowlist 也可包含显式
`re:` RE2 模式；例如下面的注释配置会允许每个通过安全模型 ID 校验的当前及未来
上游目录模型：

```dotenv
# JIEKOU_ALLOWED_MODELS=re:^.+$
```

无效或空的 `re:` 模式会导致服务在启动时失败。模式只适用于全局 `.env` allowlist；
用户和 Gateway Token 的 `allowed_models` 始终按精确 ID 匹配，不会将 `re:` 文本
解释为模式。因此，`re:^.+$` 只会向没有限制性用户或 Token ACL 的主体自动公开安全
模型，不能绕过已有 ACL。

仅支持新的 MySQL 8 schema。请在 `.env` 中设置 MySQL 连接串，例如：

```dotenv
DATABASE_URL=mysql+aiomysql://platform:platform@127.0.0.1:3306/platform
```

先对明确的新目标数据库执行迁移；服务启动不会自动创建或修改表。不要执行
`docker compose down -v` 或 `docker volume rm`，以免删除 Docker 卷中的数据库文件。

迁移是启动服务的前置步骤：

```bash
go run ./cmd/migrate up
```

### 3. 运行

```bash
go mod tidy
go run ./cmd/server
```

默认监听 `http://0.0.0.0:8000`，与 Python 版端口一致。

### 4. 前端切换

将前端 API `baseURL` 指向 Go 服务地址即可（路径不变）。开发环境默认账号：

- 手机号：`13800138000`
- 密码：`Porsche@2026`

仅支持 MySQL 8。配置 `DATABASE_URL` 和唯一的 `SNOWFLAKE_NODE_ID`（0–1023）后再启动服务。服务不会自动建表或执行隐式迁移。

## 测试

```bash
go test ./...
```

## Docker

```bash
docker build -t ai-gateway-go .
docker run --env-file .env -p 8000:8000 ai-gateway-go
```

## Production deployment

From a clean production checkout, first validate the installed Nginx
configuration, then deploy:

```bash
sudo nginx -t
sudo bash deploy/production-deploy.sh
```

The deployment can briefly interrupt the application while it swaps containers.
If the new container cannot start or its `/health` check fails, the script
automatically removes the candidate and restores the prior application
container. It fetches `origin/main`, switches to `main`, and hard-resets that
checkout to `origin/main` before building. Deployment requires no tracked
working-tree changes; an untracked `.env` is intentionally excluded from the
Docker build context.

It always uses that checkout's `.env`; it does not accept an alternate
environment-file override. A deployment lock at
`/var/lock/<APP_NAME>.deploy.lock` prevents concurrent replacements of the
same application container. The script replaces only the application container
and neither manages nor changes MySQL. On success it prints the new container
ID and deployed Git revision.

The full-stack restart command publishes frontend assets with `rsync`. It
builds and stages those assets before replacing the live site, but `rsync` is
not a cross-file atomic release mechanism; clients may briefly observe mixed
asset versions during the static-file synchronization.

### One-command frontend and backend release

On the production host, use the following command to update both repositories,
rebuild the frontend, replace the Go application container, publish frontend
assets, and reload Nginx:

```bash
sudo /opt/Porsche/deploy/restart-all.sh
```

This command has deliberately fixed production locations and does not accept
arguments:

| Purpose | Required value |
| --- | --- |
| Backend checkout | `/opt/Porsche` |
| Frontend checkout | `/opt/Porsche-Web` |
| Frontend static root | `/var/www/porsche-web` |
| Application Docker network | `porsche-app` |
| Nginx service | `nginx` |

It fetches and hard-resets both checkouts to `origin/main`; commit or stash
tracked changes before running it. The backend checkout must contain its
production `.env`, and the `porsche-app` network must already allow the
application container to reach the existing MySQL 8 service. The command does
not create, migrate, stop, remove, or otherwise manage MySQL, its Docker
container, or any database volume.

The frontend repository intentionally has no committed lockfile. The script
therefore installs build dependencies with `npm install --package-lock=false`,
then runs `npm run build` before it changes the backend or live static files.
After a successful backend deployment it stages `dist/` and synchronizes it to
`/var/www/porsche-web` with `rsync --archive --delete --delay-updates`.
`rsync` avoids stale assets but is not a cross-file atomic release mechanism:
brief mixed old/new asset responses remain possible while the copy is running.

Install `deploy/nginx/aiportcloud.conf` as the production site before using the
command. Nginx must serve the SPA from `/var/www/porsche-web` (including the
`try_files ... /index.html` fallback) and proxy `/api/`, `/v1/`, `/admin/`, and
`/health` to the loopback application. The script runs `nginx -t` before it
publishes frontend files or reloads Nginx; an invalid Nginx configuration
prevents the reload. A backend deployment failure prevents frontend publishing;
the backend deploy script retains responsibility for application-container
rollback.

If the `.env` database host is a Docker service name, attach the application to
the Docker network that resolves it (replace the example network name):

```bash
sudo APP_DOCKER_NETWORK=ai-gateway_default bash deploy/production-deploy.sh
```

Run the real JieKou upstream directory, Chat, and SSE smoke checks separately
after deployment with the production white-label configuration; a successful
local health check does not prove the real upstream path.

## Production domain access

Production traffic must enter through `https://aiportcloud.com`; direct IP
requests are rejected by the application Host allowlist and Nginx's default
HTTP server. Keep `ALLOWED_HOSTS=aiportcloud.com` in `.env`, install
`deploy/nginx/aiportcloud.conf`, and bind the application port only to the
loopback interface:

```bash
docker run --env-file .env --network ai-gateway_default \
  -p 127.0.0.1:8000:8000 ai-gateway-go
```

The Nginx TLS certificate paths in the included configuration assume
Let's Encrypt. A direct HTTPS request to an IP is rejected during TLS hostname
validation before HTTP can return a 403; direct HTTP requests return 403.

### Reverse-proxy source IP and SSE

To use Nginx's client address for IP allowlists, set `TRUST_PROXY_HEADERS=true`
and set `TRUSTED_PROXY_CIDRS` to the actual source IP or CIDR Nginx uses when
it connects to the application container. Do not copy the Docker gateway from
another host: inspect the deployed container network and use the source address
Nginx actually presents to the application.
The included Nginx configuration replaces any client-supplied
`X-Forwarded-For` chain with `$remote_addr`, and disables proxy buffering with
300-second read/send timeouts so OpenAI-compatible SSE streams are forwarded
without delay.

## 目录结构

```
Porsche/
├── cmd/server/main.go      # 入口
├── internal/
│   ├── handler/            # HTTP 路由（与 Python routes 一一对应）
│   ├── service/            # 业务逻辑
│   ├── whitelabel/         # 固定 JieKou AI 上游、目录、投影与 SSE
│   ├── models/             # GORM 实体
│   └── router/             # 路由注册
```
