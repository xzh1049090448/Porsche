# Fixed Home and Structured Public Content Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让固定公共首页在内容发布不可用时仍正常展示，并由 Root 通过结构化公告、FAQ 和精选模型安全增强首页。

**Architecture:** 前端同步渲染固定 Vue/i18n 首页，异步读取只含已发布结构化内容的 `home-config`。后端在现有内容聚合 revision 和不可变发布快照上增加公告、FAQ 草稿表与精选模型列表，同时保留 legacy 公共文档接口供滚动发布兼容。

**Tech Stack:** Go 1.22、Gin、GORM、MySQL 8、Vue 3、Pinia、Vite 6、Node test runner、JSDOM、DOMPurify、marked。

---

## 执行边界

- 后端工作树：`/Users/xuzhihao/code/Porsche/.worktrees/fixed-home-structured-content`
- 前端工作树：`/Users/xuzhihao/code/Porsche-Web/.worktrees/fixed-home-structured-content`
- 后端基线：`6ce8e48657cd8bc6a86b286503fde3d0d670749f`
- 前端基线：`f2b8c3e150b5629e25aa1f0b0fb8f02dd17d3771`
- 设计规范：`docs/superpowers/specs/2026-09-15-fixed-home-structured-public-content-design.md`
- 已批准首页视觉源：`/Users/xuzhihao/Doubao/chats/2026-09-01/new-chat-1/landing-prototype.html`；价格页视觉源：同目录 `pricing.html`。只复用布局、视觉和模块切换行为，不复制原型中的未批准事实性文案或模拟数据。
- 风险等级：完整流程。原因是新增 MySQL 迁移、跨仓库 API、Root 管理写操作、公开读取、富文本和发布状态。
- Controller：后端 `project_manager`，前端 `front_end_project_coordinator`。两个 Controller 只协调和汇总，不写实现。
- 实施期间不推送、不合并、不迁移生产数据库、不部署、不发布生产内容；这些动作必须在本地联合验收结束后单独处理。

## 文件结构

### 后端新增

- `internal/migration/sql/0020_public_home_structured_content.up.sql`：创建公告和 FAQ 草稿表。
- `internal/migration/sql/0020_public_home_structured_content.down.sql`：只回滚 0020 新表。
- `internal/migration/public_home_structured_content.go`：0020 结构验证器。
- `internal/migration/public_home_structured_content_test.go`：静态、真实 MySQL 和账本测试。
- `internal/service/public_home_content.go`：结构化草稿写入、文档草稿投影、公开投影和规范化排序。
- `internal/service/public_home_content_test.go`：纯逻辑和事务行为测试。
- `internal/dto/public_home_content.go`：严格请求解码和公开 DTO。
- `internal/dto/public_home_content_test.go`：重复键、未知字段、边界和 JSON 投影测试。
- `internal/handler/public_home_content_admin.go`：Root 草稿、预览和历史 home-config 路由。
- `internal/handler/public_home_content_admin_test.go`：HTTP、权限、缓存头和错误信封测试。

### 后端修改

- `internal/migration/runner.go`：注册 0020 并在启动验证中检查结构。
- `internal/models/public_content_pricing.go`：增加公告和 FAQ GORM 模型。
- `internal/models/public_content_pricing_test.go`：验证 guid、审计、枚举、软删除和索引元数据。
- `internal/service/public_content.go`：将结构化草稿加入校验、发布、恢复和内容哈希。
- `internal/service/public_catalog_read.go`：从同代发布快照投影 `home-config`。
- `internal/service/public_render_job.go`：渲染固定首页与可选动态数据，并保留上一有效产物。
- `internal/handler/public_content.go`：注册匿名 `/api/v1/public/home-config`。
- `internal/handler/public_content_admin.go`：注册 documents-draft、结构化子路由并保持 legacy 路由。
- `internal/router/router.go`、`internal/router/router_test.go`：路由装配与 Root/匿名矩阵。
- `docs/agents/contracts/public-content-pricing-v1.json`：升级为 v2 合同定义。
- `feature_list.json`、`progress.md`：只在所有本地门禁结束后记录实际状态和证据。

### 前端新增

- `src/api/publicHomeContentAdmin.js`、`src/api/publicHomeContentAdmin.test.js`：严格 Root 结构化内容客户端。
- `src/stores/publicHomeContent.js`、`src/stores/publicHomeContent.test.js`：公共动态内容的版本绑定、取消和失败隐藏。
- `src/components/public/HomeDynamicContent.vue`：公告、FAQ 和精选模型可选区块。
- `src/components/public-admin/AnnouncementEditor.vue`：公告表单与列表。
- `src/components/public-admin/FaqEditor.vue`：FAQ 表单与列表。
- `src/components/public-admin/FeaturedModelSelector.vue`：模型搜索、选择和排序。
- `src/components/public-admin/public-home-editors.mounted.test.js`：三类编辑器的挂载行为。

### 前端修改

- `interface-contract.json`：嵌入与后端逐字段一致的 v2 公共内容合同。
- `src/api/publicContent.js`、`src/api/publicContent.test.js`：匿名 home-config 映射。
- `src/views/public/Home.vue`：固定主体先渲染，动态区块后增强。
- `src/layouts/PublicLayout.vue`、`src/components/public/PublicHeader.vue`、`src/components/public/PublicFooter.vue`：固定导航，不再依赖 home Markdown。
- `src/views/PublicContentAdmin.vue`、`src/views/public-content-admin.mounted.test.js`：结构化首页与文档管理组合。
- `src/views/PublicContentPreview.vue`、`src/router/public-content-preview.contract.test.js`：固定首页草稿预览。
- `src/i18n/public-messages.js`、`src/i18n/messages.js`：审慎固定文案和管理表单文本。
- `src/styles/public-content.scss`：动态区块和编辑器响应式样式。
- `src/views/public/public-pages.contract.test.js`、`src/stores/publicHomePublication.test.js`：固定首页的无发布和代际回归。
- `src/api/public-content-pricing-contract.test.js`：v2 合同一致性。
- `feature_list.json`、`progress.md`：只记录真实执行结果。

### 外置审查证据

- `/private/tmp/porsche-fixed-home-structured-content-review/backend/`
- `/private/tmp/porsche-fixed-home-structured-content-review/frontend/`

scope、baseline 和 snapshot 必须由仓库 `docs/agents/review_snapshot.py` 生成并保存到以上目录，不得放回工作树。

## Task 1：冻结跨仓库 v2 合同

