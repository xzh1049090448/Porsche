# ai-gateway-go

Go 语言重构版 **国内大模型聚合平台 API**，与原 Python 项目 [`ai-gateway`](../ai-gateway) **路径与 JSON 契约完全对齐**，前端无需改动即可切换后端。

原 `ai-gateway` 目录保持不变；本目录为独立新项目。

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
| 平台对话/对比 | `/api/v1/platform/*` | ✅（compare 流式待补） |
| 模型分析 | `/api/v1/billing/analytics/*` | ✅（图表序列简化实现） |
| 管理端 | `/admin/*` | ✅ |
| Prometheus | `GET /metrics` | ✅ |

## 快速开始

### 1. 环境要求

- Go 1.22+
- （可选）Redis：验证码/限流多实例部署时使用

### 2. 配置

```bash
cd ai-gateway-go
cp .env.example .env
# 填写 DEEPSEEK_API_KEYS、GLM_API_KEYS、JWT_SECRET_KEY、ADMIN_TOKEN 等
```

配置文件与 Python 版共用相同 YAML：

- `config/models.yaml` — 模型路由
- `config/clients.yaml` — 下游客户端密钥

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

## 与 Python 版的差异（实现层）

| 项目 | Python 版 | Go 版 |
|------|-----------|-------|
| Web 框架 | FastAPI | Gin |
| ORM | SQLAlchemy async | GORM |
| 向量检索 | ChromaDB | 本地 JSON + 关键词检索（API 行为一致，精度略低） |
| 模型分析图表 | 完整时序聚合 | 汇总/排行已实现，时序 series 返回空数组 |
| compare 流式 | SSE 多路输出 | 暂返回 501，非流式正常 |

数据库文件默认 `./data/platform.db`（SQLite），可与 Python 版共用同一库文件（表结构兼容）。

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
ai-gateway-go/
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
└── tests/
```
