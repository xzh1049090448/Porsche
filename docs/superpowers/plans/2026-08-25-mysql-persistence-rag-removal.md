# MySQL 持久化标准化与 RAG 移除 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 Porsche 重构为仅使用 MySQL 8 的模型中转服务，使用显式迁移、雪花 GUID、审计字段、Unix 毫秒、整数枚举和逻辑删除，并完全移除 RAG 与数据集功能。

**Architecture:** MySQL schema 由内嵌、版本化 SQL migration runner 管理，应用启动只验证连接和版本。持久化模型以内部 `BIGINT id` 关联、以雪花 `guid` 对外；Service 集中处理审计与 `is_deleted = 0` scope，Handler/DTO 只接收和输出 GUID 字符串。Porsche-Web 同批删除 RAG 字段并改用 GUID。

**Tech Stack:** Go 1.22、Gin、GORM（MySQL 8 方言，不使用 AutoMigrate）、嵌入式 SQL、Vue 3/Vite、Node 内置测试、MySQL 8 集成测试库。

---

## 不变量

- 仅 MySQL 8：删除 SQLite dialect 与 SQLite 测试路径。
- 所有表（包括 `schema_migrations`）均有 `id BIGINT`、唯一雪花 `guid BIGINT`、`created_at/created_by/updated_at/updated_by`、`is_deleted`。
- 所有时间存 Unix 毫秒 `BIGINT`；所有受控状态存 `INT`；所有用户外键是 `BIGINT user_id -> users.id`。
- API 中业务资源标识都是 GUID 字符串，禁止泄露内部 `id` / `user_id`；JWT 内部 subject 可保留用户主键。
- 删除为逻辑删除；默认 scope、预加载、认证、授权和统计均排除 `is_deleted=1`。
- 不执行 `DROP DATABASE`、`DROP TABLE` 或任何删除数据/卷的命令。migration 仅在明确的新目标库上创建 schema。
- 删除 RAG 全栈能力，保留会话、消息、用量、订单、Chat/Compare/SSE 和 context window。

## 执行里程碑

任务 1、任务 2、任务 3 与任务 4 构成一个**后端原子重构里程碑**。它们可以按测试先行提交内部检查点，但在四项均完成前不得部署、不得执行生产 migration、不得标记任一项为可交付：初始 MySQL schema、实体字段、枚举、读写服务和 HTTP 契约必须同步切换，不能让新 schema 与旧运行时代码共存。

该里程碑完成后，先执行任务 5 的 MySQL 8 集成测试与后端安全门禁；只有后端门禁通过，才能开始任务 6 的 Porsche-Web 同步改造。任务 7 是全栈最终交付门禁。

### Task 1: 建立 MySQL 迁移、Snowflake 基础设施并前移移除 RAG

**Files:**
- Create: `internal/persistence/snowflake.go`
- Create: `internal/persistence/snowflake_test.go`
- Create: `internal/persistence/clock.go`
- Create: `internal/migration/runner.go`
- Create: `internal/migration/runner_test.go`
- Create: `internal/migration/sql/0001_initial_schema.up.sql`
- Create: `internal/migration/sql/0001_initial_schema.down.sql`
- Create: `cmd/migrate/main.go`
- Delete: `internal/rag/rag.go`
- Delete: `internal/service/seed.go`
- Modify: `cmd/server/main.go`
- Modify: `internal/app/state.go`
- Modify: `internal/router/router.go`
- Modify: `internal/service/platform_chat.go`
- Modify: `internal/handler/conversations_datasets.go`
- Modify: `internal/handler/platform.go`
- Modify: `internal/handler/admin.go`
- Modify: `internal/config/config.go`
- Modify: `internal/db/db.go`
- Modify: `go.mod`, `go.sum`
- Test: `internal/config/config_test.go`, `internal/db/db_test.go`

- [ ] **Step 1: 写失败测试，固定 MySQL-only 与节点配置契约。**

```go
func TestLoadRejectsNonMySQLDatabaseURL(t *testing.T) {
    t.Setenv("DATABASE_URL", "sqlite://./data/platform.db")
    t.Setenv("SNOWFLAKE_NODE_ID", "0")
    if _, err := config.Load(); err == nil { t.Fatal("expected MySQL-only validation failure") }
}

func TestSnowflakeProducesUniqueSignedInt64(t *testing.T) {
    g := persistence.NewSnowflake(0, fixedClock)
    a, b := g.Next(), g.Next()
    if a <= 0 || b <= a { t.Fatalf("invalid IDs %d %d", a, b) }
}
```