**Files:**
- Modify: `Porsche/docs/agents/contracts/public-content-pricing-v1.json`
- Modify: `Porsche/internal/router/router_test.go`
- Modify: `Porsche-Web/interface-contract.json`
- Modify: `Porsche-Web/src/api/public-content-pricing-contract.test.js`

- [ ] **Step 1：写合同失败测试**

在前端合同测试中将公共内容路由数量改为 48，并精确断言新增路由：

```js
assert.equal(imported.version, 'v2')
assert.equal(imported.status, 'approved_contract_pending_implementation')
assert.equal(imported.routes.length, 48)
for (const [method, path] of [
  ['GET', '/api/v1/public/home-config'],
  ['GET', '/admin/v2/public-content/home-draft'],
  ['POST', '/admin/v2/public-content/home-draft/announcements'],
  ['PATCH', '/admin/v2/public-content/home-draft/announcements/{guid}'],
  ['DELETE', '/admin/v2/public-content/home-draft/announcements/{guid}'],
  ['POST', '/admin/v2/public-content/home-draft/faqs'],
  ['PATCH', '/admin/v2/public-content/home-draft/faqs/{guid}'],
  ['DELETE', '/admin/v2/public-content/home-draft/faqs/{guid}'],
  ['PUT', '/admin/v2/public-content/home-draft/featured-models'],
  ['GET', '/admin/v2/public-content/home-preview'],
  ['GET', '/admin/v2/public-content/releases/{guid}/home-config'],
  ['GET', '/admin/v2/public-content/documents-draft'],
  ['PUT', '/admin/v2/public-content/documents-draft'],
]) assert.ok(imported.routes.some(route => route.method === method && route.path === path), `${method} ${path}`)
```

- [ ] **Step 2：运行合同测试并确认 RED**

Run in Porsche-Web:

```bash
PUBLIC_PRICING_BACKEND_CONTRACT=/Users/xuzhihao/code/Porsche/.worktrees/fixed-home-structured-content/docs/agents/contracts/public-content-pricing-v1.json node --test src/api/public-content-pricing-contract.test.js
```

Expected: FAIL，报告 `v1 !== v2` 或路由数量 `35 !== 48`。

- [ ] **Step 3：写完整 v2 合同**

后端合同新增 `AnnouncementDraft`、`FAQDraft`、`HomeDraftResponse`、`DocumentsDraftResponse`、各写请求、`HomeConfigPublicResponse` 和 13 条路由。字段必须采用以下核心形状：

```json
{
  "AnnouncementDraft": {
    "required": ["guid", "title", "body_markdown", "effective_at", "is_visible", "sort_order"],
    "properties": {
      "guid": { "type": "string", "format": "positive-int64" },
      "title": { "type": "string", "minLength": 1, "maxLength": 120 },
      "body_markdown": { "type": "string", "maxBytes": 16384 },
      "effective_at": { "one_of": ["null", "rfc3339_utc"] },
      "is_visible": { "type": "boolean" },
      "sort_order": { "type": "integer", "minimum": 0, "maximum": 1000000 }
    }
  },
  "HomeConfigPublicResponse": {
    "required": ["announcements", "faqs", "featured_model_keys", "content_release_version", "price_release_version"]
  }
}
```

把后端合同对象完整复制到 `interface-contract.json.public_content_pricing`，不要手工维护两个不同版本。保持既有认证/SSE 哈希断言不变。

- [ ] **Step 4：运行双端合同测试**

Run:

```bash
cd /Users/xuzhihao/code/Porsche/.worktrees/fixed-home-structured-content && go test ./internal/router -run 'Test.*Public.*Contract' -count=1
cd /Users/xuzhihao/code/Porsche-Web/.worktrees/fixed-home-structured-content && PUBLIC_PRICING_BACKEND_CONTRACT=/Users/xuzhihao/code/Porsche/.worktrees/fixed-home-structured-content/docs/agents/contracts/public-content-pricing-v1.json node --test src/api/public-content-pricing-contract.test.js
```

Expected: 两条命令 PASS，前端嵌入对象与后端合同逐字段相等。

- [ ] **Step 5：提交合同**

```bash
git -C /Users/xuzhihao/code/Porsche/.worktrees/fixed-home-structured-content add docs/agents/contracts/public-content-pricing-v1.json internal/router/router_test.go
git -C /Users/xuzhihao/code/Porsche/.worktrees/fixed-home-structured-content commit -m "docs: freeze structured home content contract"
git -C /Users/xuzhihao/code/Porsche-Web/.worktrees/fixed-home-structured-content add interface-contract.json src/api/public-content-pricing-contract.test.js
git -C /Users/xuzhihao/code/Porsche-Web/.worktrees/fixed-home-structured-content commit -m "docs: import structured home content contract"
```

## Task 2：增加 0020 数据库结构与模型

**Files:**
- Create: `Porsche/internal/migration/sql/0020_public_home_structured_content.up.sql`
- Create: `Porsche/internal/migration/sql/0020_public_home_structured_content.down.sql`
- Create: `Porsche/internal/migration/public_home_structured_content.go`
- Create: `Porsche/internal/migration/public_home_structured_content_test.go`
- Modify: `Porsche/internal/migration/runner.go`
- Modify: `Porsche/internal/models/public_content_pricing.go`
- Modify: `Porsche/internal/models/public_content_pricing_test.go`

- [ ] **Step 1：写模型和迁移 RED 测试**

```go
func TestPublicHomeStructuredModelsFollowDatabaseStandards(t *testing.T) {
    for _, value := range []struct{ model any; table string }{
        {PublicHomeAnnouncement{}, "public_home_announcements"},
        {PublicHomeFAQ{}, "public_home_faqs"},
    } {
        parsed, err := schema.Parse(value.model, &sync.Map{}, schema.NamingStrategy{})
        if err != nil { t.Fatal(err) }
        if parsed.Table != value.table { t.Fatalf("table=%s", parsed.Table) }
        for _, field := range []string{"ID", "Guid", "CreatedAt", "CreatedBy", "UpdatedAt", "UpdatedBy", "IsDeleted", "Revision"} {
            if parsed.LookUpField(field) == nil { t.Fatalf("%s missing %s", value.table, field) }
        }
    }
}
```

迁移测试必须断言 `All()` 长度为 20、末项为 `0020`，并在真实 MySQL 中验证 up、重复 up、down、重新 up、账本 checksum 和部分结构拒绝。

- [ ] **Step 2：运行测试并确认 RED**

