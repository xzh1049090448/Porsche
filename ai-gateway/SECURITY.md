# ai-gateway 安全配置说明

## 生产 / 预发（staging）必选项

| 变量 | 要求 |
|------|------|
| `APP_ENV` | `production` 或 `staging` |
| `ADMIN_TOKEN` | 强随机字符串，勿使用默认值 |
| `JWT_SECRET_KEY` | 强随机字符串 |
| `PLATFORM_CLIENT_SECRET` | 强随机，与 `clients.yaml` 中 platform-internal 一致 |
| `SMS_DEV_MODE` | `false` |
| `BILLING_ALLOW_MOCK_PAYMENT` | `false` |
| `REAL_NAME_AUTO_VERIFY` | `false`（对接 KYC 前） |
| `REDIS_URL` | 建议配置（验证码限流、分布式限流） |
| `TRUST_PROXY_HEADERS` | 仅在反向代理后设为 `true` |

启动时 `validate_settings()` 会在 `production` / `staging` 下校验上述项，配置不安全将拒绝启动。

## 开发环境

- `APP_ENV=development` 时，若未设置 `BILLING_ALLOW_MOCK_PAYMENT`，默认允许模拟支付（便于联调）。
- `SMS_DEV_MODE=true` 时验证码会出现在 `send-code` 响应中，**切勿用于生产**。

## 已加固接口

- `GET /metrics` — 需 `Bearer`（`METRICS_TOKEN` 或 `ADMIN_TOKEN`）
- `GET /api/v1/platform/models` — 需用户 JWT
- `POST /api/v1/auth/send-code` — 手机号 / IP 限流
- `POST /api/v1/billing/orders/{id}/pay` — 非 mock 模式下拒绝

## 本地验证

```powershell
# 先启动服务
uvicorn app.main:app --host 127.0.0.1 --port 8000

# 另开终端
.\scripts\verify_security.ps1 -AdminToken "<你的 ADMIN_TOKEN>"
```

或：`pytest tests/test_security_audit.py tests/test_billing_security.py -v`
