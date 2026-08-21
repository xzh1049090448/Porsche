# 白牌上游接入 Implementation Plan

> For agentic workers: REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Steps use checkbox syntax for tracking.

**Goal:** 将静态多厂商模型路由替换为唯一 JieKou AI 白牌上游，让 Go Gateway 与 Vue 平台共用动态目录、ACL、Chat/SSE 契约。

**Architecture:** 新增 Go internal/whitelabel，集中完成配置、缓存、模型状态、校验、上游调用和脱敏错误；Gin Handler 只负责认证与 HTTP。Vue 只调用本地平台接口。后续渠道选择器加在服务之后，不改客户端模型 ID。

**Tech Stack:** Go 1.22、Gin、GORM/SQLite、net/http/httptest；Vue 3、Pinia、Vite、Node 内置 test。

---

## 文件结构

### 后端：/Users/xuzhihao/code/Porsche/.worktrees/white-label-upstream

- Create: internal/whitelabel/{types,errors,validation,service,sse}.go 和对应测试。
- Modify: internal/config/config.go、internal/app/state.go、internal/handler/{gateway_tokens,platform,admin,health}.go、internal/service/platform_chat.go。
- Modify: internal/router/router_test.go，并创建 internal/handler/*_whitelabel_test.go。
- Delete: internal/gateway/gateway.go、internal/registry/registry.go、config/models.yaml、独占的静态模型常量/样例。
- Modify: README.md、.env.example（如有）、feature_list.json、progress.md。禁止提交 .env、数据或 Harness 日志。

### 前端：/Users/xuzhihao/code/Porsche-Web/.worktrees/white-label-upstream/llm-platform

- Create: src/utils/model-catalog.js 和 src/utils/model-catalog.test.js。
- Modify: src/api/platform.js、src/stores/{settings,chat}.js、src/components/chat/ModelPanel.vue、src/views/{ApiKeys,Chat}.vue、src/utils/sse.js、src/i18n/messages.js。
- 删除 constants/models.js、utils/api-mapper.js、api/mock.js 的生产静态模型回退和引用。
- 不触碰主工作区的用户修改：ModelAnalyticsPanel.vue、未跟踪 package-lock.json、.claw/、.sandbox-*、根 Harness 文件。

### Task 1: 建立隔离环境并固定基线

**Files:**
- Modify: 后端 feature_list.json、progress.md
- Create: 前端 .worktrees/white-label-upstream

- [ ] **Step 1: 创建前端 worktree 并确认忽略规则**

~~~bash
cd /Users/xuzhihao/code/Porsche-Web
git check-ignore -q .worktrees
git worktree add .worktrees/white-label-upstream -b feature/white-label-upstream
~~~

Expected: worktree 被忽略，主工作区不发生修改。

- [ ] **Step 2: 后端运行标准基线**

~~~bash
cd /Users/xuzhihao/code/Porsche/.worktrees/white-label-upstream
GOCACHE=/private/tmp/porsche-go-build-cache ./init.sh
~~~

Expected: go test ./... 通过；若失败，记录 blocker 并停止。

- [ ] **Step 3: 前端安装并验证基线**

~~~bash
cd /Users/xuzhihao/code/Porsche-Web/.worktrees/white-label-upstream/llm-platform
npm ci
npm test
npm run build
~~~

Expected: 测试和构建通过；只提交原本已追踪的 lockfile。

- [ ] **Step 4: 记录唯一活动功能并提交**

将 go-004 白牌子目标标记为 in_progress，只写真实基线证据。

~~~bash
git add feature_list.json progress.md
git commit -m "chore: record white-label baseline"
~~~

### Task 2: 白牌配置、统一错误与请求校验（TDD）

**Files:**
- Create: internal/whitelabel/errors.go、validation.go、validation_test.go
- Modify: internal/config/config.go

- [ ] **Step 1: 编写失败测试**

~~~go
func TestValidateRequestRejectsUnsafeMediaAndOversize(t *testing.T) {
  for _, raw := range []string{"http://example.com/a.png", "https://127.0.0.1/a.png", "https://u:p@example.com/a.png"} {
    requireCode(t, ValidateMediaURL(raw), CodeInvalidRequest)
  }
  requireCode(t, ValidateDataImage(oversizedPNGDataURI()), CodeInvalidRequest)
}
func TestErrorResponseNeverLeaksUpstream(t *testing.T) {
  got := PublicError(ErrUpstreamUnavailable("vendor.example secret"), "req_test")
  if strings.Contains(got.Error.Message, "vendor") || strings.Contains(got.Error.Message, "secret") { t.Fatal("leak") }
}
~~~

- [ ] **Step 2: 确认红灯**

Run: GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/whitelabel -run TestValidate|TestError

Expected: FAIL because the package/functions are absent.

- [ ] **Step 3: 最小实现**

实现 WhiteLabelSettings：只接受 UPSTREAM_REGION=cn|global，固定对应 URL；JIEKOU_API_KEY 或非空 JIEKOU_ALLOWED_MODELS 缺失时 fail-closed。实现 PRD 错误对象/状态/type；校验 12 MiB body、128 messages、1 MiB text、n/temperature/top_p/penalties/stop、tools/response_format/stream_options、HTTPS/data-image。

- [ ] **Step 4: 绿色验证与提交**

~~~bash
GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/whitelabel -count=1
git add internal/config/config.go internal/whitelabel
git commit -m "feat: add white-label configuration and validation"
~~~

### Task 3: 目录、详情、ACL 与状态机（TDD）

**Files:**
- Create: internal/whitelabel/types.go、service.go、service_test.go
- Modify: internal/app/state.go、internal/service/gateway_token.go

- [ ] **Step 1: 编写缓存/状态机失败测试**

~~~go
func TestCatalogUsesStaleFor24HoursAndTrusted404DisablesModel(t *testing.T) {
  up := newWhiteLabelServer(t, catalog("model-a"))
  svc := newService(t, up.URL, clock)
  requireModels(t, svc.ListModels(context.Background(), allACL), "model-a")
  up.FailCatalog(http.StatusBadGateway); clock.Add(23*time.Hour)
  requireStale(t, svc.ListModels(context.Background(), allACL))
  up.DetailStatus("model-a", http.StatusNotFound)
  requireCode(t, svc.GetModel(context.Background(), "model-a", allACL), CodeModelUnavailable)
}
~~~

- [ ] **Step 2: 确认红灯**

Run: GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/whitelabel -run TestCatalog|TestModelACL -count=1

- [ ] **Step 3: 实现服务**

实现目录 5 分钟、详情 1 小时、24h stale/cold/可信 404 状态机；清洗非法 ID、负值/NaN 元数据；先服务 allowlist 后 user/token ACL。详情 URL 只编码一次 path segment。将 WhiteLabelService 注入 app.State；禁止改动 Python MySQL 共享表。

- [ ] **Step 4: 绿色验证与提交**

~~~bash
GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/whitelabel ./internal/service ./internal/db -count=1
git add internal/app/state.go internal/service/gateway_token.go internal/whitelabel
git commit -m "feat: add cached white-label model service"
~~~

### Task 4: Gateway 动态模型与 Chat/SSE（TDD）

**Files:**
- Modify: internal/handler/gateway_tokens.go、internal/router/router_test.go
- Create: internal/handler/gateway_whitelabel_test.go

- [ ] **Step 1: 写失败测试**

~~~go
func TestGatewayModelsUseTokenACLAndDynamicCatalog(t *testing.T) { /* mock catalog a,b; token only a */ }
func TestGatewayChatRejectsBeforeUpstream(t *testing.T) { /* revoked/IP/model/max_tokens invalid; calls == 0 */ }
func TestGatewaySSEPostFirstChunkEmitsErrorAndDone(t *testing.T) { /* chunk then error event and [DONE] */ }
~~~