```bash
go test ./internal/models ./internal/migration -run 'TestPublicHomeStructured|TestAllIncludesPublicHome' -count=1
```

Expected: FAIL，类型或迁移 0020 尚不存在。

- [ ] **Step 3：实现模型和 SQL**

模型使用显式 GORM 元数据：

```go
type PublicHomeAnnouncement struct {
    ID int64 `gorm:"column:id;type:bigint;not null;primaryKey;autoIncrement" json:"-"`
    AuditFields `gorm:"embedded" json:"-"`
    ContentDraftID int64 `gorm:"column:content_draft_id;type:bigint;not null" json:"-"`
    Title string `gorm:"column:title;type:varchar(120);not null" json:"-"`
    BodyMarkdown string `gorm:"column:body_markdown;type:mediumtext;not null" json:"-"`
    EffectiveAt *int64 `gorm:"column:effective_at;type:bigint" json:"-"`
    IsVisible int `gorm:"column:is_visible;type:int;not null" json:"-"`
    SortOrder int `gorm:"column:sort_order;type:int;not null" json:"-"`
    Revision int64 `gorm:"column:revision;type:bigint;not null" json:"-"`
}
func (PublicHomeAnnouncement) TableName() string { return "public_home_announcements" }
```

`PublicHomeFAQ` 使用 `question varchar(200)`、`answer_markdown mediumtext`、`is_visible`、`sort_order` 和相同审计字段。SQL 为两表建立 guid 唯一索引、`content_draft_id/is_deleted/sort_order` 查询索引和到 `public_content_drafts(id)` 的外键。down 只按 FAQ、公告顺序删除本迁移创建的表。

在 `runner.go` 注册：

```go
{Version: "0020", UpSQL: publicHomeStructuredContentUp, DownSQL: publicHomeStructuredContentDown},
```

- [ ] **Step 4：运行模型、迁移与全量迁移测试**

```bash
go test ./internal/models ./internal/migration -count=1
```

Expected: PASS；无真实 MySQL 环境的显式 opt-in 测试可 SKIP，但静态结构和 ledger 测试必须 PASS。

- [ ] **Step 5：提交迁移**

```bash
git add internal/models/public_content_pricing.go internal/models/public_content_pricing_test.go internal/migration/runner.go internal/migration/public_home_structured_content.go internal/migration/public_home_structured_content_test.go internal/migration/sql/0020_public_home_structured_content.up.sql internal/migration/sql/0020_public_home_structured_content.down.sql
git commit -m "feat: add structured public home schema"
```

## Task 3：实现严格 DTO 与规范化规则

**Files:**
- Create: `Porsche/internal/dto/public_home_content.go`
- Create: `Porsche/internal/dto/public_home_content_test.go`
- Create: `Porsche/internal/service/public_home_content.go`
- Create: `Porsche/internal/service/public_home_content_test.go`

- [ ] **Step 1：写 DTO 边界 RED 测试**

覆盖 1/120/121 字标题、1/200/201 字问题、16 KiB/16 KiB+1 正文、活动数量 20/21、FAQ 50/51、精选模型 12/13、重复键、未知字段、控制字符、非 UTC 时间和 `sort_order` 边界。

```go
func TestDecodeAnnouncementCreateRejectsUnknownDuplicateAndOversize(t *testing.T) {
    valid := `{"expected_revision":1,"title":"维护","body_markdown":"正文","effective_at":null,"is_visible":true,"sort_order":10}`
    if _, err := DecodeAnnouncementCreateRequest(strings.NewReader(valid)); err != nil { t.Fatal(err) }
    for _, raw := range []string{
        strings.Replace(valid, `"title":"维护"`, `"title":""`, 1),
        strings.Replace(valid, `"sort_order":10`, `"sort_order":1000001`, 1),
        valid[:len(valid)-1] + `,"unknown":1}`,
    } {
        if _, err := DecodeAnnouncementCreateRequest(strings.NewReader(raw)); err == nil { t.Fatalf("accepted %s", raw) }
    }
}
```

- [ ] **Step 2：运行 DTO 测试并确认 RED**

```bash
go test ./internal/dto -run 'TestDecodeAnnouncement|TestDecodeFAQ|TestDecodeFeatured|TestDocumentsDraft' -count=1
```

Expected: FAIL，解码函数不存在。

- [ ] **Step 3：实现 DTO 和规范化函数**

```go
const (
    PublicHomeAnnouncementLimit = 20
    PublicHomeFAQLimit = 50
    PublicHomeFeaturedModelLimit = 12
    PublicHomeMarkdownLimit = 16 << 10
    PublicHomeSortOrderMaximum = 1_000_000
)

type PublicHomeDraft struct {
    Revision int64 `json:"revision"`
    Announcements []PublicHomeAnnouncementDraft `json:"announcements"`
    FAQs []PublicHomeFAQDraft `json:"faqs"`
    FeaturedModelKeys []string `json:"featured_model_keys"`
}
```

使用现有 `ValidateNoDuplicateJSON`、`DisallowUnknownFields` 和请求体限制。所有业务 GUID 在 DTO 中是十进制字符串；`effective_at` 只接受规范 RFC3339 UTC，解析后转为 Unix 毫秒。排序使用 `sort_order` 再使用数字 GUID，响应数组永不为 `null`。

- [ ] **Step 4：运行 DTO 和 service 纯逻辑测试**

```bash
go test ./internal/dto ./internal/service -run 'Test.*PublicHome|Test.*DocumentsDraft' -count=1
```

Expected: PASS。

- [ ] **Step 5：提交 DTO**

```bash
git add internal/dto/public_home_content.go internal/dto/public_home_content_test.go internal/service/public_home_content.go internal/service/public_home_content_test.go
git commit -m "feat: define structured home content DTOs"
```

## Task 4：实现 Root 草稿事务操作

**Files:**
- Modify: `Porsche/internal/service/public_home_content.go`
- Modify: `Porsche/internal/service/public_home_content_test.go`
- Modify: `Porsche/internal/service/public_content_db_test.go`

- [ ] **Step 1：写真实事务 RED 测试**

