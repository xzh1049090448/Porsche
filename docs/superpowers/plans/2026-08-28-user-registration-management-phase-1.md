# 用户注册管理一期 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 以用户名密码、可撤销会话和基础角色门禁替代手机号/长期 JWT 认证，并提供一期前端认证与会话管理。

**Architecture:** MySQL 8 显式 `0002` 迁移扩展用户并创建会话/认证审计表；AuthService 在事务中建立、刷新和吊销会话。Redis 是认证路径必需依赖，负责限流、否决栅栏和 Refresh 30 秒并发结果。前端仅将短期 Access Token 保存在内存，Refresh 只由 HttpOnly Cookie 携带。

**Tech Stack:** Go、Gin、GORM、MySQL 8、Redis、Argon2id、JWT、Vue 3、Pinia、Axios、Element Plus。

---

## 固定数据与契约

- `UserRole`: `1=user`、`10=admin`、`100=root`；业务用户不得为 `0`。
- Access JWT：15 分钟，claims 为 `sub=<user guid>`、`sid`、`sv`、`av`、`role`。
- Refresh：Cookie 名 `porsche_refresh`；`HttpOnly; Secure; SameSite=Lax`；形式 `{sid}.{64-byte-random-secret}`；数据库仅存 HMAC。
- 所有 Auth API 错误采用 `error.{code,message,type,request_id}`；错误不回显账号状态、密码、令牌、Cookie、数据库或 Redis 信息。
- 用户和会话对外标识为字符串 `guid`，绝不输出内部 `id`/`user_id`。

### Task 1: 激活一期追踪并定义认证配置

**Files:**
- Modify: `feature_list.json`, `progress.md`
- Modify: `internal/config/config.go`, `internal/config/config_test.go`, `.env.example`, `README.md`

- [ ] **Step 1: 写失败配置测试**
  ```go
  func TestLoadRejectsUnsafeAuthProductionConfiguration(t *testing.T) {
      t.Setenv("APP_ENV", "production")
      t.Setenv("REGISTER_ENABLED", "true")
      t.Setenv("REDIS_URL", "")
      _, err := config.Load()
      require.ErrorContains(t, err, "REDIS_URL")
  }
  ```
- [ ] **Step 2: 运行配置测试，确认缺少生产认证配置时失败**
  Run: `go test ./internal/config -run TestLoadRejectsUnsafeAuthProductionConfiguration -count=1`
- [ ] **Step 3: 实现设置与文档**
  增加 `RegisterEnabled`、`PasswordRegisterEnabled`、`PasswordLoginEnabled`、`SessionAccessMinutes=15`、`SessionDays=30`、`SessionMaxActive=50`、`SessionIssueLimit24h=100`、`RefreshReplaySeconds=30`、`AuthTrustedOrigins`、`RootBootstrapUsername`、`RootBootstrapPassword`、`AuthHMACKey`。生产环境拒绝空 Redis、默认 JWT/HMAC/Root 密钥、非 HTTPS Origin 和不合法 Root 引导；文档要求一次性 Root 环境变量在成功引导后移除。
- [ ] **Step 4: 更新追踪器**
  将 `go-004` 标为 `blocked`，原因写为“真实部署环境 JieKou 冒烟待办”；新增唯一 `in_progress` 的用户注册一期条目，不声称已验证。
- [ ] **Step 5: 运行并提交**
  Run: `go test ./internal/config -count=1 && git diff --check`
  Commit: `feat: add fail-closed auth configuration`

### Task 2: 迁移与持久化实体

**Files:**
- Create: `internal/migration/sql/0002_auth_core.up.sql`, `internal/migration/sql/0002_auth_core.down.sql`
- Modify: `internal/models/models.go`, `internal/models/models_contract_test.go`, `internal/migration/runner_test.go`

- [ ] **Step 1: 写 MySQL 迁移/实体契约测试**
  测试 `users.username` 全局唯一、`user_sessions`/`auth_audit_events` 包含 `id,guid,created_at,created_by,updated_at,updated_by,is_deleted`，时间列均为 `BIGINT`，角色/登录方法/审计事件为 `INT`。
- [ ] **Step 2: 运行测试确认 0002 与实体尚不存在**
  Run: `TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./internal/migration ./internal/models -run Auth -count=1`