- [ ] **Step 2: 运行失败测试。**

Run: `go test ./internal/config ./internal/persistence -run 'Test(LoadRejectsNonMySQLDatabaseURL|SnowflakeProducesUniqueSignedInt64)' -count=1`

Expected: FAIL，因缺少 MySQL-only 配置校验与 snowflake 包。

- [ ] **Step 3: 先移除 RAG 启动依赖。**

删除 RAG engine、默认数据集 seed、数据集路由注册和启动目录创建；从 Platform service/handler 请求结构删除 `dataset_enabled` 与 `dataset_ids`，使 strict decoder 拒绝它们。保留会话、消息、用量、账务和 `context_window`，并直接将裁剪后的消息传给白牌上游。必须先让应用在没有 `datasets` 表时能够构建和启动，再加入 8 表 initial migration。

- [ ] **Step 4: 实现基础设施。**

实现 `SNOWFLAKE_NODE_ID` 的 `[0,1023]` 校验；生成器以 mutex 保护毫秒/序列，始终生成小于 `math.MaxInt64` 的正 `int64`。`db.Open` 仅接受 MySQL URL，删除 SQLite imports、分支和 `AutoMigrate`；migration runner 以 `embed.FS` 按版本执行 SQL、在事务中写入 `schema_migrations` 并暴露 `Up` / `Status`。

- [ ] **Step 5: 编写初始 schema SQL。**

`0001_initial_schema.up.sql` 先创建合规 `schema_migrations`，再创建业务表；每表使用如下公共列并建立 `guid` 唯一索引：

```sql
id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
guid BIGINT NOT NULL,
created_at BIGINT NOT NULL,
created_by BIGINT NULL,
updated_at BIGINT NOT NULL,
updated_by BIGINT NULL,
is_deleted INT NOT NULL DEFAULT 0,
UNIQUE KEY uk_<table>_guid (guid)
```

为 user-owned 表建立 `(user_id, is_deleted)` 索引；为常用时间排序建立 `(..., updated_at)` 或 `(..., created_at)` 索引。down migration 只在显式开发/测试命令中使用，不得由服务或部署脚本调用。

- [ ] **Step 6: 运行迁移与基础测试。**

Run: `go test ./internal/config ./internal/persistence ./internal/migration ./internal/db -count=1`

Expected: PASS。

- [ ] **Step 7: 提交。**

```bash
git add internal/persistence internal/migration internal/rag internal/service/seed.go cmd internal/config internal/db go.mod go.sum
git commit -m "refactor: initialize mysql schema without rag"
```

### Task 2: 重建实体、整数枚举、审计与活动记录 Scope（后端原子里程碑内部任务）

**Files:**
- Create: `internal/models/base.go`
- Create: `internal/models/enums.go`
- Create: `internal/models/scopes.go`
- Create: `internal/models/enums_test.go`
- Create: `internal/models/scopes_test.go`
- Modify: `internal/models/models.go`
- Modify: `internal/models/json_types.go`
- Modify: `internal/dto/serializers.go`

- [ ] **Step 1: 写失败测试，固定实体不变量。**

```go
func TestActiveScopeExcludesLogicalDeletion(t *testing.T) {
    q := models.Active(db).Find(&[]models.User{})
    if !strings.Contains(q.Statement.SQL.String(), "is_deleted = 0") { t.Fatal("missing active scope") }
}

func TestGatewayTokenStatusUsesStableIntegerCodes(t *testing.T) {
    if models.GatewayTokenActive.Code() != 1 { t.Fatal("active code changed") }
    if _, err := models.ParseGatewayTokenStatus(1); err != nil { t.Fatal(err) }
}
```

- [ ] **Step 2: 重写八个实体。**

保留 `User`、`Conversation`、`Message`、`UsageRecord`、`Order`、`AuditLog`、`ModelHealth`、`GatewayAPIToken`；删除 Dataset/DatasetVersion 和所有 dataset 字段。所有 ID/FK 使用 `int64`，嵌入如下基础字段：

