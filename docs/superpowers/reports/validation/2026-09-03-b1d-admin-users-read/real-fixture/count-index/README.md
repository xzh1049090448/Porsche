# B1-D 0004 count 索引隔离 fixture 结果

日期：2026-09-04。最终结论：`PASS_LIMITED_SCOPE / INDEPENDENT_QA_PASS / EXACT_CLEANUP_COMPLETE`。0004仅应用到任务隔离fixture；最终本地候选采用count-local H3避开旧索引，writer和独立QA的三次fresh warm P95均通过。没有生产迁移、commit、push或deploy。

## 实现与 TDD

- RED：先增加 runner/schema 契约测试，证明缺少0004；原始输出为 `tdd-red.log`。
- GREEN：新增不可变 forward migration `0004_admin_users_read_count.up.sql`，创建非唯一索引 `idx_users_admin_read_count(is_deleted, role, status)`，使用 MySQL 8 `ALGORITHM=INPLACE LOCK=NONE`。down 文件只记录必须由未来0005 forward migration撤销，0001–0003未修改。
- runner 按0001、0002、0003、0004顺序 embed/apply/verify/checksum。真实 ledger 中0004 checksum为 `44b5caba0473162c239e6b3035e6d9067e3af0b35e998a79c827a1424621494e`。
- 首次真实运行暴露 verifier 的 GORM struct scan 不匹配；索引已建但 verifier 返回 contract mismatch，原样保留在 `migration-up-initial-verifier-scan-fail.log` 与 `migration-focused-initial-verifier-fail.jsonl`。修复仅改为 rows direct scan，再 fresh 重建并通过；没有修改索引、API、DTO、query或500ms阈值。

## 结构与回归

- `SHOW INDEX`：索引存在，列序1=`is_deleted`、2=`role`、3=`status`，均为非唯一；DDL后查询到的 pending metadata locks 为0。空库迁移总墙钟约1158.284ms，这不是100k生产式在线构建成本。
- migration focused：20 terminal/18 leaf PASS、0 FAIL、0 SKIP。
- service focused/race：各46 PASS、0 FAIL、0 SKIP；handler focused/race：各17 PASS、0 FAIL、0 SKIP。
- fresh serial full：692 terminal/632 leaf PASS、1 opt-in performance SKIP、0 FAIL；其中 package 15 PASS、4 no-test SKIP。build、vet、diff-check均exit0。

## 性能与预设停点

第1次按独立 fresh MySQL+Redis、0001–0004、100000合成用户、500/批、10并发×20请求、page20执行：首次应用读90.669ms，warm P95 470.646ms，数值 PASS。seed已经暖库；true disk-cold仍 `NOT_RUN`。

但该次 fresh `EXPLAIN ANALYZE` 显示 exact count **仍选择 `idx_users_active_updated`**，扫描100001行，actual约203ms；新索引的三列统计 cardinality 当时均为1。page继续合理使用倒序 `uk_users_guid`，约0.12ms；总量保持100000匹配/100001含fixture actor。

实施计划明确规定：0004存在后若 optimizer 仍不选择新索引，必须停止并提交PM另审，不得擅自加hint。因此：

- 第2、3次 fresh性能：`NOT_RUN_BY_PRESET_OPTIMIZER_STOP`；不能把一次PASS写成三次稳定PASS。
- `ANALYZE TABLE`、query hint、索引列调整：`NOT_RUN / REQUIRES_PM_DECISION_AND_SEPARATE_SCOPE`。
- populated 100k 索引构建成本、per-index size、insert/update 写入成本：`NOT_RUN_BY_PRESET_OPTIMIZER_STOP`，避免在停点后继续改变fixture。
- 独立QA与exact cleanup：尚未执行；资源静止保留。

历史独立QA三次633.956ms FAIL、466.753ms PASS、504.793ms FAIL全部保留，不被本次单次470.646ms覆盖。`go-016`继续`in_progress / PERFORMANCE_BLOCKED`。

## PM批准的 ANALYZE 复核

PM在同一fixture-only授权范围内批准先对当前100k fixture执行`ANALYZE TABLE users`，仍不改索引/代码且不加hint。执行前后保存同一总量、SHOW INDEX及count/page `EXPLAIN ANALYZE`：

- `ANALYZE TABLE users`返回OK，主机观察墙钟0.10s。
- 总量前后均为100001行，其中100000行匹配列表条件。
- `idx_users_admin_read_count`的`is_deleted/role/status`估算cardinality前后仍均为1。
- exact count执行前仍选`idx_users_active_updated`，扫描100001行、actual约106ms；执行后仍选同一旧索引，扫描100001行、actual约148ms。
- page前后继续合理选择`uk_users_guid`，执行后约0.054ms。

这触发PM设定的第二个停点。因此没有执行新的三次fresh性能、hint、索引修改或写/软删除成本采样；它们继续标为`NOT_RUN_AFTER_ANALYZE_STILL_SELECTED_OLD_INDEX`。fixture在ANALYZE及只读采证后再次静止，等待索引设计/查询设计的独立审查。

## PM批准的只读 H1/H2 实验