```go
func TestPublicHomeDraftMutationsShareAggregateRevision(t *testing.T) {
    if os.Getenv("TEST_DATABASE_URL") == "" {
        t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
    }
    f := openPublicModelDBFixture(t)
    cleanPublicContentDBFixture(t, f.db)
    tx := f.db.Begin()
    if tx.Error != nil { t.Fatal(tx.Error) }
    t.Cleanup(func() { _ = tx.Rollback().Error })
    content := NewPublicContentService(tx)
    draft, err := content.GetDraft(context.Background(), f.actor.ID)
    if err != nil { t.Fatal(err) }
    created, err := content.CreateAnnouncement(context.Background(), f.actor.ID, AnnouncementCreateRequest{
        ExpectedRevision: draft.Revision, Title: "维护", BodyMarkdown: "正文", IsVisible: true, SortOrder: 10,
    })
    if err != nil { t.Fatal(err) }
    if created.Revision != draft.Revision+1 || len(created.Announcements) != 1 { t.Fatalf("draft=%+v", created) }
    if _, err = content.SaveDocumentsDraft(context.Background(), f.actor.ID, DocumentsDraftSaveRequest{
        ExpectedRevision: draft.Revision, About: "a", Terms: "t", Privacy: "p", LegalReviewed: true,
    }); status(err) != 409 { t.Fatalf("stale status=%d err=%v", status(err), err) }
}
```

另外覆盖更新、隐藏、排序、FAQ、精选模型原子替换、软删除、删除后无管理读取、actor 审计、RowsAffected=0 冲突和事务回滚。

- [ ] **Step 2：运行测试并确认 RED**

```bash
go test ./internal/service -run 'TestPublicHomeDraftMutations|TestDocumentsDraft|TestPublicHomeSoftDelete' -count=1
```

Expected: FAIL，事务方法尚未实现。

- [ ] **Step 3：实现共享 revision 事务**

每个方法调用现有 Root 锁与内容草稿锁；关键更新模式固定为：

```go
result := tx.Model(&models.PublicContentDraft{}).
    Where("id=? AND revision=? AND is_deleted=0", draft.ID, in.ExpectedRevision).
    Updates(map[string]any{"revision": draft.Revision + 1, "updated_at": now, "updated_by": actor.ID})
if result.Error != nil { return errUnavailable("public content draft persistence unavailable") }
if result.RowsAffected != 1 { return errConflict("public content draft revision conflict") }
```

结构化行写入、软删除或精选列表 payload 更新必须和该 CAS 在同一事务中。legacy `SaveDraft` 改为保留 payload 中不属于 `home/about/terms/privacy/legal_reviewed` 的键；`SaveDocumentsDraft` 不接收 legacy `home`。

- [ ] **Step 4：运行 service 全包测试**

```bash
go test ./internal/service -count=1
```

Expected: PASS；旧内容与价格发布测试保持通过。

- [ ] **Step 5：提交事务服务**

```bash
git add internal/service/public_home_content.go internal/service/public_home_content_test.go internal/service/public_content.go internal/service/public_content_db_test.go
git commit -m "feat: manage structured public home drafts"
```

## Task 5：把结构化内容加入校验、发布和恢复

**Files:**
- Modify: `Porsche/internal/service/public_content.go`
- Modify: `Porsche/internal/service/public_content_test.go`
- Modify: `Porsche/internal/service/public_content_db_test.go`
- Modify: `Porsche/internal/service/public_price_snapshot.go`

- [ ] **Step 1：写发布与恢复 RED 测试**

```go
func TestPreparePublicContentBindsStructuredHomeToPriceSnapshot(t *testing.T) {
    draft := PublicContentDraft{Revision: 4, Home: "legacy", About: "about", Terms: legalTerms, Privacy: legalPrivacy, LegalReviewed: true}
    home := PublicHomeDraft{Revision: 4, FeaturedModelKeys: []string{"deepseek-chat"}}
    prepared, issues := preparePublicContent(draft, home, priceSnapshot(), []models.PublicPriceSnapshotItem{pricedItem("deepseek-chat")})
    if len(issues) != 0 { t.Fatalf("issues=%+v", issues) }
    config := prepared.Payload["home_config"].(models.JSONMap)
    if !reflect.DeepEqual(config["featured_model_keys"], []string{"deepseek-chat"}) { t.Fatalf("config=%+v", config) }
    if prepared.Payload["home"] == nil { t.Fatal("legacy sanitized home missing") }
}
```

覆盖不存在、失效、软删除、缺输入价、缺输出价、重复精选模型；危险 Markdown/URL；恢复不复活已删除公告/FAQ；价格安全快照重绑后精选模型保持同代。

- [ ] **Step 2：运行测试并确认 RED**

```bash
go test ./internal/service -run 'TestPreparePublicContent|TestStructuredHomePublish|TestStructuredHomeRestore|TestPrice.*Rebind' -count=1
```

Expected: FAIL，`preparePublicContent` 尚未接收结构化草稿。

- [ ] **Step 3：实现规范化发布载荷**

构造以下服务端内部结构，再统一计算内容哈希：

```go
type publishedHomeConfig struct {
    Announcements []publishedAnnouncement `json:"announcements"`
    FAQs []publishedFAQ `json:"faqs"`
    FeaturedModelKeys []string `json:"featured_model_keys"`
}

payload := models.JSONMap{
    "schema_version": int64(2),
    "home_config": published,
    "home": documents["home"],
    "about": documents["about"],
    "terms": documents["terms"],
    "privacy": documents["privacy"],
    "legal_reviewed": true,
    "price_snapshot_guid": strconv.FormatInt(price.Guid, 10),
    "price_snapshot_version": price.Version,
}
```

正文使用现有服务端 sanitizer，发布载荷只保留净化 HTML。恢复读取目标 home-config 后过滤当前 tombstone GUID，再校验当前价格快照并创建新 release。

- [ ] **Step 4：运行 service 与 publiccontent 测试**

```bash
go test ./internal/publiccontent ./internal/service -count=1
```

Expected: PASS。

- [ ] **Step 5：提交发布逻辑**

```bash
git add internal/service/public_content.go internal/service/public_content_test.go internal/service/public_content_db_test.go internal/service/public_price_snapshot.go
git commit -m "feat: publish structured home content snapshots"
```

## Task 6：实现 Root 和匿名 HTTP 路由

**Files:**
- Create: `Porsche/internal/handler/public_home_content_admin.go`
- Create: `Porsche/internal/handler/public_home_content_admin_test.go`
- Modify: `Porsche/internal/handler/public_content_admin.go`
- Modify: `Porsche/internal/handler/public_content.go`
- Modify: `Porsche/internal/handler/public_content_test.go`
- Modify: `Porsche/internal/router/router.go`
- Modify: `Porsche/internal/router/router_test.go`

- [ ] **Step 1：写 HTTP RED 测试**