```go
type RecordMeta struct {
    ID        int64 `gorm:"primaryKey;type:bigint" json:"-"`
    GUID      int64 `gorm:"column:guid;type:bigint;not null;uniqueIndex" json:"guid"`
    CreatedAt int64 `gorm:"column:created_at;type:bigint;not null" json:"-"`
    CreatedBy *int64 `gorm:"column:created_by;type:bigint" json:"-"`
    UpdatedAt int64 `gorm:"column:updated_at;type:bigint;not null" json:"-"`
    UpdatedBy *int64 `gorm:"column:updated_by;type:bigint" json:"-"`
    IsDeleted int `gorm:"column:is_deleted;type:int;not null;default:0" json:"-"`
}
```

为 `UserStatus`、`PlanType`、`GatewayTokenStatus`、`OrderStatus`、`MessageRole`、`UsageRecordType` 定义显式整数常量、`Code`、`Parse` 和 JSON 文本映射。不得使用字符串 enum、`iota` 或 GORM 自动时间字段。

- [ ] **Step 3: 实现统一 scope 与 DTO。**

`Active(db)` 返回 `db.Where("is_deleted = ?", 0)`；DTO 仅导出 `guid` 字符串和语义字段，绝不导出内部 ID、user ID 或 GORM 审计字段。

- [ ] **Step 4: 运行模型测试。**

Run: `go test ./internal/models ./internal/dto -count=1`

Expected: PASS。

- [ ] **Step 5: 提交。**

```bash
git add internal/models internal/dto
git commit -m "refactor: standardize persistent models"
```

### Task 3: 重构认证、Token、会话、计费和审计写路径（后端原子里程碑内部任务）

**Files:**
- Create: `internal/service/persistence.go`
- Create: `internal/service/persistence_test.go`
- Modify: `internal/service/auth.go`
- Modify: `internal/service/gateway_token.go`
- Modify: `internal/service/conversation.go`
- Modify: `internal/service/platform_chat.go`
- Modify: `internal/service/billing.go`
- Modify: `internal/service/audit.go`
- Modify: `internal/service/dashboard.go`
- Modify: `internal/service/analytics.go`
- Modify: `internal/service/gateway_token_test.go`
- Modify: `internal/service/platform_chat_test.go`
- Modify: `internal/service/analytics_test.go`

- [ ] **Step 1: 写失败测试，固定审计与逻辑删除。**

```go
func TestDeleteConversationSoftDeletesConversationAndMessages(t *testing.T) {
    err := service.DeleteConversation(ctx, db, user.ID, conversation.GUID)
    require.NoError(t, err)
    require.Equal(t, 1, storedConversation.IsDeleted)
    require.Equal(t, 1, storedMessage.IsDeleted)
}

func TestTokenAuthenticationRejectsDeletedTokenAndOwner(t *testing.T) {
    _, err := tokenService.Authenticate(secret, "", "model-a", now)
    require.ErrorIs(t, err, service.GatewayTokenInvalid)
}
```

- [ ] **Step 2: 增加统一写入 API。**

定义 `ActorID *int64`、`NewRecordMeta(actor, now, snowflake)`、`Touch(actor, now)` 和 `SoftDelete(actor, now)`。所有 create/update/delete Service 签名显式接收 actor，复合写（聊天+消息+用量、支付+用户套餐、会话+消息删除）使用 `db.Transaction`。

- [ ] **Step 3: 将所有查询改为 GUID 入口和 Active scope。**

公开 Service 方法以 `(userID int64, guid int64)` 查询资源；内部关联仍用主键。`Preload("Messages", "is_deleted = 0")`、分析/仪表盘/账务查询均使用活动 scope。手机号仅用于登录查找；所有 ownership 判断只使用用户内部 ID。

- [ ] **Step 4: 收口无 RAG 的聊天持久化。**

确保 Task 1 前移删除 RAG 后，聊天/比较的持久化只写会话、消息与用量；`ChatParams` 仅保留模型、消息、会话 GUID、context window 和上游允许参数。不得重新引入 `Dataset`、`dataset_*` 字段或 RAG 归因。

- [ ] **Step 5: 运行服务测试。**