PM随后批准只读实验：B0保持当前`counted AS (SELECT COUNT(*) FROM filtered)`；H1只将counted改为直接从`users USE INDEX FOR JOIN (idx_users_admin_read_count)`读取，使用与filtered完全相同的WHERE；filtered/paged不加hint。只有H1未选择新索引时才允许H2 count-local `FORCE INDEX`。本阶段没有修改源码、索引或DB。

- B0、H1和H2的完整statement输出一致：total均为100000，page均为相同20个降序GUID。
- 完整statement的EXPLAIN把counted折叠为`Rows fetched before execution`，因此另保存完全相同count子查询的直接EXPLAIN以看清chosen access path。
- B0 direct count：选择`idx_users_active_updated`，读取100001行、输出100000行，actual约112ms。
- H1 `USE INDEX FOR JOIN`：**未选择新索引**，改为table scan100001行、输出100000行，actual约37.9ms。H1虽比该次B0快，但它不满足“选择新索引”的实验判据，且单次EXPLAIN不能替代并发P95门禁。
- 因H1未选新索引，按预授权执行H2。H2 `FORCE INDEX FOR JOIN`确实选择`idx_users_admin_read_count`，range scan输出100000行，actual约184ms，比同批B0和H1都慢。
- 三种完整statement的page都保持无hint并使用`uk_users_guid`；H1 page约0.086ms，H2 page约0.139ms。

该100k合成分布几乎全部为未删除、user、active，新索引的前三列对count过滤没有选择性；本实验不能推断生产混合分布表现。H1/H2都不支持直接落生产：H1没有选择新索引，H2强制后更慢。

实现风险：当前生产查询只生成一次动态`where,args`，count通过filtered自然复用同一集合。若将counted改成直接users查询，即便SQL文本由同一个where变量拼入两次，绑定参数也必须按出现顺序复制一份。`deleted`切换`is_deleted`语义，role/status可增加等值谓词，搜索会增加两个escaped LIKE以及可选exact GUID OR；任何文本或参数复制漂移都会让total与page集合不同。下一设计必须以单一谓词构建结果同时生成两组绑定参数，并覆盖无筛选、deleted、role、status、LIKE转义和可解析/不可解析GUID组合，不能手写第二套条件。

## PM批准的只读 H3 实验

H3只在counted中加入`IGNORE INDEX FOR JOIN (idx_users_active_updated)`，filtered/paged保持当前结构。为满足“谓词必须来自现有生成结果、不可手写”约束，实验通过临时Go overlay置于`service`包内，直接调用生产`usersReadWhere(actor, q)`一次取得`where,predicateArgs`；B0复用一次，H3把同一args切片复制一份以匹配同一where出现两次。overlay未修改工作树源码，提取后的实验源码保存为`h3-overlay-test.go.txt`。

首次overlay命令因shell source没有自动export `TEST_DATABASE_URL`而明确SKIP，保存在`h3-ignore-active-updated-overlay-initial-env-not-exported.log`；随后仅修正环境导出后执行，测试PASS：

- 生产生成的P为`id <> ? AND guid <> ? AND role IN (?) AND is_deleted = 0 AND status IN (?,?)`，arg count 5；实验没有手写第二套P。
- B0/H3完整statement均为total100000及完全相同的20个降序GUID。
- page保持无hint并选择`uk_users_guid`，读取21/输出20行，actual约0.067ms。
- B0 direct count选择`idx_users_active_updated`，读取100001/输出100000行，EXPLAIN actual约77.2ms。
- H3避开旧索引后选择`PRIMARY` range scan，读取100001/输出100000行，EXPLAIN actual约30.5ms；没有选择0004。
- 7组交替只读墙钟：B0 min/median/mean/max=`65.115/67.241/68.572/75.007ms`；H3=`23.317/24.042/26.938/33.421ms`。每次total均为100000，方向在本fixture内重复成立。

H3是当前最有希望的只读方向，但这仍不是10×20 HTTP并发P95，也只覆盖默认无搜索/未删除筛选。它不授权生产实现；在修改查询前仍需对deleted、role/status、escaped LIKE及可选exact GUID OR做同源谓词/参数复制等价测试，再执行三次各自fresh完整性能门禁。

## H3本地候选实现与TDD

用户既有fixture-only性能整改授权下，PM确认H3证据满足进入本地候选实现。严格TDD先增加`TestAdminUsersListStatementReusesPredicateAndArguments`，缺少helper时compile RED；随后最小实现`adminUsersListStatement`：

- `usersReadWhere`只调用一次，filtered与counted拼接同一个P；
- counted直接查询`users IGNORE INDEX FOR JOIN (idx_users_active_updated)`；
- args严格为P args、P args副本、limit、offset；
- paged继续来自filtered，完整count/page仍在一个MySQL statement snapshot；
- 0004、API、DTO、排序、exact total和500ms门禁均未修改。

纯测试覆盖active/disabled/deleted、role/status、两个escaped LIKE、可选exact GUID OR、四种排序及asc/desc、参数复制与limit/offset顺序。真实DB用例另覆盖active/disabled结合role/status、空结果与空页仍保留total/items等价；既有deleted、literal LIKE、numeric GUID、四排序和NULL跨页测试全部复跑。