- [ ] **Step 3: 写迁移与模型**
  `ALTER TABLE users ADD username VARCHAR(20) NULL, ADD role INT NOT NULL DEFAULT 1, ADD auth_version INT NOT NULL DEFAULT 1, ADD last_login_at BIGINT NULL`，建立全局用户名唯一索引；创建 `user_sessions`（含 UUID `sid` 唯一键、`user_id` FK、Refresh HMAC 当前/前一摘要和毫秒到期字段）与 `auth_audit_events`。新增 `UserRole`、`LoginMethod`、`AuthAuditEvent` 明确数值映射及 `Session`/`AuthAuditEvent` GORM 实体；创建路径用共享雪花/audit helper。
- [ ] **Step 4: 运行真实隔离库迁移测试**
  Run: `TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./internal/migration ./internal/models -count=1`
- [ ] **Step 5: 提交**
  Commit: `feat: add auth session schema`

### Task 3: Redis 认证存储与会话服务

**Files:**
- Create: `internal/service/auth_session.go`, `internal/service/auth_session_test.go`, `internal/service/auth_redis.go`, `internal/service/auth_redis_test.go`
- Modify: `go.mod`, `go.sum`, `internal/app/state.go`, `internal/security/security.go`

- [ ] **Step 1: 写失败测试**
  覆盖：账户/IP 第五次失败后 30 秒拒绝、创建第 51 个会话淘汰最旧、Refresh 并发窗口返回同一新 Secret、窗口外旧 Secret 重放将会话吊销。
- [ ] **Step 2: 运行测试确认服务不存在**
  Run: `go test ./internal/service -run 'Test(AuthSession|RefreshRotation|LoginRateLimit)' -count=1`
- [ ] **Step 3: 实现最小服务**
  使用 `go-redis/v9`，实现 `AuthRedis` 的限流、否决、短期加密轮换结果；实现 `SessionService.Create/Refresh/Revoke/RevokeOthers/Validate`。MySQL 变更必须在事务中完成，Redis 写失败返回错误；使用 `crypto/rand` 生成 Refresh Secret、HMAC-SHA256 摘要、AEAD 包装短期并发结果。
- [ ] **Step 4: 运行测试**
  Run: `go test ./internal/service -run 'Test(AuthSession|RefreshRotation|LoginRateLimit)' -count=1`
- [ ] **Step 5: 提交**
  Commit: `feat: add revocable auth sessions`

### Task 4: 用户名认证、Root 引导与 JWT/角色中间件

**Files:**
- Modify: `internal/service/auth.go`, `internal/service/auth_jwt_test.go`, `internal/middleware/middleware.go`, `internal/middleware/jwt.go`, `internal/models/models.go`
- Create: `internal/service/auth_registration_test.go`, `internal/middleware/auth_session_test.go`

- [ ] **Step 1: 写失败测试**
  覆盖用户名 trim/长度/永久唯一、Argon2id 密码校验、Root 仅首次创建、禁用/软删用户被拒绝、JWT `sub` 仅为 guid、`sid/sv/av/role` 不匹配即 401、Admin/Root 最低角色门禁。
- [ ] **Step 2: 运行失败测试**
  Run: `go test ./internal/service ./internal/middleware -run 'Test(Username|RootBootstrap|SessionClaims|Role)' -count=1`
- [ ] **Step 3: 实现认证收口**
  以 `RegisterUsername` 和 `LoginUsername` 替换手机号/短信路径；密码使用 Argon2id；`makeToken` 接收 Session 并加入固定 claims；`RequireUser` 调用 `SessionService.Validate`；新增 `RequireAdmin`/`RequireRoot`，不再比较 `ADMIN_TOKEN`。
- [ ] **Step 4: 运行测试**
  Run: `go test ./internal/service ./internal/middleware -count=1`
- [ ] **Step 5: 提交**
  Commit: `feat: add username authentication and roles`

### Task 5: HTTP Auth、会话与基础管理员端点

**Files:**
- Modify: `internal/handler/auth_users.go`, `internal/handler/admin.go`, `internal/router/router.go`, `internal/dto/*_test.go`
- Create: `internal/handler/auth_sessions_test.go`, `internal/handler/admin_users_auth_test.go`

