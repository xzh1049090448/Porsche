# 前后端一键更新重启设计

## 目标

在生产主机以一条命令更新并发布 Porsche 后端应用与 Porsche-Web 前端静态站点。脚本只操作后端应用容器、前端构建产物和 Nginx；绝不创建、停止、删除 MySQL 容器、数据库或 Docker 卷，也不触碰无关的 `new-api` 容器。

## 固定部署约定

- 后端仓库：`/opt/Porsche`
- 前端仓库：`/opt/Porsche-Web`
- 前端发布目录：`/var/www/porsche-web`
- 应用 Docker 网络：`porsche-app`
- 后端应用：复用 `deploy/production-deploy.sh` 管理的 `ai-gateway-go`
- Nginx：系统服务 `nginx`

所有仓库更新均明确拉取并硬重置到远端 `main`。后端 `.env` 保留在工作树中，不被 Git 或脚本覆盖。

## 脚本与执行流程

新增 `deploy/restart-all.sh`，不接受位置参数。它必须以 root 身份运行，使用独占 `flock` 防止与另一次全栈发布并发。

1. **前置检查**：确认两个目录分别是 Git 工作树；确认 `git`、`docker`、`npm`、`rsync`、`nginx`、`systemctl`、`flock` 可用；确认后端 `.env`、前端 `package.json`、后端部署脚本存在；确认 `porsche-app` Docker 网络存在。
2. **预构建前端**：前端 fetch、切换并硬重置 `origin/main`；使用 `npm install --package-lock=false` 同步依赖，再运行 `npm run build`。构建失败即退出，此时后端与线上静态资源均不改变。
3. **发布后端**：调用现有后端部署脚本，并显式传入 `APP_DOCKER_NETWORK=porsche-app`。该脚本继续负责候选容器健康检查、失败回滚、只重建应用容器。
4. **发布前端**：仅在后端发布成功后，使用临时目录接收 `dist/`，再以 `rsync --delete --delay-updates` 同步到 `/var/www/porsche-web`。构建或暂存失败不会覆盖现有前端；同步失败返回非零并保留后端已发布状态，要求人工处理，不伪造成功。
5. **Nginx 校验与重载**：先运行 `nginx -t`，通过后 `systemctl reload nginx`。校验失败时不重载；已经替换的前端资源和成功部署的后端保持现状，并报告需修复 Nginx 配置。
6. **结束验证**：向 `http://127.0.0.1:8000/health` 发起带受允许 `Host` 的请求，并输出后端、前端两个 Git revision。

## Host 白名单与健康检查

现有后端启用 `ALLOWED_HOSTS` 时，裸 loopback 健康检查会返回 403。后端部署脚本应从 `.env` 的 `ALLOWED_HOSTS` 读取首个逗号分隔域名，并将它作为 `curl -H 'Host: ...'` 的健康检查 Host；不要求将 `127.0.0.1` 加入公网主机白名单。若该配置不存在或值为空，部署脚本失败并提示修复 `.env`。

## 失败与安全边界

- 不执行 `docker compose down`、任何 `prune`、`docker volume rm`、`docker network rm`、MySQL DDL 或数据库迁移。
- 后端失败：由既有部署脚本恢复旧应用容器；前端不发布。
- 前端依赖或构建失败：后端不部署、线上前端不覆盖。
- Nginx 校验失败：不重载 Nginx；不尝试删除前端目录或回滚数据库。
- 不输出 `.env` 内容、Token、数据库 URL 或密码。

## 测试

为脚本添加 shell mock 回归测试，覆盖：命令顺序、前端构建失败不触发后端、后端失败不发布前端、Nginx 校验失败不 reload、显式 Docker 网络传递、禁止 MySQL/卷/网络销毁命令、以及带 `Host` 头的后端健康检查。继续运行现有后端部署脚本测试与 Nginx 配置测试。
