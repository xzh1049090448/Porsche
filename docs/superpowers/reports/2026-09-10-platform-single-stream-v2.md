# BE05 platform single v2 stream 本地交付证据

## 结论与边界

- BE05 single v2 stream 的本地实现与真实 fixture 验证完成。候选代码头为 `d09d493532d1730745599bbc2d50377b6f6447ee`；Task 9 测试、报告与 tracker 由本文所在的 `docs(platform): record single v2 stream evidence` 提交封装。
- `go-018` 保持 `in_progress`。BE06 compare v2 stream、前后端联合验收、生产迁移与部署、公开 HTTPS 验证、真实付费上游调用、push、PR 和 merge 均未运行。
- 测试上游全部是进程内 loopback `httptest`，没有访问真实或付费上游；报告不保存 fixture 凭据、连接串、SQL、Redis key、原始 provider body、prompt/reply 或 lease material。

## 覆盖范围

八个真实 MySQL/Redis integration 顶层测试全部通过且零跳过：新会话成功、既有会话成功、断连后 GET、取消且无持久化、续租胜过 converger、commit-unknown reconcile、duplicate/quota 并发竞争，以及失败矩阵无 durable content。

成功路径统一验证 receipt/result 各一条、精确两条消息、usage 一条、daily calls 增量一、token totals 相等、requested/existing provenance、assistant GUID 与 Redis completed 状态一致，以及 `Control.Get` 水合后的最终内容。非成功路径比较前后 durable snapshot，验证 conversation/message/usage/receipt/quota/token 均无变化。测试数据使用唯一实体 ID 和 owned Redis 范围并精确清理。

## Fixture 与迁移

- MySQL `8.4.11` 与 Redis `7.4.11`，均来自本机已有镜像并以 `--pull=never` 创建。
- 两只 disposable 容器均为唯一名称和完整 ID、`codex.task=be05-task9` 标签、AutoRemove、read-only rootfs、tmpfs、无 mount/命名卷，并只映射到 `127.0.0.1` 随机端口。
- 每个主要门禁前都精确重建专用数据库、清空专用 Redis DB，再执行 `0001`–`0013` migration up/status。最终账本为 13 项：首项 `0001`、第 11 项 `0011`、末项 `0013`。
- 测试凭据只存在于 mode `0700` 私有目录内的 mode `0600` 文件，未写入仓库或命令结果。

## TDD 与失败记录

- 迁移断言先以旧的 11 项预期产生 RED；测试随后改为精确验证 13 项账本并 GREEN。
- 八个 integration 名称先因缺少 harness helper 编译 RED，再补最小真实 fixture harness。取消用例暴露 `httptest` 请求在响应头前无法仅靠客户端取消结束的测试泄漏，分类 `FAIL_TEST_FIXTURE`; 使用测试本地 release cleanup 与 bounded timeout 修复后通过。
- 真实续租用例发现 `RenewLease` 延长 lease 却未同步 activity timestamp，runner 校验拒绝记录，分类 `FAIL_PRODUCT`。产品修复 `027073509225d7d9384d529d7222dbb32ea3772a` 独立通过 SPEC、SECURITY 与真实 Redis TEST review。
- 首次 full real-fixture 重跑发现：数据库已到 `0013` 时 runner 仍先调用旧版本精确 verifier，导致重跑失败，分类 `FAIL_PRODUCT`。产品修复 `d09d493532d1730745599bbc2d50377b6f6447ee` 独立通过 SPEC、SECURITY 与真实 MySQL TEST review。
- 另有三类未计为产品失败：sandbox 禁止 loopback bind、私有 fixture URL 格式错误、一次性测试 action key 格式错误，分别分类为 `FAIL_ENV_SANDBOX` 与 `FAIL_ENV_FIXTURE_CONFIG`；修正环境后均从 fresh fixture 重跑。

## 最终验证

所有下列命令都在最终修复链上运行；涉及共享数据状态的门禁前均 fresh reset/migrate：

- `go test -p 1 ./internal/service ./internal/handler ./internal/app ./internal/router -run 'TestPlatformSingleGeneration|Test.*Platform.*Single.*V2' -count=1`：4 packages PASS，114 test events PASS，八项 BE05 顶层 integration PASS，0 skip。
- `go test -race -p 1 ./internal/service ./internal/handler ./internal/app ./internal/router -run 'TestPlatformSingleGeneration|Test.*Platform.*Single.*V2' -count=1`：4 packages PASS，114 test events PASS，无 race report，八项 BE05 顶层 integration PASS，0 skip。
- `go test -p 1 ./... -count=1`：17 packages PASS，3403 test events PASS，0 FAIL；八项 BE05 顶层 integration PASS，0 skip。
- `go test -race -p 1 ./internal/service ./internal/handler ./internal/app ./internal/router ./internal/whitelabel -count=1`：5 packages PASS，2488 test events PASS，0 FAIL、无 race report；八项 BE05 顶层 integration PASS，0 skip。
- `go build ./...`、`go vet ./...`、`git diff --check`：exit 0。

唯一 skip inventory：`TestAdminUsersReadPerformance` 在 full 与 affected race 中各按其显式 opt-in 开关跳过；它不是 BE05 fixture 测试，不影响八项 BE05 零跳过结论。

## 隐私、范围与清理

- 四类 test-only 敏感 sentinel 对本报告、`progress.md` 和 `feature_list.json` 的扫描为零匹配；仓库证据不包含其值。
- Task 9 直接变更限于 integration test、迁移账本 test-only 断言、本报告与两个 tracker。最终审查的 10 文件 snapshot 另纳入两份已独立审查的续租修复文件和三份已独立审查的 migration 重跑修复文件；审查同时检查累计 `79bff604..FINAL` diff。
- MySQL 完整 ID `04a10c51a45f3c6d2ae5481ec1f65e359bd93f88528dcd10b1e1d707603ddb2b` 与 Redis 完整 ID `36797284462bef6a28ed85ebf1c8436b9172ca7e7b39865e854a95812a907f29` 已精确停止；AutoRemove 后两者均返回 not found。
- `codex.task=be05-task9` 容器、volume、network 列表均为空；两个 loopback 随机端口均无 listener。旧私有凭据与失效证据目录已精确删除，只保留不含凭据的新 10 文件 review baseline/snapshot 目录。

## 未执行事项

未修改 Porsche-Web；未运行 BE06 compare streaming、前后端联合验收、生产 migration/deploy、公开 HTTPS 验证或真实上游；未 push、创建 PR 或 merge。BE05 本地通过不能外推为整体 `go-018` 或生产发布通过。
