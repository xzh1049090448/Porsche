# BE06 platform compare v2 stream 本地交付证据

## 结论与边界

### 2026-09-12 本地跨仓库验收增补

- 后端候选 `4680c549bd28f8be57a438061cf9161914c046a0` 已绑定前端代码候选 `db5e25f27e879d5697fe758e5f40048105312b6d` 与前端证据提交 `19dff079e5525ef6bc159a61bc4500073dbaa834`。前端冻结合同为 `v1.0.0-p0` / `agreed_for_implementation`，`platform-chat-sse.v2` 为 `closed`，SHA-256 为 `47cfbc485c4df0f5d2c12539f389f466c97bb8318adf04966757420287d10a2f`。
- fresh 后端全量、build、vet、affected race 均 PASS；全新 loopback-only MySQL 8.4 / Redis 7.4 下八个 BE06 compare integration 在 normal 与 `race -p 1` 各 `8/8` PASS、0 skip。前端六合同全量 `926/926`、0 skip，production build/init PASS。真实本地浏览器以 production build + synthetic backend/upstream 覆盖完整矩阵：standard 使用实际 RAF 每帧一个字符簇，25ms 目标约 33ms且标点无额外停顿；reduced-motion send/recovery 观测 29 次增长、单次最多 8 字符簇并核对 modeReason；手动上滚不被增长抢走，Back to latest 可键盘恢复；生命周期、断流 GET 恢复、权威取消、三模型部分失败、重复抑制、畸形敏感值不渲染、IME、焦点、375/390 与 unmount 也全部 PASS。独立最终 verdict PASS。
- 新 fixture 已精确清理 2 个容器、2 个卷、1 个网络及 loopback listener。本地前后端合同、实现和 synthetic 联合验收现为 PASS；下方 2026-09-11 的“前端尚未实现/合同为空”仅是当时历史快照，已由本增补取代。
- `go-018` 继续 `in_progress`，仅保留 production migration/deployment、公开 HTTPS、真实账号及付费或真实上游验收，以及 push/PR/合入 main。上述发布动作均未执行，也未在本轮生成最终 review snapshot。

- BE06 compare v2 stream 的后端本地实现与真实 fixture 验证完成。固定基线为 `944309003ce47bbaf949f6c0f28d9bd302016d0f`，pre-report 实现候选为 `20e51fe90a08f0581d3fdf261a3564deaf392b6b`，分支为 `feature/platform-compare-stream-v2`。evidence commit 由本提交生成，最终提交后用 `git rev-parse` 确认。
- Task 1–9 的 implementer、spec 与 quality 修复链最终均通过。实现快照结论为 Spec `SPEC_PASS`、Security `SECURITY_PASS`、Test `PASS_LIMITED_SCOPE`；限定原因仅为生成快照时本报告与 tracker 尚不存在，以及前端和生产尚未验收。后端实现门禁本身为 PASS。
- `go-018` 保持 `in_progress`。BE01–BE06 仅表示后端本地完成；仍需前后端合同对齐、Porsche-Web 实现、联合验收、生产迁移与部署、公开 HTTPS 和真实上游验证。本批未 push、创建 PR、merge、部署、迁移生产或访问真实上游。
- 当前本地 `origin/main` 引用为 `d85c101db3d6898404b9abab677b6fd4c2a4ed87`；分支相对其 ahead 29、behind 96。本批未 fetch、rebase 或 push，因此该引用只记录现场状态，不代表远端最新状态。

## 冻结审查证据

- external scope evidence `scope.json`：SHA-256 `c97964d319f615589c9af59e9e5d751ca4b1bf9f714c62beeaf690de9bb15faa`。
- external baseline evidence `baseline-v2.json`：SHA-256 `49928c9c4e8a6435e3363ae5c749624738adb1a9553e71870d12daa720c60b41`。
- external implementation snapshot evidence `snapshot-vet-fix.json`：snapshot ID `7eb74da31b7be10dcf3c5078ec8cf7cfd907335ec78ea3ccf99c2247ab8d4971`，文件 SHA-256 `082803b968a7ad747527c34a50a2f683a656e080db36f908fcf194cd8ce141c5`。
- implementer、Spec、Security 与 Test 角色均使用 `python3 docs/agents/review_snapshot.py verify --scope scope.json --baseline baseline-v2.json --snapshot snapshot-vet-fix.json` 的相同验证语义；pre-report clean HEAD 的现场复核返回 snapshot verified。加入本文和 tracker 后当前工作树按冻结语义不再等于该 implementation snapshot。

## 实现与协议覆盖