- [ ] **Step 1: 写 HTTP 失败测试**
  使用 `httptest` 覆盖注册/登录 JSON 封套、Refresh Origin 拒绝、Cookie 属性、logout、会话列表/本人吊销、吊销其他设备、管理员不能管理同级/Root，且响应没有 `id`、哈希或 Refresh。
- [ ] **Step 2: 运行测试确认端点不存在或契约不符**
  Run: `go test ./internal/handler ./internal/router -run 'Test(AuthSession|AdminUser)' -count=1`
- [ ] **Step 3: 实现端点与 DTO**
  注册 `/api/v1/auth/{register,login,refresh,logout,self,sessions}`；在 refresh/logout 加 Origin 守卫和 `X-Auth-Session` 一致性校验；管理员端点只使用用户 guid，执行 actor/target 角色比较和软删除。删除旧 `/send-code`、`/login/code`、固定账号路径及 `ADMIN_TOKEN` 管理保护。
- [ ] **Step 4: 运行 HTTP 测试**
  Run: `go test ./internal/handler ./internal/router -count=1`
- [ ] **Step 5: 提交**
  Commit: `feat: expose session authentication APIs`

### Task 6: 前端内存 Access、注册登录与会话安全页

**Files:**
- Modify: `/Users/xuzhihao/code/Porsche-Web/src/api/auth.js`, `src/api/request.js`, `src/stores/user.js`, `src/router/index.js`, `src/views/Login.vue`, `src/views/Profile.vue`, `src/i18n/messages.js`
- Create: `/Users/xuzhihao/code/Porsche-Web/src/views/Register.vue`, `src/api/auth-session.test.js`, `src/stores/user-auth.test.js`

- [ ] **Step 1: 写失败前端测试**
  测试用户名注册/登录 payload、Access 不写 localStorage/sessionStorage、401 多请求只发一次 refresh、Refresh 失败清空内存并跳转登录、会话 API 不展示敏感字段。
- [ ] **Step 2: 运行测试确认当前实现写入 token storage**
  Run: `npm test -- --test-name-pattern='auth|session'`
- [ ] **Step 3: 实现前端认证**
  让 Axios 使用 `withCredentials: true`；Pinia 内存保存 Access/User；实现单飞 refresh 队列与原请求重放；新增 `/register` 页面和 Profile 会话管理卡；路由守卫以 store 初始化/refresh 结果判定，不再从 storage 直接读取 token。
- [ ] **Step 4: 运行前端验证**
  Run: `npm test && npm run build`
- [ ] **Step 5: 提交**
  Commit: `feat: add session-based frontend authentication`

### Task 7: 真实 MySQL 8、Redis 与端到端门禁

**Files:**
- Modify: `README.md`, `.env.example`, `feature_list.json`, `progress.md`
- Create: `internal/service/auth_mysql_integration_test.go`, `deploy/test-auth-config.sh`

- [ ] **Step 1: 写并发/重放集成测试**
  在受控 `*_test` MySQL 与专用 Redis DB 中并发刷新同一 Cookie，断言只产生一个轮换结果；窗口外重放后原 JWT 和 Cookie 都被拒绝。
- [ ] **Step 2: 运行集成测试**
  Run: `TEST_DATABASE_URL="$TEST_DATABASE_URL" TEST_REDIS_URL="$TEST_REDIS_URL" go test ./... -count=1`
- [ ] **Step 3: 更新部署文档与追踪证据**
  文档要求先 `go run ./cmd/migrate up`、Redis 健康、HTTPS Cookie/Origin 配置和一次性 Root 引导移除；仅在全部自动化与浏览器 E2E 已执行后标记一期为 `passing`。
- [ ] **Step 4: 运行最终检查**
  Run: `go test ./... -count=1 && go vet ./... && git diff --check`; frontend `npm test && npm run build`。
- [ ] **Step 5: 提交**
  Commit: `docs: verify user registration phase one`

## 计划自检

- 设计中的认证、会话、数据标准、HTTP、前端与验收要求分别由 Tasks 1–7 覆盖。
- Passkey、PAT 和 Casbin 被明确排除，未在任务中暗中实现。
- 每个任务均先 RED、后最小实现、再 GREEN 并提交；真实 MySQL/Redis 集成在最后门禁中强制执行。
