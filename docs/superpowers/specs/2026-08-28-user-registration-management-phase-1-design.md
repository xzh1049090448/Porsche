# 用户注册管理一期设计

## 目标与边界

一期将 Porsche 从手机号/短信与长期 JWT 认证迁移为用户名密码认证、短期 Access JWT 与服务端可撤销会话。交付注册、登录、刷新、登出、本人会话管理、Root 一次性引导、基础角色门禁，以及注册/登录/安全设置的前端入口。

以下内容不在一期实现：Passkey/WebAuthn、Security Proof、PAT、Casbin 细粒度策略编辑、管理员用户全量管理/配额操作、第三方 OAuth、邮箱/手机号登录、钱包、模型/渠道配置与公开页面。它们必须复用本设计的用户、会话、角色和审计基础，在后续独立设计中实现。

## 已冻结决策

- 登录标识仅为不可修改、全局唯一、永久占用的 `username`；不支持邮箱、手机号或短信登录。
- 注册使用用户名与密码；密码采用 Argon2id 哈希，长度为 8–20 字符，并拦截常见弱密码。
- 角色为稳定 INT：普通用户 `1`、管理员 `10`、Root `100`。一期只有基于最低角色的门禁，不实现 Casbin 覆盖。
- 首个 Root 仅能由一次性部署引导配置创建；不存在默认密码，创建后该引导配置必须失效。现有 `ADMIN_TOKEN` 管理入口将在角色管理端点可用后退役。
- Redis 是生产认证依赖。Redis 不可用时，注册、登录、刷新和会话撤销均失败关闭；不得回退为不可撤销的 JWT。
- 单用户最多 50 个活跃会话；过去 24 小时最多签发 100 次会话。达到上限时淘汰最旧活跃会话，再建立新会话。
- 生产使用同一 HTTPS 主域。Refresh Cookie 固定 `HttpOnly; Secure; SameSite=Lax`；可信 Origin 仅为部署配置中明确列出的 HTTPS 来源。该 Cookie 选择优先于原 PRD 的 SameSite=Strict 描述。
- 登录失败按账户和来源 IP 双维度限流；连续 5 次失败后锁定 30 秒，并始终对客户端返回统一账号密码错误。
- 用户删除为墓碑软删除：吊销会话、递增 `auth_version`、清除认证凭据与个人敏感资料、保留用户名占用和最小审计记录。业务代码不执行物理 DELETE。

## 架构

```text
Porsche-Web
  ├─ 注册 / 登录表单
  ├─ Access JWT：仅内存保存
  └─ 401 单飞刷新：浏览器自动携带 Refresh Cookie
         │
         ▼
Porsche Go API
  ├─ Auth Service：密码、会话、轮换、账户状态
  ├─ JWT/Session Middleware：签名 + Redis/DB 会话否决检查
  ├─ Role Middleware：User / Admin / Root 最低角色门禁
  └─ Audit Service：认证安全事件
         │
         ├─ MySQL 8：用户、会话、认证审计（显式迁移）
         └─ Redis：账户/IP 限流、会话否决、30 秒轮换结果
```

Access JWT 有效期为 15 分钟，包含用户 `guid`、会话 `sid`、会话版本和用户 `auth_version`。JWT 不携带内部 `users.id`。每个受保护请求均需确认会话未吊销、未过期且版本匹配。

Refresh Token 形式为 `{sid}.{64-byte-random-secret}`。数据库只保存秘密的 HMAC 摘要；Cookie 不可由前端 JavaScript 读取。轮换成功后，Redis 以服务端加密形式暂存同一次轮换的新秘密 30 秒，使并发刷新返回同一结果。窗口外旧摘要重放会先写 Redis 否决栅栏、再在 MySQL 事务中吊销该会话。

## 数据模型与迁移

不得修改已发布的 `0001` 迁移。新增显式、可审查的 MySQL 8 迁移，并在部署前执行 `go run ./cmd/migrate up`。

### users 扩展

新增 `username`、`password_hash`、`role`、`auth_version`、`last_login_at` 与认证状态所需字段。用户名建立全局唯一索引，不因软删除而复用。密码哈希、认证版本和内部主键绝不出现在 DTO、URL 或 JWT 的可读声明中。

### user_sessions

每行均含内部 `id`、雪花 `guid`、四个审计字段和 `is_deleted`。`sid` 是会话凭据选择器，使用 UUID 并建立唯一索引；它不替代该表的 `guid`。其他字段包括 `user_id`、登录方式 INT、IP、UA、会话版本、当前/前一 Refresh HMAC、前一摘要宽限截止时间、创建/最后活跃/过期时间和吊销状态。常规查询固定过滤 `is_deleted=0`。