```go
func TestPublicHomeConfigReturnsPublishedProjectionOnly(t *testing.T) {
    r := gin.New()
    registerPublicContentWithReader(r, publicReadStub{projection: testStructuredPublicProjection()}, func(*gin.Context) bool { return false })
    rec := httptest.NewRecorder()
    r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/public/home-config", nil))
    if rec.Code != http.StatusOK { t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String()) }
    if rec.Header().Get("Cache-Control") != "public, max-age=60, stale-while-revalidate=300" { t.Fatal(rec.Header()) }
    if bytes.Contains(rec.Body.Bytes(), []byte("body_markdown")) { t.Fatalf("draft leaked: %s", rec.Body.Bytes()) }
}
```

Root 路由测试覆盖 401、403、400、404、409、422、503、no-store、request ID、预览 noindex 和删除 revision 响应头。

- [ ] **Step 2：运行 handler/router 测试并确认 RED**

```bash
go test ./internal/handler ./internal/router -run 'Test.*PublicHome|Test.*DocumentsDraft|Test.*HomeConfig' -count=1
```

Expected: FAIL，路由未注册。

- [ ] **Step 3：实现路由和响应**

匿名响应 DTO：

```go
type PublicHomeConfigRead struct {
    Announcements []PublicAnnouncementRead `json:"announcements"`
    FAQs []PublicFAQRead `json:"faqs"`
    FeaturedModelKeys []string `json:"featured_model_keys"`
    ContentReleaseVersion int64 `json:"content_release_version"`
    PriceReleaseVersion int64 `json:"price_release_version"`
}
```

只返回已生效公告；ETag 使用 `content hash + price hash + effective subset hash`。Root 路由复用 `gatewayRequestID`、`publicAdminNoStore`、`RequireRootWithError`、严格 body 和统一错误信封。不得在错误中返回正文或数据库 ID。

- [ ] **Step 4：运行 handler/router 全包测试**

```bash
go test ./internal/handler ./internal/router -count=1
```

Expected: PASS。

- [ ] **Step 5：提交 HTTP 层**

```bash
git add internal/handler/public_home_content_admin.go internal/handler/public_home_content_admin_test.go internal/handler/public_content_admin.go internal/handler/public_content.go internal/handler/public_content_test.go internal/router/router.go internal/router/router_test.go
git commit -m "feat: expose structured public home APIs"
```

## Task 7：扩展公共投影与静态渲染

**Files:**
- Modify: `Porsche/internal/service/public_catalog_read.go`
- Modify: `Porsche/internal/service/public_catalog_read_test.go`
- Modify: `Porsche/internal/service/public_render_job.go`
- Modify: `Porsche/internal/service/public_render_job_test.go`

- [ ] **Step 1：写代际和渲染 RED 测试**

测试 `home-config` 的内容版本、价格版本、ETag、生效时间边界；结构化渲染失败后 production pointer 不变；未来公告到点后进入渲染队列；legacy `/home` 仍返回净化内容。

```go
func TestValidatePublishedHomeConfigRejectsFeaturedModelOutsideBoundSnapshot(t *testing.T) {
    home := publishedHomeConfig{FeaturedModelKeys: []string{"not-in-snapshot"}}
    items := []models.PublicPriceSnapshotItem{{ModelKey: "deepseek-chat"}}
    if err := validatePublishedHomeConfig(home, items); err == nil {
        t.Fatal("featured model outside bound snapshot accepted")
    }
}
```

同文件增加真实 MySQL 投影用例：基于现有 `openPublicModelDBFixture`、`seedContentPublicationFixture` 和事务回滚 fixture 写入具有正确重算 hash、但引用快照外 modelKey 的 release，断言 `Projection` 返回 `503`，从而证明失败原因不是 hash 漂移。

- [ ] **Step 2：运行测试并确认 RED**

```bash
go test ./internal/service -run 'TestProjection.*Featured|TestPublicRender.*Structured|Test.*EffectiveAnnouncement' -count=1
```

Expected: FAIL，投影尚未验证结构化绑定。

- [ ] **Step 3：实现投影和渲染**

`PublicCatalogProjection` 增加不可变 `HomeConfig`。加载 release 后先验证 schema version、内容 hash、价格绑定和全部精选模型，再生成公开子集。渲染任务写 staging 目录，完整验证后才原子切换；失败时保留旧目录并创建 `renderer_failure` Root 通知。

未来 `effective_at` 使用现有周期 worker 检查最早未生效时间并幂等入队，不因重复 tick 创建重复发布版本。

- [ ] **Step 4：运行后端全量门禁**

```bash
GOCACHE=/private/tmp/porsche-fixed-home-go-cache go test ./... -count=1
GOCACHE=/private/tmp/porsche-fixed-home-go-cache go test -race ./internal/service ./internal/handler ./internal/router -count=1
GOCACHE=/private/tmp/porsche-fixed-home-go-cache go vet ./...
GOCACHE=/private/tmp/porsche-fixed-home-go-cache go build ./...
git diff --check
```

Expected: 全部退出 0；数据库 opt-in 项单独记录，不把 SKIP 写成 PASS。

- [ ] **Step 5：提交投影和渲染**

```bash
git add internal/service/public_catalog_read.go internal/service/public_catalog_read_test.go internal/service/public_render_job.go internal/service/public_render_job_test.go
git commit -m "feat: render structured public home releases"
```

## Task 8：实现前端严格 API 和公共动态状态

**Files:**
- Create: `Porsche-Web/src/api/publicHomeContentAdmin.js`
- Create: `Porsche-Web/src/api/publicHomeContentAdmin.test.js`
- Create: `Porsche-Web/src/stores/publicHomeContent.js`
- Create: `Porsche-Web/src/stores/publicHomeContent.test.js`
- Modify: `Porsche-Web/src/api/publicContent.js`
- Modify: `Porsche-Web/src/api/publicContent.test.js`

- [ ] **Step 1：写 API/state RED 测试**

```js
test('public home config maps an exact same-generation response', async () => {
  const api = createPublicContentClient({ fetchImpl: async () => response(200, {
    announcements: [], faqs: [], featured_model_keys: ['deepseek-chat'],
    content_release_version: 2, price_release_version: 3,
  }, { 'ETag': '"abc"', 'X-Public-Release-Version': '2', 'Cache-Control': 'public, max-age=60, stale-while-revalidate=300' }) })
  const out = await api.getHomeConfig()
  assert.deepEqual(out.data.featuredModelKeys, ['deepseek-chat'])
  assert.deepEqual(out.publicationVersions, { content: 2, price: 3 })
})
```