Run: `go test ./internal/service -count=1`

Expected: PASS，且测试不使用 `sqlite://`。

- [ ] **Step 6: 提交。**

```bash
git add internal/service
git commit -m "refactor: enforce guid audit and soft deletion"
```

### Task 4: 完成 RAG HTTP 清理并切换后端 HTTP 契约到 GUID（后端原子里程碑内部任务）

**Files:**
- Modify: `cmd/server/main.go`
- Modify: `internal/app/state.go`
- Modify: `internal/router/router.go`
- Modify: `internal/handler/conversations_datasets.go`
- Modify: `internal/handler/platform.go`
- Modify: `internal/handler/auth_users.go`
- Modify: `internal/handler/billing.go`
- Modify: `internal/handler/gateway_tokens.go`
- Modify: `internal/handler/admin.go`
- Modify: `internal/handler/analytics.go`
- Modify: `internal/handler/health.go`
- Modify: `internal/router/router_test.go`
- Modify: `internal/handler/gateway_tokens.go`
- Modify: `internal/handler/gateway_whitelabel_test.go`
- Modify: `internal/handler/platform_whitelabel_test.go`
- Modify: `internal/handler/analytics_test.go`

- [ ] **Step 1: 写失败 HTTP 契约测试。**

```go
func TestConversationRoutesUseGUIDAndHideInternalID(t *testing.T) {
    rec := performJSON(t, router, "GET", "/api/v1/conversations/"+guidString, jwt)
    require.Equal(t, http.StatusOK, rec.Code)
    require.JSONEq(t, `{"guid":"`+guidString+`"}`, selectJSON(rec.Body.Bytes(), "guid"))
    require.NotContains(t, rec.Body.String(), `"id"`)
}

func TestDatasetRoutesAndFieldsAreRemoved(t *testing.T) {
    require.Equal(t, http.StatusNotFound, perform(router, "GET", "/api/v1/datasets").Code)
    rec := performJSON(t, router, "POST", "/api/v1/platform/chat/completions", jwt, `{"model":"model-a","messages":[],"dataset_enabled":true}`)
    require.Equal(t, http.StatusBadRequest, rec.Code)
}
```

- [ ] **Step 2: 验证已前移删除的 RAG HTTP 边界。**

验证 Task 1 已删除 `RegisterDatasets`、`RegisterAdminDatasets` 及路由注册，并已删除 datasets 请求/响应字段和 `CHROMA_*` / `DATASET_*` 初始化。严格 decoder 必须将 `dataset_enabled`、`dataset_ids` 视为未知/不支持参数；本任务不重新实现或保留它们。

- [ ] **Step 3: 切换所有路径和 DTO。**

所有 `/:id` 路由更名为 `/:guid`；解析十进制 int64 GUID，服务层以活动 GUID 查找。Token CRUD、Conversation、Order、Admin 用户资源均适用。保持 JWT 内部 claim 不变；错误响应不得披露内部 ID。

- [ ] **Step 4: 运行路由与全量后端测试。**

Run: `go test ./internal/handler ./internal/router -count=1 && go test ./... -count=1 && go vet ./...`

Expected: PASS。

- [ ] **Step 5: 提交。**

```bash
git add cmd internal
git commit -m "refactor: remove rag and expose resource guids"
```

### Task 5: 后端原子里程碑的 MySQL 8 集成测试与运行文档门禁

**Files:**
- Create: `scripts/test-mysql-migrations.sh`
- Create: `internal/migration/mysql_integration_test.go`
- Modify: `Dockerfile`
- Modify: `.env.example`
- Modify: `README.md`
- Modify: `docs/agents/domain.md`
- Modify: `CONTEXT-MAP.md`
- Modify: `deploy/production-deploy.sh`
- Modify: `deploy/test-production-deploy.sh`

- [ ] **Step 1: 写失败 migration smoke 测试。**

```go
func TestInitialMigrationCreatesOnlyConformingTables(t *testing.T) {
    db := openFreshMySQL(t)
    require.NoError(t, migration.Up(ctx, db, generator, clock))
    assertColumnType(t, db, "users", "created_at", "bigint")
    assertNoColumnType(t, db, "users", "timestamp")
    assertTableMissing(t, db, "datasets")
}
```