### auth_audit_events

记录注册、登录成功、刷新、重放吊销、登出、会话吊销、禁用及注销等安全事件。事件类型使用稳定 INT 枚举；关联用户只使用 `user_id`。不得写入密码、Refresh 明文、PAT、Cookie 或完整 Authorization 值。

新增或修改的所有列均遵守 `docs/conventions/database-standards.md`：`BIGINT` Unix 毫秒 UTC 时间、INT 枚举、`guid`、审计字段、软删除及 `user_id → users.id` 关联。测试使用专用 `*_test` MySQL 库，禁止连接 `DATABASE_URL` 指向的生产库。

## HTTP 契约

所有错误使用：

```json
{"error":{"code":"auth_invalid_credentials","message":"用户名或密码错误","type":"authentication_error","request_id":"req_..."}}
```

不得回显数据库、Redis、密码哈希、Refresh、JWT、Cookie 或用户是否存在/被禁用的内部细节。

| 端点 | 行为 |
| --- | --- |
| `POST /api/v1/auth/register` | 注册开关开启时创建普通用户、默认额度和审计事件。 |
| `POST /api/v1/auth/login` | 账户/IP 限流后校验密码，建立服务端会话，返回 Access JWT 并设置 Refresh Cookie。 |
| `POST /api/v1/auth/refresh` | 仅接受同源 Cookie；CAS 轮换并返回新的 Access JWT。 |
| `POST /api/v1/auth/logout` | 吊销当前会话并清除 Refresh Cookie。 |
| `GET /api/v1/auth/self` | 返回当前用户的 `guid`、用户名、角色和安全状态摘要。 |
| `GET /api/v1/auth/sessions` | 返回当前用户最多 100 个未删除、当前 auth_version 的会话，标记当前会话。 |
| `DELETE /api/v1/auth/sessions/:guid` | 仅吊销本人指定会话；对当前会话同步清 Cookie。 |
| `POST /api/v1/auth/sessions/revoke-others` | 保留当前会话，吊销其余会话。 |

所有会话资源 URL 使用会话 `guid`；`sid` 只作为不可预测的凭据关联值，永不作为通用管理资源 ID。

## 前端设计

- 注册和登录页只提交用户名/密码，不持久化密码或 Refresh Token。
- Auth Store 仅以内存持有 Access JWT 与基础用户资料；页面刷新通过受 Cookie 保护的 refresh 恢复。
- API 拦截器对并发 401 执行单飞 refresh，成功后重放原请求；失败时清空内存状态并跳转登录页。
- 个人安全设置列出会话并支持单个吊销、吊销其他设备和登出。显示时间、登录方式、IP/UA，不显示 Refresh、哈希或内部用户 ID。
- 一期管理员页仅提供低角色用户的列表、创建、启停和软删除；所有展示/路由使用用户 `guid`。后端角色检查是最终授权边界。

## 失败与一致性规则

- Redis 不可用、MySQL 认证事务失败或审计写入失败时，认证和会话变更失败，不得返回伪成功。
- 建立会话、写入 Refresh 摘要、更新 `last_login_at` 与认证审计在一个 MySQL 事务中完成；缓存发布只在提交后发生。
- 修改密码、禁用和删除用户时在同一事务递增 `auth_version` 并逻辑吊销会话；当前/旧 JWT 随即失效。
- Root 不能被禁用、删除或由其他 Root 管理；管理员只能管理严格低于自己的角色。

## 验收门禁

1. 真实隔离 MySQL 8 运行迁移、校验 checksum/锁，并跑完整 Go 测试。
2. 覆盖用户名永久唯一、Argon2id 校验、账号/IP 限流、禁用/软删、审计字段、GUID 不泄露和 `is_deleted` 读取过滤。
3. 覆盖 Refresh 并发 30 秒同结果、窗口外重放全会话吊销、Cookie/Origin 拒绝、50 会话淘汰与 24 小时签发上限。
4. 覆盖用户和角色越权、Root 保护、注销与会话吊销的事务一致性。
5. 前端单元和浏览器 E2E 覆盖注册、登录、刷新、会话管理和无权限状态；确认浏览器存储中没有 Refresh 或密码。

## 后续阶段

- 第二期：Passkey/WebAuthn、Security Proof 与 PAT。
- 第三期：Casbin `authz_roles` / `casbin_rule`、能力目录、管理员用户管理、权限编辑与配额治理。
- 第四期：PRD 中明确排除的第三方登录、系统设置和其它平台模块，逐项另行设计。