同时测试未知字段、重复/非法 modelKey、草稿 Markdown 泄漏、混合版本、304 无缓存、取消、503 和迟到响应。

- [ ] **Step 2：运行测试并确认 RED**

```bash
node --test src/api/publicContent.test.js src/api/publicHomeContentAdmin.test.js src/stores/publicHomeContent.test.js
```

Expected: FAIL，客户端和 store 不存在。

- [ ] **Step 3：实现客户端与状态机**

```js
export function createPublicHomeContentState({ api }) {
  const value = shallowRef({ status: 'idle', data: null, error: null })
  let generation = 0
  let controller = null
  async function load() {
    const own = ++generation
    controller?.abort(); controller = new AbortController()
    value.value = { status: 'loading', data: null, error: null }
    try {
      const result = await api.getHomeConfig({ signal: controller.signal })
      if (own === generation) value.value = { status: 'ready', data: result.data, error: null }
    } catch (error) {
      if (own === generation && error?.name !== 'AbortError') value.value = { status: 'hidden', data: null, error: safePublicError(error) }
    }
    return value.value.data
  }
  function dispose() { generation++; controller?.abort(); controller = null; value.value = { status: 'idle', data: null, error: null } }
  return { value, load, dispose }
}
```

Root 客户端严格映射全部新路由，GUID 保持字符串，响应必须 `no-store` 且 request ID 存在；错误只暴露白名单 code 和脱敏 request ID。

- [ ] **Step 4：运行 focused 测试**

```bash
node --test src/api/publicContent.test.js src/api/publicHomeContentAdmin.test.js src/stores/publicHomeContent.test.js
```

Expected: PASS。

- [ ] **Step 5：提交 API/state**

```bash
git add src/api/publicContent.js src/api/publicContent.test.js src/api/publicHomeContentAdmin.js src/api/publicHomeContentAdmin.test.js src/stores/publicHomeContent.js src/stores/publicHomeContent.test.js
git commit -m "feat: add structured home content clients"
```

## Task 9：让固定首页脱离发布依赖

**Files:**
- Modify: `Porsche-Web/src/views/public/Home.vue`
- Modify: `Porsche-Web/src/layouts/PublicLayout.vue`
- Modify: `Porsche-Web/src/components/public/PublicHeader.vue`
- Modify: `Porsche-Web/src/components/public/PublicFooter.vue`
- Create: `Porsche-Web/src/components/public/HomeDynamicContent.vue`
- Modify: `Porsche-Web/src/stores/publicHomePublication.js`
- Modify: `Porsche-Web/src/stores/publicHomePublication.test.js`
- Modify: `Porsche-Web/src/views/public/public-pages.contract.test.js`
- Modify: `Porsche-Web/src/i18n/public-messages.js`
- Modify: `Porsche-Web/src/styles/public-content.scss`

- [ ] **Step 1：写首页无发布 RED 测试**

```js
test('fixed home remains usable when every publication request is unavailable', async () => {
  const wrapper = await mountPublicHome({ getHomeConfig: async () => { throw Object.assign(new Error('unavailable'), { code: 'unavailable' }) } })
  assert.equal(wrapper.get('#home-title').text(), '一个入口，连接已验证的模型能力')
  assert.equal(wrapper.get('a[href="/chat"]').exists(), true)
  assert.equal(wrapper.get('[data-section="advantages"]').exists(), true)
  assert.equal(wrapper.find('[data-section="announcements-faq"]').exists(), false)
  assert.equal(wrapper.find('[data-section="models"]').exists(), false)
})
```

合同测试禁止 `Home.vue` 和 `PublicLayout.vue` 导入 legacy `public-document` decoder、`getHome` 或使用发布内容生成导航。

- [ ] **Step 2：运行测试并确认 RED**

```bash
node --test src/stores/publicHomePublication.test.js src/views/public/public-pages.contract.test.js
```

Expected: FAIL，当前 Home 在发布失败时显示整页错误。

- [ ] **Step 3：实现固定主体和可选动态组件**

`Home.vue` 直接渲染 Hero、proof、advantages 和 CTA；逐模块对照已批准的 `/Users/xuzhihao/Doubao/chats/2026-09-01/new-chat-1/landing-prototype.html`，复用其桌面/移动布局、主题、间距和模块间切换样式。固定文案进入中英文 i18n，不出现未批准数字或授权声明。动态组件仅在有有效数据时渲染：

```vue
<HomeDynamicContent
  v-if="homeContent.status === 'ready' && homeContent.data"
  :announcements="homeContent.data.announcements"
  :faqs="homeContent.data.faqs"
  :featured-model-keys="homeContent.data.featuredModelKeys"
/>
```

`PublicLayout` 提供固定导航数组，挂载时启动 home-config 加载但不等待它。动态失败只保留一个视觉隐藏、屏幕阅读器可读的非打断状态；不得渲染“暂时无法显示内容”卡片。

- [ ] **Step 4：运行首页与响应式测试**

```bash
node --test src/stores/publicHomePublication.test.js src/views/public/public-pages.contract.test.js src/router/public-routes.test.js src/router/route-transition.contract.test.js
```

Expected: PASS。

- [ ] **Step 5：提交固定首页**

```bash
git add src/views/public/Home.vue src/layouts/PublicLayout.vue src/components/public/PublicHeader.vue src/components/public/PublicFooter.vue src/components/public/HomeDynamicContent.vue src/stores/publicHomePublication.js src/stores/publicHomePublication.test.js src/views/public/public-pages.contract.test.js src/i18n/public-messages.js src/styles/public-content.scss
git commit -m "feat: render fixed home without publication dependency"
```

## Task 10：实现 Root 结构化编辑器

**Files:**
- Create: `Porsche-Web/src/components/public-admin/AnnouncementEditor.vue`
- Create: `Porsche-Web/src/components/public-admin/FaqEditor.vue`
- Create: `Porsche-Web/src/components/public-admin/FeaturedModelSelector.vue`
- Create: `Porsche-Web/src/components/public-admin/public-home-editors.mounted.test.js`
- Modify: `Porsche-Web/src/views/PublicContentAdmin.vue`
- Modify: `Porsche-Web/src/views/public-content-admin.mounted.test.js`
- Modify: `Porsche-Web/src/i18n/messages.js`
- Modify: `Porsche-Web/src/styles/public-content.scss`

- [ ] **Step 1：写编辑器 RED 测试**