- [ ] **Step 2: 确认红灯**

Run: GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/handler ./internal/router -run Gateway -count=1

- [ ] **Step 3: 改造 /v1**

增加 GET /v1/models/:id；列表/详情使用服务交集；chat 绑定完整 DTO，先 GatewayTokens.Authenticate 再白牌校验。首字节前返回稳定 JSON/HTTP，首字节后严格发送 PRD event:error 和 [DONE]；不复制上游 header/body。

- [ ] **Step 4: 绿色验证与提交**

~~~bash
GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/handler ./internal/router -count=1
git add internal/handler/gateway_tokens.go internal/handler/gateway_whitelabel_test.go internal/router/router_test.go
git commit -m "feat: route gateway API through white-label service"
~~~

### Task 5: 平台模型、单聊、compare 与健康检查（TDD）

**Files:**
- Modify: internal/handler/platform.go、internal/service/platform_chat.go、internal/handler/admin.go
- Create: internal/handler/platform_whitelabel_test.go、internal/service/platform_chat_test.go

- [ ] **Step 1: 写失败测试**

~~~go
func TestPlatformModelDetailHidesUnauthorizedAs404(t *testing.T) {}
func TestCompareRejectsFourBeforeUpstream(t *testing.T) {}
func TestCompareSSEMuxContinuesAfterOneModelFails(t *testing.T) {}
func TestHealthCheckSerializesSameModel(t *testing.T) {}
~~~

