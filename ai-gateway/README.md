# AI API Gateway（海外大模型统一接入 / OpenAI 兼容网关）

## 重要合规声明（必读）

**法律风险提示：**

- 本程序仅为**技术实现示例与架构参考**，开发者和使用者必须严格遵守《中华人民共和国网络安全法》《中华人民共和国数据安全法》《中华人民共和国个人信息保护法》等相关法律法规。
- **未经国家有关部门批准，不得擅自向境外提供数据处理服务**，不得搭建非法跨境数据传输通道。
- 不得利用本程序传播违法信息、从事危害国家安全和社会公共利益的活动。
- **使用者需自行承担因使用本程序而产生的一切法律责任。**  
  本仓库作者不对任何违法、违规或超授权使用承担责任。

**用途说明：**  
本服务设计目标为**合法合规的技术研究、企业内部经审批的 AI 能力接入与统一治理**（密钥集中管理、审计、配额与限流等）。在部署到任何环境之前，请完成法务与合规评估，并确保符合服务所在地及数据处理相关法规。

---

## 项目简介

`ai-gateway` 是一个基于 **FastAPI + httpx** 的异步网关，对外提供 **OpenAI 兼容的** `POST /v1/chat/completions` 接口，在服务端将请求路由到多个上游（OpenAI 兼容端点、Anthropic Messages、Google Gemini REST 等），并支持：

- **模型别名与动态路由**：`config/models.yaml` 配置逻辑模型名 → 上游 provider / 真实模型 / 密钥环境变量名；支持 **`POST /admin/reload-config` 热加载**（无需重启）。
- **上游密钥池**：同一逻辑模型多密钥 **轮询**；对 401/403/429/5xx 等触发 **熔断跳过**（可配置阈值与冷却时间）。
- **下游客户端密钥**：`config/clients.yaml` 配置每客户端 **Bearer** 密钥、**可用模型列表**、**RPM/TPM**、**每日/每月 token 上限（建议配合 Redis）**、**IP 白名单**。
- **流式响应（SSE）**：OpenAI 兼容上游为**透传**；Anthropic 为 **SSE 解析后转写为 OpenAI chunk 格式**；Gemini 当前为 **非流式聚合后模拟 SSE**（见下文限制说明）。
- **可观测性**：`/health`、`/metrics`（Prometheus 文本格式）、结构化日志（loguru）。

> **说明**：本实现覆盖生产网关的**核心骨架与扩展点**；敏感词过滤、Webhook、批量接口、完整 Gemini 流式、Prometheus 全量业务标签等可作为后续迭代（代码中已预留清晰模块边界）。

---

## 技术栈

| 组件 | 版本要求 |
|------|-----------|
| Python | 3.10+（Docker 镜像使用 3.11） |
| FastAPI | 0.110+ |
| httpx | 0.27+ |
| Pydantic | 2.0+ |
| python-dotenv / pydantic-settings | 环境变量与配置 |
| loguru | 日志 |
| Redis（可选） | 分布式 RPM/TPM 计数与 token 用量累计 |
| Prometheus Client | `/metrics` |

---

## 快速开始

### 1. 准备配置

```bash
cd ai-gateway
cp .env.example .env
# 编辑 .env：至少设置 ADMIN_TOKEN；按需设置 REDIS_URL
```

在 `.env` 或部署环境中配置**上游密钥**（逗号或换行分隔多密钥，支持轮询）：

| 环境变量 | 含义 |
|-----------|------|
| `OPENAI_API_KEYS` | OpenAI 官方或兼容 `/v1/chat/completions` 的密钥 |
| `ANTHROPIC_API_KEYS` | Anthropic `x-api-key` |
| `GOOGLE_API_KEYS` | Google AI Studio / Gemini API Key（query `key=`） |
| `MISTRAL_API_KEYS` | Mistral OpenAI 兼容密钥 |

逻辑模型与 `api_keys_env` 的对应关系见 `config/models.yaml`（可从 `config/models.example.yaml` 复制修改）。

下游客户端见 `config/clients.yaml`（示例见 `config/clients.example.yaml`）。

### 2. 本地运行（虚拟环境）

```bash
python -m venv .venv
. .venv/bin/activate   # Windows: .venv\Scripts\activate
pip install -r requirements.txt
uvicorn app.main:app --reload --host 0.0.0.0 --port 8000
```

- OpenAPI 文档（非 `production` 的 `APP_ENV`）：<http://127.0.0.1:8000/docs>  
- 健康检查：<http://127.0.0.1:8000/health>  
- 指标：<http://127.0.0.1:8000/metrics>  

### 3. Docker Compose

```bash
cd ai-gateway
cp .env.example .env
# 填写 ADMIN_TOKEN 与各 OPENAI_API_KEYS / ANTHROPIC_API_KEYS 等
docker compose up --build
```

Compose 默认启动 **Redis**，并将 `REDIS_URL=redis://redis:6379/0` 注入网关，用于 RPM/TPM 与 token 用量累计。

### 4. 调用示例（OpenAI SDK 指向网关）