```js
test('home editor exposes structured controls and no legacy home markdown', async () => {
  const wrapper = await mountAdminWithHomeDraft({ revision: 4, announcements: [], faqs: [], featuredModelKeys: [] })
  assert.equal(wrapper.find('[data-editor="announcement"]').exists(), true)
  assert.equal(wrapper.find('[data-editor="faq"]').exists(), true)
  assert.equal(wrapper.find('[data-editor="featured-models"]').exists(), true)
  assert.equal(wrapper.find('#content-home').exists(), false)
})
```

覆盖 20/50/12 限制、键盘排序、确认软删除、无已删除筛选、显示开关、UTC 时间、搜索结果取消、Root 降级、迟到响应和 `409` 保留本地输入。

- [ ] **Step 2：运行挂载测试并确认 RED**

```bash
node --test src/components/public-admin/public-home-editors.mounted.test.js src/views/public-content-admin.mounted.test.js
```

Expected: FAIL，新组件不存在且旧首页 Markdown 仍显示。

- [ ] **Step 3：实现三个编辑器**

每次写操作发送当前聚合 revision，成功后用完整服务端响应替换状态。软删除必须使用显式确认对话框；不提供 deleted query。排序按钮具有可读标签并更新 `sort_order`，拖拽不能成为唯一操作方式。

`PublicContentAdmin` 的文档标签固定为：

```js
const documentNames = ['about', 'terms', 'privacy']
const homeSections = ['announcements', 'faqs', 'featuredModels']
```

任意草稿写入后清空旧 validation proof。`409` 时把未提交值复制到只存在内存的 conflict buffer，重新读取服务端版本并显示并排差异；不得把正文写入 localStorage/sessionStorage。

- [ ] **Step 4：运行管理页 focused 测试**

```bash
node --test src/api/publicHomeContentAdmin.test.js src/components/public-admin/public-home-editors.mounted.test.js src/views/public-content-admin.mounted.test.js
```

Expected: PASS。

- [ ] **Step 5：提交管理页**

```bash
git add src/components/public-admin/AnnouncementEditor.vue src/components/public-admin/FaqEditor.vue src/components/public-admin/FeaturedModelSelector.vue src/components/public-admin/public-home-editors.mounted.test.js src/views/PublicContentAdmin.vue src/views/public-content-admin.mounted.test.js src/i18n/messages.js src/styles/public-content.scss
git commit -m "feat: manage structured public home content"
```

## Task 11：更新预览、发布和历史恢复界面

**Files:**
- Modify: `Porsche-Web/src/views/PublicContentPreview.vue`
- Modify: `Porsche-Web/src/router/public-content-preview.contract.test.js`
- Modify: `Porsche-Web/src/views/PublicContentAdmin.vue`
- Modify: `Porsche-Web/src/views/public-content-admin.mounted.test.js`
- Modify: `Porsche-Web/src/api/publicContentAdmin.js`
- Modify: `Porsche-Web/src/api/publicContentAdmin.test.js`
- Modify: `Porsche-Web/src/utils/public-content-validation.js`
- Modify: `Porsche-Web/src/utils/public-content-validation.test.js`

- [ ] **Step 1：写预览和证明失效 RED 测试**

```js
test('preview renders fixed home with structured draft and stays noindex', async () => {
  const wrapper = await mountPreview({ home: { announcements: [], faqs: [], featuredModelKeys: [] }, documents: legalDocuments, revision: 5 })
  assert.equal(wrapper.get('#home-title').exists(), true)
  assert.equal(document.head.querySelector('meta[name="robots"]').content, 'noindex, nofollow, noarchive')
})

test('every structured mutation invalidates the publication proof', async () => {
  const admin = await mountedValidatedAdmin()
  await admin.createFAQ({ question: '问题', answerMarkdown: '答案' })
  assert.equal(admin.validationProof, null)
  assert.equal(admin.publishEnabled, false)
})
```

- [ ] **Step 2：运行测试并确认 RED**

```bash
node --test src/router/public-content-preview.contract.test.js src/views/public-content-admin.mounted.test.js src/api/publicContentAdmin.test.js src/utils/public-content-validation.test.js
```

Expected: FAIL，预览仍以 legacy home Markdown 为首页。

- [ ] **Step 3：实现固定首页预览与完整 canonical proof**

canonical proof 必须覆盖 documents draft、home draft、聚合 revision 和 price release GUID：

```js
export function canonicalStructuredContent({ revision, documents, home }) {
  return JSON.stringify({
    revision,
    documents: { about: documents.about, terms: documents.terms, privacy: documents.privacy, legal_reviewed: documents.legalReviewed },
    home: {
      announcements: [...home.announcements].sort(bySortOrderGuid),
      faqs: [...home.faqs].sort(bySortOrderGuid),
      featured_model_keys: [...home.featuredModelKeys],
    },
  })
}
```

预览并行读取 documents-draft、home-preview 和指定价格 release，三者 revision 或代际不一致时拒绝渲染。发布/恢复沿用现有密码、action ticket、幂等键和 reconciliation；不在浏览器保存密码或 ticket。

- [ ] **Step 4：运行管理发布全套测试**

```bash
node --test src/api/publicContentAdmin.test.js src/api/publicHomeContentAdmin.test.js src/utils/public-content-validation.test.js src/views/public-content-admin.mounted.test.js src/router/public-content-preview.contract.test.js
```

Expected: PASS。

- [ ] **Step 5：提交预览发布流程**

```bash
git add src/views/PublicContentPreview.vue src/router/public-content-preview.contract.test.js src/views/PublicContentAdmin.vue src/views/public-content-admin.mounted.test.js src/api/publicContentAdmin.js src/api/publicContentAdmin.test.js src/utils/public-content-validation.js src/utils/public-content-validation.test.js
git commit -m "feat: preview and publish structured home content"
```

## Task 12：执行前端全量与浏览器矩阵

**Files:**
- Modify: `Porsche-Web/src/views/public/public-pages.contract.test.js`
- Modify: `Porsche-Web/src/styles/typography.contract.test.js`
- Modify: `Porsche-Web/src/router/public-chunks.test.js`
- Modify: `Porsche-Web/feature_list.json`
- Modify: `Porsche-Web/progress.md`

- [ ] **Step 1：补充最终合同反例**

加入以下反例并确认每个都能使测试失败：Home 重新导入 `getHome`、固定导航读取发布内容、503 显示整页错误、动态响应含草稿 Markdown、全局 `zoom/scale`、390px 横向溢出、reduced motion 仍动画、Root 降级后迟到响应回填。

