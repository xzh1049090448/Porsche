# ai-gateway-go

Go 语言实现的 **国内大模型聚合平台 API**。服务路径与既有前端契约保持一致。

## 功能对齐

| 模块 | 路径前缀 | 状态 |
|------|----------|------|
| 健康检查 | `GET /health` | ✅ |
| OpenAI 兼容网关 | `POST /v1/chat/completions` | ✅ |
| 用户认证 | `/api/v1/auth/*` | ✅ |
| 用户资料 | `/api/v1/users/*` | ✅ |
| 对话 CRUD | `/api/v1/conversations/*` | ✅ |
| 数据集列表 | `/api/v1/datasets` | ✅ |
| 套餐/订单 | `/api/v1/billing/*` | ✅ |
| 平台对话/对比 | `/api/v1/platform/*` | ✅ |
| 模型分析 | `/api/v1/billing/analytics/*` | ✅（图表序列简化实现） |
| 管理端 | `/admin/*` | ✅ |
| Prometheus | `GET /metrics` | ✅ |

## 快速开始

### 1. 环境要求

- Go 1.22+
- （可选）Redis：验证码/限流多实例部署时使用

### 2. 配置

```bash
cp .env.example .env
# 填写 DEEPSEEK_API_KEYS、GLM_API_KEYS、JWT_SECRET_KEY、ADMIN_TOKEN 等
```

配置文件：

- `config/models.yaml` — 模型路由
- `config/clients.yaml` — 下游客户端密钥

如需使用既有 MySQL 数据，请在 `.env` 中设置原有的连接串。Go 同时支持
`mysql+aiomysql://...` 与 `mysql://...`，例如：

```dotenv
DATABASE_URL=mysql+aiomysql://platform:platform@127.0.0.1:3306/platform
```

删除旧代码目录不会删除 MySQL 数据：数据保存在外部 MySQL 服务或 Docker 命名卷中。不要执行
`docker compose down -v` 或 `docker volume rm`，否则才会删除 Docker 卷中的数据库文件。

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

数据库默认使用 `./data/platform.db`（SQLite）。生产环境建议配置 MySQL；已有 MySQL 数据库可直接复用。

## 测试

```bash
go test ./...
```

## Docker

```bash
docker build -t ai-gateway-go .
docker run --env-file .env -p 8000:8000 ai-gateway-go
```

## 目录结构

```
Porsche/
├── cmd/server/main.go      # 入口
├── config/                 # models.yaml / clients.yaml
├── internal/
│   ├── handler/            # HTTP 路由（与 Python routes 一一对应）
│   ├── service/            # 业务逻辑
│   ├── gateway/            # 上游模型代理
│   ├── registry/           # 模型/客户端配置
│   ├── rag/                # RAG 检索
│   ├── models/             # GORM 实体
│   └── router/             # 路由注册
```
