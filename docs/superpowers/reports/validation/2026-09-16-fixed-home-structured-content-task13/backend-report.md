# Fixed Home Structured Content — Task 13 后端真实数据层验证记录

日期：2026-09-16
后端实现候选：`77c4e003310f3194f3e7a90cdadb7d650cbd3ba1`（包含 `f6ed4e1` exact action target locks 与 `8303a28` atomic structured artifact renderer）
配对前端实现候选：`61c1ccb6512198539ae13b3d98e4043ef77a1dca`
跨仓库总合同：`interface-contract.json`，`v1.0.0-p0 / agreed_for_implementation`，SHA-256 `9bc9c70e70bb45b63185b3b619ae49e5e6a251ba3c4a30f64539774cb272a3ce`
后端公共内容子合同：`docs/agents/contracts/public-content-pricing-v1.json`，`v2 / implemented_locally_pending_acceptance`，SHA-256 `4db4380dcef26f0098443f591d9fe098f9faefd32a367f02ce2a14ba62d8309d`
当前结论：`PASS_LIMITED_SCOPE`

## 已验证范围

Task 13 在 disposable、loopback-only 的 MySQL 8.4.11 和 Redis 7-alpine 上验证了迁移 0001–0020、公共内容/模型/价格的真实事务与并发路径、管理动作目标锁和结构化发布产物 renderer。MySQL 使用 tmpfs，Redis 无持久化；测试凭据仅存放于私有临时目录，未读取生产环境文件。

- 迁移 `up`/`status` 在验证前后均为 20 行，首项 0001、末项 0020。
- 计划内 normal gate：`go test -p 1 ./internal/migration ./internal/service ./internal/handler -run 'Test.*PublicHome|Test.*PublicContent|Test.*PublicCatalog' -count=1`，migration 3.860s、service 3.178s、handler 0.519s，exit 0，0 unexpected skip。
- 计划内 race gate：`go test -race -p 1 ./internal/service ./internal/handler -run 'Test.*PublicHome|Test.*PublicContent' -count=1`，service 7.051s、handler 1.643s，exit 0，0 unexpected skip。
- root 允许 loopback 后执行的静态全仓 `go test ./... -count=1` exit 0；真实 fixture 下 service 全包串行 171.312s、handler 全包串行 17.009s 通过。
- `go vet ./...`、`go build ./...`、`deploy/test-dockerfile.sh`、`gofmt -d` 与 `git diff --check` 均 exit 0。
- `public-render-job` 的确定性 stale-overwrite 用例先 RED，加入 `flock` 序列化后 GREEN；focused race `-count=3` exit 0。覆盖安全根目录、拒绝 symlink、目录 0700、文件 0600、canonical SHA/manifest、generation/fence、原子 current 切换以及 stale rollback 不覆盖新 generation。
- renderer 产物是供固定 Vue shell 消费的结构化快照，不是服务端生成 HTML。

## 额外检查偏差

计划外命令 `go test -p 1 ./internal/migration ./internal/service ./internal/handler -count=1` overall exit 1。service 与 handler 在同次运行中通过；migration 的四个历史断言失败：

- `TestBusinessGroupMigrationOnIsolatedMySQL/backfills_active_and_tombstoned_users`
- `TestPublicPriceDraftStateMigrationRealMySQLDownAndReapply`
- `TestPublicRenderJobTerminalMigrationRealMySQLDownAndReapply`
- `TestUpstreamMonitorLeaseMigrationRealMySQLDownAndReapply`

这些用例硬编码了已过时的 terminal/dependency 假设，记录为 `NON_BLOCKING_EXTRA_CHECK_FAIL`。计划明确要求的 scoped migration gate 已通过；本记录不把完整 migration package 写成通过。

## 清理

- MySQL 完整容器 ID `230609b618dde469fdf1093ed109c747d9989f41fd91b4e5bc3f432f702ba3c0` 已精确删除。
- Redis 完整容器 ID `4156fde30229cb2372c2a8376b608a678b4e952d6a1cf0b6f28af16f0c38f00e` 已精确删除。
- 两个名称均不存在，loopback 端口 52179/54737 已关闭，私有凭据文件已不存在。

## 契约状态纠偏后的重验证

- `go test ./internal/dto ./internal/router -run 'TestPublicContentPricingContract|TestPublicContentPricingAdminRoutesMatchFrozenContract' -count=1` 使用私有 Go cache 后 exit 0。
- `go test ./... -count=1` 在获准的 loopback 环境下 exit 0；先前沙箱内运行仅因本机监听权限失败，不计为产品失败。
- `go vet ./...`、`go build ./...`、`deploy/test-dockerfile.sh`、变更 Go 文件格式检查和 `git diff --check` 再次 exit 0。

## 规格复审缺陷与修复

- 首个最终快照后，前端独立规格审查发现两项阻塞：Root 首页预览错误要求无空格 `noindex,nofollow`；两份合同未写入后端既有稳定 `modelKey` 规则，且公开详情客户端接受后端会拒绝的大小写、点和下划线。该前端快照及配对后端快照均失效。
- RED 证据：后端合同测试因缺失 `minLength/maxLength/pattern` 失败；前端定向 55 项中合同、公开详情键与真实预览头三项失败。
- 后端 `77c4e00` 将既有规则冻结为 `^[a-z](?:[a-z0-9]|-[a-z0-9])*$`、长度 1–128，并把预览头合同写为字面量 `noindex, nofollow`。前端 `61c1ccb` 同步合同、严格公开详情输入与预览响应校验。
- GREEN 证据：后端 dto/router/publiccontent 定向通过；前端定向 61/61、跨仓库公共内容子合同字节一致；后端 `go test ./... -count=1` 通过，前端完整 `npm test` 1138/1138、0 fail/skip/cancel/todo，48.734s；production build 与公共 chunk 9/9、171428 JS bytes/20362 CSS bytes 通过。

## 尚未运行与状态边界

- Task 12 浏览器证据仍是 Playwright `page.route` 合成矩阵：12/12 场景、234/234 断言、32 条脱敏请求、0 unexpected console error、0 page error。它没有被重新标记为真实后端浏览器验收。
- Root 的真实浏览器 CRUD、RBAC、预览、校验、发布、历史、恢复及其浏览器层事务行为均为 `NOT_RUN`。
- external scheduler/systemd、生产 volume mount、前端 artifact reader 为 `NOT_RUN`。
- 生产 migration、deploy、真实内容 publication、public HTTPS 和生产验收均为 `NOT_RUN`。
- P08 生产内容真实性标准继续为 `BLOCKED_PRODUCT`；`web-012` 继续 `in_progress / PASS_LIMITED_SCOPE`。
- 本轮未 push、创建 PR、merge 或 deploy。

Task 13 的最终 review baseline/snapshot 与有序 `Spec → Security → Test` 审查将在所有受审文件冻结后生成。评审回执写入工作区外的 `/private/tmp/porsche-fixed-home-structured-content-review/backend/final-review-receipt.json`，避免回执自身改变受审快照。