将 `base_url` 设为你的网关地址，`api_key` 设为 `clients.yaml` 中的下游 `secret`（非上游密钥）：

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:8000/v1",
    api_key="sk-client-dev-change-me",
)
resp = client.chat.completions.create(
    model="gpt-3.5-turbo",
    messages=[{"role": "user", "content": "Hello"}],
)
print(resp.choices[0].message.content)
```

流式：

```python
stream = client.chat.completions.create(
    model="claude-3-5-sonnet",
    messages=[{"role": "user", "content": "Hi"}],
    stream=True,
)
for chunk in stream:
    print(chunk.choices[0].delta.content or "", end="")
```

---

## 主要 HTTP 接口

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/v1/chat/completions` | OpenAI 兼容聊天（需 `Authorization: Bearer <下游密钥>`） |
| `GET` | `/health` | 健康检查 |
| `GET` | `/metrics` | Prometheus 指标 |
| `GET` | `/admin/status` | 管理：路由与客户端数量（需 `Authorization: Bearer <ADMIN_TOKEN>`） |
| `POST` | `/admin/reload-config` | 热加载 `models.yaml` 与 `clients.yaml` |

---

## 环境变量说明（摘录）

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `APP_ENV` | `development` | `production` 时关闭 `/docs` |
| `HOST` / `PORT` | `0.0.0.0` / `8000` | Uvicorn 监听（Dockerfile 内固定 8000） |
| `ADMIN_TOKEN` | （开发默认见 `config.py`，**生产必须覆盖**） | 管理接口 Bearer |
| `MODELS_CONFIG_PATH` | `config/models.yaml` | 模型路由文件路径 |
| `CLIENTS_CONFIG_PATH` | `config/clients.yaml` | 下游客户端策略文件路径 |
| `REDIS_URL` | 空 | 设置后启用分布式 RPM/TPM 与 token 用量累计 |
| `UPSTREAM_TIMEOUT_SECONDS` | `120` | 上游超时 |
| `CIRCUIT_FAILURE_THRESHOLD` | `5` | 熔断前连续失败次数（按密钥） |
| `CIRCUIT_OPEN_SECONDS` | `60` | 熔断打开时长（秒） |

完整示例见 `.env.example`。

---

## 多环境

通过不同 `.env` 或编排系统注入环境变量区分 **development / staging / production**（`APP_ENV`）。生产建议：

- 关闭公网文档（已随 `APP_ENV=production` 关闭）。
- 强随机 `ADMIN_TOKEN`、最小权限挂载 `config/*.yaml`、TLS 终止（Nginx / Ingress）。
- **务必使用 Redis** 以实现多副本间一致的限流与用量统计。

---

## 测试

```bash
cd ai-gateway
pip install -r requirements.txt
pytest
```

---

## 当前限制与扩展建议

- **Gemini**：当前为 REST `generateContent`；`stream=true` 时为**模拟 SSE**（单条合成流）。完整 `streamGenerateContent` 可后续补充。
- **Anthropic**：已实现文本类流式 delta 的主要路径；复杂 tool-use 全量对齐需进一步测试与扩展。
- **OpenAI 兼容上游**：请求体支持 **Pydantic `extra='allow'`**，便于透传 `stream_options`、`frequency_penalty` 等字段。
- **敏感内容过滤 / Webhook / 批量 / 响应缓存**：未内建，可在 `app/services/gateway.py` 前后增加中间件或钩子实现。

---

## 常见问题（FAQ）

**Q：修改模型路由后一定要重启吗？**  
A：不需要。更新 `config/models.yaml` 后调用 `POST /admin/reload-config`（带 `ADMIN_TOKEN`）即可。

**Q：下游客户端会看到上游 OpenAI 密钥吗？**  
A：不会。上游密钥只存在于服务端环境变量或密钥管理系统中；客户端仅使用 `clients.yaml` 中的下游 Bearer。

**Q：没有 Redis 时配额准确吗？**  
A：RPM/TPM 在单进程内仍有效；**每日/每月 token 上限**在 Redis 不可用时预检查会放宽（建议在 README 与运维手册中强制生产使用 Redis）。可在后续版本为纯内存模式补全预检查逻辑。

---

## 目录结构（摘录）

```text
ai-gateway/
  app/
    main.py                 # FastAPI 入口
    config.py               # 环境变量配置
    state.py                # 运行时状态
    api/routes/             # Controller 层（HTTP 接口）
    common/
      schemas/              # Req/Resp DTO
      constants/            # 常量
      errors.py             # 错误响应
      exceptions.py         # 上游异常
      security.py           # JWT / 密码
    service/                # 业务逻辑
    repository/             # 数据访问（models / session）
    task/                   # 定时任务（预留）
    tool/                   # 工具（日志、启动检查等）
    providers/              # 上游 LLM 适配
    observability/          # Prometheus 指标
  config/
    models.yaml
    clients.yaml
  CONTEXT.md                # 领域上下文与模块说明
  Dockerfile
  docker-compose.yml
  requirements.txt
  README.md
```

模块分层说明见 [CONTEXT.md](CONTEXT.md) 与仓库根目录 [docs/conventions/module-structure.md](../docs/conventions/module-structure.md)。

---

## 免责声明（再次强调）

在将本网关用于任何**跨境、对外提供或处理个人信息/重要数据**的场景前，请咨询专业法律顾问与主管部门要求。**禁止将本示例用于任何违法用途。**