```js
assert.doesNotMatch(homeSource, /getHome|public-document/)
assert.doesNotMatch(layoutSource, /shellLinks.*publication|home\.value.*shellLinks/)
assert.doesNotMatch(homeSource, /PublicContentState[^\n]+status=.*error/)
```

- [ ] **Step 2：运行前端全量门禁**

```bash
npm test
VITE_USE_MOCK=false npm run build
node scripts/check-public-route-chunks.mjs
git diff --check
```

Expected: 全部退出 0；保留已知 chunk size 告警但不得新增生产 mock、跨路由同步公共包或构建错误。

- [ ] **Step 3：运行可见浏览器矩阵**

在 production preview 和 deterministic local backend 上验证：

- 首页接口 503 时固定 Hero、优势、CTA、导航和页脚；动态区块不存在。
- 有效发布时公告、FAQ、精选模型显示且版本一致。
- 隐藏、未来生效、软删除内容不可见。
- Root 新增、编辑、排序、隐藏、软删除、预览、发布、历史、恢复。
- 非 Root 访问管理路由被拒绝。
- 375、390、768、1280、1600；浅色、深色；键盘、焦点、Esc 和 reduced motion。

Expected: 每个场景保存 URL、视口、角色、请求计数、release version、断言和 console/page error；敏感值脱敏。

- [ ] **Step 4：只按实际结果更新 tracker**

如果本地全量和 synthetic 浏览器均通过，将 `web-012` 保持 `in_progress`，新增 `PASS_LIMITED_SCOPE` 证据。生产迁移、部署、真实内容发布、真实上游和公开 HTTPS 保持 `NOT_RUN`。

- [ ] **Step 5：提交前端验收证据**

```bash
git add src/views/public/public-pages.contract.test.js src/styles/typography.contract.test.js src/router/public-chunks.test.js feature_list.json progress.md docs/agents/validation
git commit -m "test: verify fixed home structured content"
```

## Task 13：真实 MySQL 联合验证与完整审查链

**Files:**
- Modify: `Porsche/feature_list.json`
- Modify: `Porsche/progress.md`
- Modify: `Porsche-Web/feature_list.json`
- Modify: `Porsche-Web/progress.md`
- Create: scoped validation artifacts under each repository's existing `docs/agents/validation/` or `docs/superpowers/reports/validation/` convention

- [ ] **Step 1：运行真实 MySQL 0020 和事务用例**

使用独立、loopback-only、临时 MySQL 8 容器，执行 0001–0020 up/status、0020 down/up、部分 DDL 恢复、并发 revision、发布/恢复和清理。测试凭据仅存在于私有临时目录，结束后按完整容器 ID 精确删除。

```bash
TEST_DATABASE_URL="$TEST_DATABASE_URL" GOCACHE=/private/tmp/porsche-fixed-home-go-cache go test ./internal/migration ./internal/service ./internal/handler -run 'Test.*PublicHome|Test.*PublicContent|Test.*PublicCatalog' -count=1
TEST_DATABASE_URL="$TEST_DATABASE_URL" GOCACHE=/private/tmp/porsche-fixed-home-go-cache go test -race ./internal/service ./internal/handler -run 'Test.*PublicHome|Test.*PublicContent' -count=1
```

Expected: 0 fail、0 unexpected skip；命名资源和监听端口清理为零。

- [ ] **Step 2：冻结审查 scope 和 baseline**

后端 scope 包含 0020、公共内容 models/service/dto/handler/router/renderer、合同、测试和本轮 tracker。前端 scope 包含 interface contract、公共 API/store/layout/home/admin/preview/components/styles/i18n、测试和 tracker。运行仓库原生 helper：

```bash
python3 docs/agents/review_snapshot.py baseline --scope /private/tmp/porsche-fixed-home-structured-content-review/backend/scope.json --output -
python3 docs/agents/review_snapshot.py baseline --scope /private/tmp/porsche-fixed-home-structured-content-review/frontend/scope.json --contract interface-contract.json --output -
```

授权 writer 将 stdout 原始字节 exclusive 保存为各自 `baseline.json`，记录 SHA-256 和 stdout ID。

- [ ] **Step 3：生成最终 snapshot 并依序审查**

每个仓库生成 snapshot 后，严格执行：

1. Spec Review；
2. Security Review；
3. 后端 Test Verification / 前端 Quality Gate；
4. 两个 Controller 做跨仓库合同和联合证据汇总。

任何受审文件变化都废止旧 snapshot 和下游结论，修复后从 Spec Review 重新开始。

- [ ] **Step 4：记录限定结论**

报告必须绑定后端 revision、前端 revision、合同 version/status/SHA-256、snapshot ID、测试命令、退出码、skip、浏览器矩阵、真实 MySQL 版本和 cleanup。未执行的生产项目保持明确状态。

- [ ] **Step 5：提交最终本地证据**

```bash
git -C /Users/xuzhihao/code/Porsche/.worktrees/fixed-home-structured-content add feature_list.json progress.md docs/superpowers/reports/validation
git -C /Users/xuzhihao/code/Porsche/.worktrees/fixed-home-structured-content commit -m "docs: record structured home validation"
git -C /Users/xuzhihao/code/Porsche-Web/.worktrees/fixed-home-structured-content add feature_list.json progress.md docs/agents/validation
git -C /Users/xuzhihao/code/Porsche-Web/.worktrees/fixed-home-structured-content commit -m "docs: record structured home validation"
```

## Task 14：准备合并与生产验收包

**Files:**
- Create or modify only the repository-standard final report files selected in Task 13

- [ ] **Step 1：检查与最新远端 main 的关系**

```bash
git fetch origin main
git merge-base --is-ancestor origin/main HEAD
git log --oneline --left-right origin/main...HEAD
```

Expected: 若远端已前进，先在隔离工作树合入/变基并重新执行 Task 12–13；不得带未审查漂移直接推送。

- [ ] **Step 2：形成可审阅交付包**

交付包列出用户可见前后对比：接口 503 时首页由整页错误变为固定主体；发布后才出现真实公告、FAQ、精选模型。附迁移版本、回滚边界、合同、审查结论和所有 `NOT_RUN` 项。

- [ ] **Step 3：停止在本地完成边界**

只有用户明确要求后，才执行 push、PR、merge、生产 migration、`restart-all.sh`、内容发布或线上验收。计划执行完成时向用户提供可选择的下一动作和精确候选 revision。