BE06 为 compare generation 增加 owner-bound 单模型失败转换、专用 compare runner、一次 claim/registration、2–3 模型并发 fan-out、单序列化临界区、10 秒共享续租、取消与失败收敛、部分成功持久化、commit-unknown receipt reconcile、app wiring 和 compare SSE v2 handler。单模型失败不取消仍可继续的兄弟模型；authority、encoder 或 store 失败按 generation 级 fail-closed 收敛。没有修改 schema、migration、依赖或部署文件。

公开 SSE 示例只使用合成、已清理字段：

```text
event: meta
data: {"schema":"platform-chat-sse.v2","generation_id":"<generation-uuid>","conversation_guid":"<conversation-guid>","models":["model-a","model-b"]}

event: delta
data: {"generation_id":"<generation-uuid>","model":"model-a","seq":1,"delta":"<sanitized>"}

event: model_done
data: {"generation_id":"<generation-uuid>","model":"model-a","last_seq":1}

event: done
data: {"generation_id":"<generation-uuid>","status":"completed","conversation_guid":"<conversation-guid>","total_tokens_used":3,"models":{"model-a":{"status":"completed","tokens":3},"model-b":{"status":"failed","code":"upstream_error"}}}
```

该示例仅说明实际 wire protocol 的 meta、delta、model_done 与 done 字段结构；当前协议没有单独的 model_start 帧，模型开始由首个 delta 或本地 terminal 帧体现。示例不含真实请求、provider body 或内部 authority material。

## Task 9 真实集成覆盖

下列八个顶层测试在 normal 与 race 中各自 PASS，均为零 skip：

1. `TestPlatformCompareGenerationIntegrationAllModelsSucceed`
2. `TestPlatformCompareGenerationIntegrationPartialSuccess`
3. `TestPlatformCompareGenerationIntegrationAllModelsFailWithoutDurableMutation`
4. `TestPlatformCompareGenerationIntegrationDisconnectThenGET`
5. `TestPlatformCompareGenerationIntegrationCancelNoPersistence`
6. `TestPlatformCompareGenerationIntegrationRenewalBeatsConverger`
7. `TestPlatformCompareGenerationIntegrationCommitUnknownReconciles`
8. `TestPlatformCompareGenerationIntegrationConcurrentQuotaAndDuplicateRaces`

矩阵覆盖 2/3 模型、receipt/result/message/usage 的精确 graph 与请求顺序、仅成功模型计费、GET 顺序、all-failed/cancel 前后 durable count 不变、续租胜过 converger，以及 commit-unknown 的精确调用次数：Finalize 1、LoadReceipt 1、ReconcileComplete 1、Complete 0，且不重放 SQL finalize。额度矩阵覆盖 limited plan 的 `models-1`、exact、more，reset immediate-before 拒绝与 immediate-after 接受，以及 Professional/Enterprise unlimited；并发请求在 upstream barrier 后竞争 quota，且包含 3-model competitor，同时验证 duplicate race。

集成 fixture 使用跨进程 crypto-random canonical UUID；每次 claim 前登记 exact Redis generation key，线程安全 cleanup 只删除本 fixture 创建或可能创建的精确 key，不扫描或删除用户前缀。所有 runner 均使用可取消 context、幂等 release、bounded channel wait 和无条件 bounded join；HTTP barrier 在请求 context 结束时退出。

## Fixture、迁移与清理

- MySQL `8.4.11` 容器完整 ID 为 `6a17bfaf6da87bd80cb0ee1dbef87a900007ed204704d9cb30f66e9ebb085955`；Redis `7.4.11` 容器完整 ID 为 `31bbb3f44f2c3a931a9f3e318b55dc0ee6aa1f93a1ca3230237da4fd53dc8ef2`。清理前 inspect 验证二者标签为 `be06-compare-v2-20260911`。
- 两个服务仅绑定 loopback `127.0.0.1:54761` 与 `127.0.0.1:54794`。迁移账本为 `0001`–`0013`；主要矩阵使用 7 个 fresh `*_test` 数据库，每组前对专用 Redis 执行 FLUSHDB。
- 测试结束后按上述完整 ID 精确 stop/remove；删除 network `porsche-be06-compare-v2-net`、volumes `porsche-be06-compare-v2-mysql-data` 与 `porsche-be06-compare-v2-redis-data`，并删除一次性私有凭据目录。label-filter 的 container/network/volume inventory 均为空，两个 loopback 端口均无 listener。
- 这些是不可恢复的一次性测试资源；未触碰生产、共享 fixture 或其他任务资源。报告不保存连接串、凭据值、Redis key、SQL、原始 provider body、lease 或私有 prompt。

