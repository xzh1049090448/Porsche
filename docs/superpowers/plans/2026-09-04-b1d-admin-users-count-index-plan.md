# B1-D 管理用户 exact count 索引实施计划

状态：`PASS_LIMITED_SCOPE / INDEPENDENT_QA_PASS / EXACT_CLEANUP_COMPLETE`。H3已按TDD实现并通过writer与独立QA动态合同、回归和各三次fresh性能；两个精确容器与私密目录已按授权清理。生产迁移始终未授权。

## 问题与边界

独立 QA 对相同的 100000 合成用户、10 并发、每 worker 20 请求、page size 20 做了三次 fresh warm 测量：P95 分别为 633.956ms（FAIL）、466.753ms（PASS）和 504.793ms（FAIL），500ms 门禁不稳定。精确查询拆解显示 page 部分约 0.09ms；exact total 的 count 部分使用 `idx_users_active_updated`，在 `is_deleted=0` 后继续过滤 `role/status`，扫描 100001 行并耗时约312ms。因此功能、安全与集成验证继续 PASS，整体为 `PARTIAL / PERFORMANCE_BLOCKED`。

本批只处理现有 B1-D 列表 exact-count 路径的索引验证。不改变 API、DTO、角色/状态语义、exact total、CTE一致性、分页、排序、错误或权限合同；不降低500ms门槛，不改性能断言，不接前端、旧写入口或生产部署。

## 实施方案

1. 新增不可变 forward migration `0004`，创建：

   ```sql
   CREATE INDEX idx_users_admin_read_count
     ON users (is_deleted, role, status);
   ```

2. 将0004按现有显式 migration runner 的版本顺序接入，并增加 runner/checksum/schema验证测试。不得修改、覆盖或重新计算既有0001、0002、0003。
3. 在新的明确授权范围内，将0004仅应用到保留的任务隔离 MySQL fixture；用 `SHOW INDEX FROM users` 核对名称、列顺序、唯一性和实际存在，用 migration status 核对0001–0004及各自checksum。
4. 初始查询实现保持不变，先让 MySQL optimizer 自行选择 `idx_users_admin_read_count`。保存 exact count 与完整列表查询的 `EXPLAIN ANALYZE`，确认扫描行数、chosen key、count/page耗时及结果总数不变。
5. 只有新索引存在且 optimizer 仍选择错误索引、并有 fresh `EXPLAIN ANALYZE` 证据时，才另行提交 query hint 设计审查；本计划不预先加入、实现或授权 hint。

## TDD 与验证

先添加会在0004缺失时 RED 的 migration/schema测试，再实现 runner/SQL并 GREEN。验证顺序串行执行，数据库与 Redis 必须同时 fresh，所有测试仍只使用显式私有 `TEST_DATABASE_URL` / `TEST_REDIS_URL`：

1. migration focused、runner checksum/schema tests，以及真实 `SHOW INDEX` 列顺序验证。
2. B1-D service/handler focused。
3. 对应 service/handler race。
4. fresh serial full `go test -p 1 ./... -count=1 -json`。
5. `go build ./...`、`go vet ./...`、`git diff --check`。
6. 在完全相同的100000合成用户、10×20请求、page20条件下，执行**三次各自 fresh** 的性能测试；三次 warm P95 必须全部 `<=500ms`，任一次超过即保持 `PERFORMANCE_BLOCKED`。记录首次应用读，明确 seed 已暖库，disk-cold仍单独标识。
7. 每次性能运行保存 exact count 和完整列表的 `EXPLAIN ANALYZE`、实际总数与索引选择，确认功能结果和一致性合同没有变化。

独立 QA 必须复算生产/迁移/test hashes，复核安全与证据脱敏，并独立重跑三次 fresh 性能门禁。PM 在证据完成后复核0004合同与查询计划。

## 成本与风险证据

报告必须记录：

- `SHOW INDEX` 和 `EXPLAIN ANALYZE` 前后差异、扫描行数与耗时；
- `idx_users_admin_read_count` 的索引大小、构建时间，以及 migration 持锁/阻塞窗口；
- 100k fixture 上新增索引前后的 insert/update 写入成本，重点覆盖 `is_deleted`、`role`、`status` 变更；
- MySQL/Docker/主机规格和每次 fresh/warm状态；
- 额外磁盘、buffer pool、写放大、部署窗口及失败恢复风险。

索引建表可能消耗I/O、磁盘和DDL窗口；生产应用0004必须另获生产迁移授权并具备经审核的维护窗口、备份与监控。当前隔离 fixture 授权不包含0004、额外性能运行或生产。

## 回滚与授权

已应用迁移不可原地修改或删除。若0004进入任何共享/生产环境后需要撤销，必须另行设计、审核并执行 forward migration `0005` 删除 `idx_users_admin_read_count`；不得把编辑0004、删除migration ledger或直接执行未登记DDL作为回滚。

用户随后已对创建0004实现与测试、在保留隔离fixture应用/验证、三次fresh性能、独立QA窗口及最终exact cleanup作出扩展授权。执行在第1次fresh EXPLAIN触发optimizer停点后暂停；该授权不自动扩大为统计刷新、索引重设计或query hint。生产迁移始终单独授权且本批未获授权。