- [ ] **Step 2: 确认红灯**

Run: GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/handler ./internal/service -run Platform|Compare|Health -count=1

- [ ] **Step 3: 实现平台契约**

/models 返回 {data,catalog_stale}，新增详情；平台 chat/compare 强制 max_tokens、n=1、compare 1–3。compare 并发输出 chunk/model_done/model_error，全部结束才 [DONE]。保留会话/RAG/账务语义，不记录 prompt/completion。健康检查固定 Reply with OK.、5 tokens、10 秒、禁 tools，按模型互斥并返回 409。

- [ ] **Step 4: 绿色验证与提交**

~~~bash
GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/handler ./internal/service -count=1
git add internal/handler/platform.go internal/handler/admin.go internal/service/platform_chat.go internal/handler/platform_whitelabel_test.go internal/service/platform_chat_test.go
git commit -m "feat: add dynamic platform models and compare streams"
~~~

### Task 6: 删除旧路由并完成后端回归

**Files:**
- Delete: internal/gateway/gateway.go、internal/registry/registry.go、config/models.yaml、静态模型常量（仅无引用时）
- Modify: internal/handler/health.go、README.md、.env.example、feature_list.json、progress.md

- [ ] **Step 1: 写旧路径不存在的检查**

~~~bash
rg -n "ModelRegistry|KeyPool|MODELS_CONFIG_PATH|APIKeysEnv|DeepSeek|Zhipu" internal config cmd
~~~

Expected: 无生产旧厂商路由引用。

- [ ] **Step 2: 删除旧实现并更新说明**

健康端点报告白牌目录状态/模型数；README 与示例配置只列 UPSTREAM_REGION、JIEKOU_API_KEY、JIEKOU_ALLOWED_MODELS，示例只用假值。

- [ ] **Step 3: 全量验证与提交**

~~~bash
GOCACHE=/private/tmp/porsche-go-build-cache go test -count=1 ./...
go vet ./...
git diff --check
git add -A
git commit -m "refactor: remove legacy upstream routing"
~~~

### Task 7: 前端动态目录与详情（TDD）

**Files:**
- Create: src/utils/model-catalog.js、src/utils/model-catalog.test.js
- Modify: src/api/platform.js、src/stores/settings.js

- [ ] **Step 1: 写失败的 Node 测试**

~~~js
test("normalizes metadata and formats price", () => {
  const model = normalizeModel({ id: "a", title: "<img>", input_token_price_per_m: 4.75 })
  assert.equal(model.title, "<img>")
  assert.equal(formatPrice(model.inputTokenPricePerM), "$4.75/Mt")
})
test("limits compare IDs to three", () => assert.deepEqual(limitCompareIds(["a","b","c","d"]), ["a","b","c"]))
~~~