验证结果：service focused46 terminal/42 leaf PASS；handler focused18 terminal/9 leaf PASS、1项opt-in性能SKIP；对应race计数相同；均0FAIL。最终fresh serial full为693 terminal/633 leaf PASS、1项性能opt-in SKIP、0FAIL；15 package PASS、4 no-test package SKIP。build/vet/diff均exit0。

## 三次独立fresh性能

每轮均独立执行任务DB+Redis reset、0001–0004、root fixture、100000用户500/批seed、`ANALYZE TABLE users`，再做首次应用读与10并发×20请求/page20 benchmark：

| run | ANALYZE ms | first ms | warm P95 ms | result |
| --- | ---: | ---: | ---: | --- |
| 1 | 4.652 | 35.218 | 77.817 | PASS |
| 2 | 4.480 | 35.101 | 75.512 | PASS |
| 3 | 4.652 | 36.317 | 74.710 | PASS |

三次均远低于500ms且没有覆盖/重跑失败值。seed与ANALYZE已经暖库，true disk-cold仍`NOT_RUN`。第3轮后同源只读EXPLAIN再次确认H3 total100000及相同20 GUID；B0 count约70.9ms，H3 PRIMARY约30.0ms，page `uk_users_guid`约0.051ms。

## 0004成本样本

成本仅来自当前Mac/Docker临时表单样本，不能外推生产：

- 100001行clone的0004索引在线构建墙钟0.16s；构建后pending metadata lock观测为0，但没有并发锁等待采样。
- 原users索引统计size约2,637,824 bytes、leaf约2,588,672 bytes；clone为3,686,400/2,572,288 bytes。差异说明采样/页填充会影响体积。
- 10k insert：有0004为0.19s、无0004为0.17s；5k软删除0.13/0.12s；5k status+role更新0.13/0.09s。单次主机墙钟分辨率较粗，只能说明该索引会增加写放大，不能作为容量承诺。
- 三个精确临时表均已删除；结果核对两组各10000行、5000软删除、5000 status/role变更。

本地writer门禁已通过，但独立QA尚未复跑H3动态谓词与三轮fresh；资源保留且不cleanup。

## 独立QA与exact cleanup

独立报告`real-fixture/independent-qa-h3/SECURITY_REPORT.md`最终PASS：动态合同、focused/race/fresh full/build/vet/diff均通过，安全Critical/High/Medium/Low均0；独立三次fresh P95为113.688/76.997/78.737ms，均低于500ms。disk-cold、FE-BE、26联合用例和生产仍不在本限定PASS内。

随后按用户已授权的exact cleanup执行：

- 先精确核对完整MySQL/Redis ID、名称、task label、AutoRemove、计划内tmpfs与只读任务私密bind；named volume为0，同label inventory恰为这两个ID。
- 首次预检脚本因错误假设“无任何mount”安全停止，未执行stop/delete；读取实际字段后与原生命周期计划中的只读私密bind完全一致，才继续。
- 仅`docker stop`两个精确ID，AutoRemove后验证两个ID及名称均不存在、同label残留为0；没有prune、volume rm或其它容器操作。
- 仅删除精确私密目录，未使用glob；验证路径不存在。

脱敏证据：`exact-cleanup-precheck.txt`、`exact-cleanup-stop.txt`、`exact-cleanup-container-verification.txt`、`exact-cleanup-private-path-verification.txt`。`go-016`可标记`passing / PASS_LIMITED_SCOPE`。

## 证据索引

- `tdd-red.log`、`tdd-green-pure.log`、`tdd-green-pure-2.log`
- `migration-up.log`、`perf-1-migration.log`、`show-index-and-locks.txt`
- `migration-focused.jsonl`、`focused-service.jsonl`、`focused-handler.jsonl`、`race-service.jsonl`、`race-handler.jsonl`、`full.jsonl`
- `build.log`、`vet.log`、`diff-check.log`
- `perf-1.log`、`perf-1.exit`、`perf-1-explain.txt`
- `analyze-current-before.txt`、`analyze-current.log`、`analyze-current-time.txt`、`analyze-current-after.txt`
- `h1-use-index-output-and-explain.txt`、`h1-use-index-count-explain.txt`
- `h2-force-index-output-and-explain.txt`
- `h3-overlay-test.go.txt`、`h3-ignore-active-updated-overlay.log`
- `h3-ignore-active-updated-overlay-initial-env-not-exported.log`
- `tdd-h3-red.log`、`tdd-h3-green-pure.log`
- `h3-focused-service.jsonl`、`h3-focused-handler.jsonl`、`h3-race-service.jsonl`、`h3-race-handler.jsonl`
- `h3-perf-{1,2,3}.log`及各自reset/migration/exit证据
- `h3-post-performance-explain.log`、`h3-cost-*`
- `h3-final-full.jsonl`、`h3-build.log`、`h3-vet.log`、`h3-diff-check.log`

凭据内容与私密路径未进入证据。精确容器ID、镜像ID、任务label继续沿用父级manifest的fixture ledger；资源等待独立QA或Root exact cleanup指令。