## 最终验证

Task 9 的精确 fixture 门禁：

- `go test -p 1 ./internal/service -run '^TestPlatformCompareGenerationIntegration' -count=1`：八个顶层测试 PASS，0 skip。
- `go test -race -p 1 ./internal/service -run '^TestPlatformCompareGenerationIntegration' -count=1`：八个顶层测试 PASS，0 skip，无 race report。

后端正式门禁结果：

- focused normal：service `217.565s`、handler `23.787s`、app `1.379s`、router `5.764s`，全部 PASS。
- full normal：17 个有测试 package 全部 PASS；JSON census 为 3688 个 test pass event、0 test fail。唯一 test skip 为显式 opt-in 性能用例 `TestAdminUsersReadPerformance`；另 5 个 package no-test event 不计作 test skip。
- canonical serialized race（`go test -race -p 1 ...`）：service `358.033s`、handler `24.805s`、app `1.485s`、router `2.607s`，全部 PASS 且无 race report；router 单独 fresh race 为 `2.697s` PASS。
- `go build ./...`、`go vet ./...`、`git diff --check` 与 tracker JSON 校验均 PASS。
- fixture cleanup 后的无 fixture focused：service `5.099s`、handler `1.326s`、app `0.723s`、router `0.537s`，全部 PASS。

## TDD 与失败历史

- 首次 full 缺少 `ACTION_SECURITY_HMAC_KEY`，既有 action fixture 失败，分类 `FAIL_CONFIG`；单用例加入合成、仅本地使用的值后 PASS。
- 复用已污染 Redis/DB 触发 rate-limit、session-limit 与 legacy list 失败，分类 `FAIL_FIXTURE_STATE`；fresh database 与 Redis flush 后 PASS。
- 非串行 race attempt 1 的既有 action TTL hard bound 得到 898，预期 899–900；该用例单独 race PASS。attempt 2 中 service、handler、app PASS，但 router 的 business-groups schema unavailable；router 在独立 fresh fixture 下 race PASS。
- 组合并行 fixture 不作为 canonical 证据；仓库 README 要求完整集成使用 `-p 1`，最终 canonical serialized race PASS。
- 受限 sandbox 首次拒绝 `httptest` 的 `[::1]` bind，允许 loopback 的执行环境中复跑 PASS，分类为环境限制而非产品失败。

这些失败没有被重跑结果抹去，也没有被归类为产品通过。最终 PASS 只绑定 fresh、串行、完整记录的门禁。

## 范围与跨仓库状态

`944309003ce47bbaf949f6c0f28d9bd302016d0f..20e51fe90a08f0581d3fdf261a3564deaf392b6b` 的 pre-report changed-file inventory 精确为以下 14 条：

1. `docs/superpowers/plans/2026-09-11-platform-compare-stream-v2.md`
2. `docs/superpowers/specs/2026-09-11-platform-compare-stream-v2-design.md`
3. `internal/app/state.go`
4. `internal/app/state_test.go`
5. `internal/handler/platform.go`
6. `internal/handler/platform_compare_v2_test.go`
7. `internal/handler/platform_single_v2_test.go`
8. `internal/handler/platform_v2_contract_test.go`
9. `internal/service/platform_compare_generation.go`
10. `internal/service/platform_compare_generation_integration_test.go`
11. `internal/service/platform_compare_generation_test.go`
12. `internal/service/platform_generation_store.go`
13. `internal/service/platform_generation_store_redis_test.go`
14. `internal/service/platform_generation_store_test.go`

该 inventory 仅含两份 BE06 plan/design、service/store/compare runner 代码与测试、handler 代码与测试、app state 代码与测试；没有 migration、dependency、deploy 或 frontend 文件。本文所在提交随后只新增本报告并更新 `progress.md`、`feature_list.json`。

历史快照（2026-09-11）：当时配对仓库合同 SHA-256 为 `0891e452f122922f576745db89c96c853a9a7cf4ff00078c30ae3b3f0769e970`，仍为 `v1.0.0` draft，且前端尚无 compare v2、cancel 或 GET recovery integration。该历史状态已由本文顶部的 2026-09-12 增补取代；不得将本段解释为当前状态。

## 未执行事项

本地前后端合同对齐、Porsche-Web 实现和 synthetic 联合验收已经通过。生产迁移与部署、公开 HTTPS、真实账号、付费或真实上游、push、PR 和合入 main 均未执行；因此 `go-018` 继续 `in_progress`，本地 PASS 不能外推为生产发布通过。