- [ ] **Step 2: 确认红灯**

Run: npm test -- src/utils/model-catalog.test.js

- [ ] **Step 3: 实现 API/store**

listModels() 解析 {data,catalog_stale}，失败时不得 import/返回静态 MODELS；新增 getModel(id)。store 保存 loading/error/stale/动态模型，移除失效选择，compare 最多三项；保留通用本地场景提示词。

- [ ] **Step 4: 绿色验证与提交**

~~~bash
npm test
git add src/api/platform.js src/stores/settings.js src/utils/model-catalog.*
git commit -m "feat: load dynamic white-label model catalog"
~~~

### Task 8: 前端模型 UI、API Key 与多路 compare SSE（TDD）

**Files:**
- Modify: src/components/chat/ModelPanel.vue、src/stores/chat.js、src/utils/sse.js、src/views/ApiKeys.vue、src/views/Chat.vue、src/i18n/messages.js
- Modify/Delete: src/constants/models.js、src/utils/api-mapper.js、src/api/mock.js 及引用

- [ ] **Step 1: 写失败测试**

~~~js
test("keeps an isolated compare failure from ending another model", () => {
  assert.equal(parseCompareEvent("model_error", "{\"model\":\"a\",\"error\":{\"code\":\"gateway_upstream_unavailable\"}}").kind, "error")
  assert.equal(parseCompareEvent("model_done", "{\"model\":\"b\"}").kind, "done")
})
~~~

- [ ] **Step 2: 确认红灯**

Run: npm test -- src/utils/model-catalog.test.js

- [ ] **Step 3: 实现页面与流式适配**

ModelPanel 仅用动态模型，加载禁用、stale/空态、搜索、详情与 $…/Mt；使用 Vue 文本绑定，禁止 v-html。ApiKeys 复用动态列表。chat 处理 chunk/model_done/model_error/[DONE]，单模型错误不取消其他流。Chat 显示最小数据处理提示。

- [ ] **Step 4: 清除生产静态回退、验证并提交**

~~~bash
npm test
npm run build
git diff --check
git add src
git commit -m "feat: render dynamic white-label models in chat"
~~~

### Task 9: 安全门禁、预发布说明与交付

**Files:**
- Modify: 后端 feature_list.json、progress.md、README.md；前端 Harness 文件仅在已跟踪时更新

- [ ] **Step 1: 安全复审**

核验密钥/Authorization/prompt 不泄漏；ACL 与所有请求校验在上游前；详情不枚举；SSE 不回显上游错误；前端无 v-html、storage、URL 或 console secret sink。任意 Critical/High 必须修复并复测。

- [ ] **Step 2: 运行全部门禁**

~~~bash
cd /Users/xuzhihao/code/Porsche/.worktrees/white-label-upstream
GOCACHE=/private/tmp/porsche-go-build-cache go test -count=1 ./...
go vet ./...
git diff --check
cd /Users/xuzhihao/code/Porsche-Web/.worktrees/white-label-upstream/llm-platform
npm test
npm run build
git diff --check
~~~

- [ ] **Step 3: 记录预发布冒烟步骤并提交**

记录 mock-only 限制；预发布只用可轮换低权限环境变量 Key 和一个允许模型验证目录、详情、非流、SSE、compare 上限。不得在任何文件或命令历史写入真实 Key。分别提交两个仓库的目标文件，不加入 .claw/、.sandbox-*、用户 analytics 修改或真实 .env。

## 计划自检

- 覆盖 PRD-260820 的区域/密钥、目录/详情/状态机、ACL、chat/SSE、compare、健康检查、前端动态 UI、隐私、错误、安全、测试与预发布。
- PRD-260819-01/02/03 的渠道、审计、配额/限流只保留边界，不在本计划实现。
- 每个代码任务从失败测试开始，包含明确验证命令和独立提交边界。
