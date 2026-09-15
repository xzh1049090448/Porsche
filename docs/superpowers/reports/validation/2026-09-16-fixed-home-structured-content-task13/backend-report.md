# Fixed Home Structured Content — Task 13 后端真实数据层验证记录

日期：2026-09-16
后端代码候选：`8303a28`（包含 `f6ed4e1` exact action target locks 与 `8303a28` atomic structured artifact renderer）
配对前端代码候选：`78006b360b1f005ded208b68aae6464c67891b52`
合同：`v1.0.0-p0` / `agreed_for_implementation` / SHA-256 `3bb7932bf8637b6fcbe3b26694b4e63aa0fb72a047fb127f817865d46e0c7058`
当前结论：`PASS_LIMITED_SCOPE`

## 已验证范围

Task 13 在 disposable、loopback-only 的 MySQL 8.4.11 和 Redis 7-alpine 上验证了迁移 0001–0020、公共内容/模型/价格的真实事务与并发路径、管理动作目标锁和结构化发布产物 renderer。MySQL 使用 tmpfs，Redis 无持久化；测试凭据仅存放于私有临时目录，未读取生产环境文件。

- 迁移 `up`/`status` 在验证前后均为 20 行，首项 0001、末项 0020。
- 计划内 normal gate 使用 `-p 1`：migration 3.860s、service 3.178s、handler 0.519s，全部通过且 0 unexpected skip。
- 计划内 race gate：service 7.051s、handler 1.643s，全部通过且 0 unexpected skip。
- root 允许 loopback 后执行的静态全仓 `go test ./...` 通过；真实 fixture 下 service 全包串行 171.312s、handler 全包串行 17.009s 通过。
- `go vet ./...`、构建、`deploy/test-dockerfile.sh`、`gofmt -d` 与 `git diff --check` 通过。
- `public-render-job` 的确定性 stale-overwrite 用例先 RED 后通过 `flock` 序列化变为 GREEN，race `-count=3` 通过；覆盖安全根目录、拒绝 symlink、目录 0700、文件 0600、canonical SHA/manifest、generation/fence、原子 current 切换以及 stale rollback 不覆盖新 generation。
- renderer 产物是供固定 Vue shell 消费的结构化快照，不是服务端生成 HTML。

## 额外检查偏差

计划外追加的完整真实 migration package 运行失败：历史迁移测试硬编码了已过时的 terminal/dependency 假设。该结果记录为 `NON_BLOCKING_EXTRA_CHECK_FAIL`；计划明确要求的 scoped migration gate 已通过。此失败保留为后续测试债务，不得改写为完整 migration package 通过。

## 清理

- MySQL 完整容器 ID `230609b618dde469fdf1093ed109c747d9989f41fd91b4e5bc3f432f702ba3c0` 已精确删除。
- Redis 完整容器 ID `4156fde30229cb2372c2a8376b608a678b4e952d6a1cf0b6f28af16f0c38f00e` 已精确删除。
- 两个名称均不存在，loopback 端口 52179/54737 已关闭，私有凭据文件已不存在。

## 尚未运行与状态边界

- Task 12 浏览器证据仍是 Playwright `page.route` 合成矩阵：12/12 场景、234/234 断言、32 条脱敏请求、0 unexpected console error、0 page error。它没有被重新标记为真实后端浏览器验收。
- Root 的真实浏览器 CRUD、RBAC、预览、校验、发布、历史、恢复及其浏览器层事务行为均为 `NOT_RUN`。
- external scheduler/systemd、生产 volume mount、前端 artifact reader 为 `NOT_RUN`。
- 生产 migration、deploy、真实内容 publication、public HTTPS 和生产验收均为 `NOT_RUN`。
- P08 生产内容真实性标准继续为 `BLOCKED_PRODUCT`；`web-012` 继续 `in_progress / PASS_LIMITED_SCOPE`。
- 本轮未 push、创建 PR、merge 或 deploy。

Task 13 的 review baseline/final snapshot 与有序 `Spec → Security → Test` 审查尚未写入本记录；任何后续受审文件变化都必须废止旧 snapshot 并从 Spec 重新开始。