- [ ] **Step 2: 实现可重复 MySQL 8 测试入口。**

`scripts/test-mysql-migrations.sh` 仅创建带随机名称的临时测试 database、运行 migration 与 Go 集成测试，退出时只删除该随机测试 database；禁止操作部署数据库、容器卷或任意非测试 database。通过 `TEST_DATABASE_URL` 显式连接 MySQL 8。

- [ ] **Step 3: 更新运行与部署资料。**

`.env.example` 删除 SQLite、Chroma 和 dataset 变量，加入 MySQL URL、`SNOWFLAKE_NODE_ID`。README 说明先执行 `go run ./cmd/migrate up`，再启动服务；Docker/部署脚本不创建 RAG 目录、不执行迁移、不清理数据库。

- [ ] **Step 4: 验证。**

Run: `bash scripts/test-mysql-migrations.sh && go test ./... -count=1 && go vet ./... && git diff --check`

Expected: PASS。

- [ ] **Step 5: 提交。**

```bash
git add scripts Dockerfile .env.example README.md CONTEXT-MAP.md docs deploy
git commit -m "docs: document mysql-only deployment"
```

### Task 6: Porsche-Web 同步移除 RAG 并使用 GUID 字符串

**Files (Porsche-Web repository):**
- Modify: `src/api/platform.js`
- Modify: `src/api/conversations.js`
- Modify: `src/api/chat.js`
- Modify: `src/api/mock.js`
- Modify: `src/stores/chat.js`
- Modify: `src/stores/user.js`
- Modify: `src/utils/platform-mappers.js`
- Modify: `src/utils/sse.js`
- Modify: `src/router/index.js`
- Modify: affected views/components and `README.md`
- Create: `src/utils/resource-guid.test.js`
- Modify: existing Node test files

- [ ] **Step 1: 写失败前端契约测试。**

```js
test('normalizes resource GUIDs as strings and never preserves internal ids', () => {
  assert.deepEqual(normalizeConversation({ guid: '922337203685477000', id: 1 }), {
    guid: '922337203685477000'
  })
})

test('chat request contains no RAG fields', () => {
  const body = buildPlatformChatRequest({ model: 'model-a', messages: [] })
  assert.equal('dataset_enabled' in body, false)
  assert.equal('dataset_ids' in body, false)
})
```

- [ ] **Step 2: 更新 API、store、SSE 与界面。**

删除 dataset state、字段、mock、路由与文案；所有会话/Token/订单资源使用 GUID string。聊天和 Compare 继续处理 chunk/model_done/model_error/[DONE]，不得恢复知识库字段或内部 ID。

- [ ] **Step 3: 运行前端验证。**

Run: `npm test && npm run build`

Expected: PASS；只允许已记录的构建告警。

- [ ] **Step 4: 提交 Porsche-Web。**

```bash
git add src README.md
git commit -m "refactor: remove rag and use resource guids"
```

### Task 7: 项目经理终验与发布准备

**Files:**
- Modify: `feature_list.json`
- Modify: `progress.md`
- Modify: `session-handoff.md`
- Modify: `docs/agents/domain.md`（如发现与最终结构不一致）

- [ ] **Step 1: 执行后端与数据库门禁。**

Run: `go test ./... -count=1 && go vet ./... && bash scripts/test-mysql-migrations.sh && git diff --check`

Expected: PASS；记录 MySQL 版本、migration version、测试 database 名称（不记录 URL/密码）。

- [ ] **Step 2: 执行前端门禁。**

Run: `npm test && npm run build && git diff --check`

Expected: PASS。

- [ ] **Step 3: 安全审计。**

审查：无 `AutoMigrate`、无 SQLite URL、无 RAG 包/字段/目录、无对外内部 ID、所有活动 scope 与逻辑删除覆盖认证/授权/统计、MySQL migration 不会删除非测试数据、没有密钥进入仓库。

- [ ] **Step 4: 更新交接记录并提交。**

```bash
git add feature_list.json progress.md session-handoff.md docs/agents/domain.md
git commit -m "docs: record persistence refactor verification"
```

- [ ] **Step 5: 由项目经理交付。**

仅在安全审查无 High/Critical、全部测试通过、MySQL migration 冒烟成功且前后端契约一致时，报告可合并/部署。
