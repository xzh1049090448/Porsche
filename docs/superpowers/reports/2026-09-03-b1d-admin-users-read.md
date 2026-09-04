# B1-D 后端实现及验证记录

当前：2026-09-04，实现候选已落地并冻结；`go-016` 唯一 in_progress，`PARTIAL / PERFORMANCE_BLOCKED`。独立真实 fixture QA 确认功能、安全、隔离、focused/race/fresh full/build/vet/diff均PASS，四级安全问题均0、九生产SHA匹配；但三次相同100k warm性能为633.956ms FAIL、466.753ms PASS、504.793ms FAIL，未稳定满足500ms门禁。资源静止保留，等待用户扩展0004索引验证授权或exact cleanup指令。真正disk-cold、FE-BE联合验收及全PRD仍未执行。

原任务为 B1-D 两个 `/admin/v2/users` 只读接口、三个旧只读兼容入口和 login/refresh/self/me 权限投影。按已确认 r1，实现了规范 query/白名单 DTO、单 CTE count+page、严格 policy 和 actor→target→session→Redis 的自有 READ COMMITTED 事务，提交成功后输出；旧写入口保持原样。列表/详情是 B2 提前交付的子集，不代表 B1 全部底座/写链、M1/A04 或 26 联合用例完成。

生产范围与 SHA256 见同名 validation/manifest.json，共九文件：三个 admin_users service 文件、auth_session.go（仅新增私有成功 refresh proof）、admin_users_read/auth_projection 两 handler 文件、原 admin/auth_users handler 及 router。测试为 service admin_users_read_test/admin_users_read_db_test、handler admin_users_read_test/admin_users_read_performance_test。保留原 B1A–C dirty，无 commit/push/deploy/schema/生产配置访问。

新接口具备 requestID/no-store、固定 detail 错误；旧 middleware 401 语义及 auth envelope 保留。login/refresh 已发行后 fresh identity 401/503 时不给新 cookie/access且不清旧 cookie；仅身份有效、policy 不可用时 200 同时省略两个提示字段。refresh 只有内部成功 Refresh+publish 设置的私有 proof 可读取锁内当前 AV，通用 Actor AV=0 始终拒绝；不改变持久化锁序/恢复窗。新 session 孤留/旧 cookie 已旋转仍是已记录局限。

## Check: 纯 TDD 与攻击边界

Command run（每次都显式清除 TEST_DATABASE_URL、TEST_REDIS_URL、RUN_START_COMMAND、APP_ENV、DATABASE_URL、REDIS_URL、SNOWFLAKE_NODE_ID，GOCACHE=/private/tmp/porsche-go-build-cache）：

```
go test ./internal/service -run AdminUsersRead -count=1
go test ./internal/handler -run 'AdminUsersRead|AdminAuthzHTTPAuthentication' -count=1
go test ./internal/handler -run AuthProjection -count=1
go test ./internal/service -run AdminUsersReadLegacy -count=1
go test ./internal/service ./internal/handler -run 'AdminUsersRead|AuthProjection' -count=1
```

Output observed：初次缺失 ParseAdminUsersReadQuery/ProjectUserRead/RegisterAdminUsersRead/respondIssuedAuth 的预期编译 RED，补实现后对应包 `ok`；legacy 数字溢出回归先实际报 `got 0/50/<nil>`，改 ErrRange 400 后 GREEN。原始 red/green 日志已归档。

Result: PASS（仅已执行纯测试）。攻击边界包括未知/重复/畸形 query、UTF8/长度/数值溢出、LIKE %/_/! 字面转义、稳定排序回退、固定 DTO/null/真实毫秒时间、五个已注册路由认证前 headers，以及发行后错误不泄漏 access/refresh、不改变 cookie。真实 DB TDD 尚未运行，不冒称其 RED/GREEN。

## Check: Fresh 全量无 fixture 回归

Command run:

```
env -u TEST_DATABASE_URL -u TEST_REDIS_URL -u RUN_START_COMMAND -u APP_ENV -u DATABASE_URL -u REDIS_URL -u SNOWFLAKE_NODE_ID -u B1D_RUN_PERFORMANCE GOCACHE=/private/tmp/porsche-go-build-cache go test -p 1 ./... -count=1 -json > /private/tmp/porsche-b1d-full-no-fixture.jsonl 2>&1
```

Output observed：exit 0；test terminal events `pass=386, skip=258, fail=0`，叶子用例 `pass=342, skip=256, fail=0`，package `pass=15, skip=4`（四包无测试）。JSON 全部有效。258 是缺 fixture/opt-in 性能等显式 skip，不能计作通过。

Result: PASS（可运行本地回归）；真实 DB 集成 NOT_RUN。

## Check: Focused race

Command run:

```
env -u TEST_DATABASE_URL -u TEST_REDIS_URL -u RUN_START_COMMAND -u APP_ENV -u DATABASE_URL -u REDIS_URL -u SNOWFLAKE_NODE_ID -u B1D_RUN_PERFORMANCE GOCACHE=/private/tmp/porsche-go-build-cache go test -race ./internal/service ./internal/handler -run 'AdminUsersRead|AuthProjection' -count=1 -json > /private/tmp/porsche-b1d-race-no-fixture.jsonl 2>&1
```

Output observed：exit 0；test `pass=30, skip=29, fail=0`，叶子 `pass=21, skip=29, fail=0`；两包 pass。实际数据库并发测试虽已编译，仍 skip。

Result: PASS（已执行纯 race）；DB race NOT_RUN。

## Check: Build / vet / diff

Command run:

```
env -u TEST_DATABASE_URL -u TEST_REDIS_URL -u RUN_START_COMMAND -u APP_ENV -u DATABASE_URL -u REDIS_URL -u SNOWFLAKE_NODE_ID GOCACHE=/private/tmp/porsche-go-build-cache go build ./...
env -u TEST_DATABASE_URL -u TEST_REDIS_URL -u RUN_START_COMMAND -u APP_ENV -u DATABASE_URL -u REDIS_URL -u SNOWFLAKE_NODE_ID GOCACHE=/private/tmp/porsche-go-build-cache go vet ./...
git diff --check
```

Output observed：三个命令 exit 0、无输出。Result: PASS。

## 真实 fixture 待验证矩阵

新/旧角色拒绝、Admin 显式 users.read deny 的五 URL、Root/Admin 层级隐藏、deleted 额外能力、无 head/空 head/非法 Root rule/orphan 严格性、actor AV/session SV/禁用/删除/过期/撤销/Redis失败、外部事务拒绝、实际事务 rollback 后 commit 失败不得输出 DTO、用户 UPDATE 锁串行 policy head/rules、21目标 NULL/tie 双方向跨页顺序与准确 total、四来源 normal/省略、私有 refresh proof 与 Redis 撤销，均已写测试但无 fixture 时 NOT_RUN。

具体新生命周期计划：validation/2026-09-03-b1d-admin-users-read/fixture-lifecycle-plan.md。Root 已向用户异步请求本批专用授权，未收到前不执行。固定任务 b1d-admin-users-read-260904、库 porsche_b1d_admin_users_260904_test，MySQL8/Redis7 本地 image ID 已只读核对。授权范围须包括现有0001–0003、仅任务容器中 owned 子库、100k 合成数据性能与 exact 清理。

性能测试位于 internal/handler/admin_users_read_performance_test.go：独立 opt-in 100k用户/10并发/page20/warmP95<=500ms；记录硬件与实际首次应用读，明确种数后数据库已暖，绝不称磁盘冷样本。当前 warm 和真正 disk-cold 都 NOT_RUN。旧写/GET logs/alerts/dashboard 权限闭环仍属后续范围。

独立审查归档：2026-09-04 Root 转交 SECURITY_REPORT；fresh身份/权限/事务/CTE/DTO/refresh/cookie静态边界均未发现缺陷。独立真实 MySQL/Redis、锁竞争及100k验证尚未运行，全批继续等待本批 fixture 授权。

## Check: 性能测试响应真实性补强（仅测试，2026-09-04）

Command run：同上显式 unset 环境后，`go test ./internal/service ./internal/handler -run 'AdminUsersRead|AuthProjection' -count=1 -json`，输出 `/private/tmp/porsche-b1d-performance-response-focused.jsonl`。

Output observed：exit0，31 test PASS /29 SKIP /0 FAIL。响应校验 helper 的新增纯测试先 undefined RED，补实现后 GREEN；可拒绝空 items、低于100000的total、错误page/page_size和非规范GUID。真实性能仍 NOT_RUN。

Result: PASS（编译及可运行focused）。唯一代码改动为性能测试：ServeHTTP完成即停计时，之后解析JSON断言items20/total>=100000/page1/page_size20和每项GUID有效，不打印body/token/用户明细。九生产hash不变。未重跑全量/race，本文386/258与30/29为此前生产候选验证结果，新增纯测试不冒充已计入那两份JSON；新日志/测试源hash已入manifest。

VERDICT: PARTIAL

## Check: 新隔离 MySQL/Redis、fresh full 与 100k 性能（2026-09-04）

用户已明确授权同名 validation/fixture-lifecycle-plan.md 的一次性资源。实际创建 task label `codex.task=b1d-admin-users-read-260904`、精确名称的 MySQL 8.0.46 / Redis 7.4.11 容器、loopback 动态端口、tmpfs、AutoRemove、无 named volume；固定 image ID 与计划一致。私有0700目录和0600随机凭据未进入日志或仓库。精确ID、端口和非敏感状态见 `real-fixture/resource-ledger.md`；资源和凭据保留给独立 QA，尚未清理。

现有0001/0002/0003由私有目录中的 `cmd/migrate` 二进制执行，进程仅接收本批 `DATABASE_URL`、`APP_ENV=test`、`SNOWFLAKE_NODE_ID=904`；三项 ledger checksum 与迁移源码完全相同。测试前移除 `APP_ENV/SNOWFLAKE_NODE_ID/DATABASE_URL/REDIS_URL/RUN_START_COMMAND`，只加载本批 `TEST_*`。

Final results：focused service 46 terminal/42 leaf PASS，handler 17/8 PASS；对应 race 同为46/42与17/8，全部0 skip/0 fail。最终 fresh serial full 为691 terminal PASS/1 SKIP/0 FAIL，631 leaf PASS/1 SKIP/0 FAIL，15 package PASS、4个无测试 package SKIP。唯一测试 SKIP 是显式 opt-in 的性能测试，随后独立以 `B1D_RUN_PERFORMANCE=1` 执行并 PASS。build/vet/diff-check均exit0。

100k性能：500/批，10并发×20请求，page20；最终首次应用读96.890ms，warm P95 340.023ms，低于500ms。macOS arm64/Mac15,6、12逻辑核、主机内存19327352832字节；容器无单独CPU/内存limit。种数已经暖库，首次应用读不是磁盘冷缓存，真正 disk-cold 仍 `NOT_RUN`。

真实fixture暴露并修正四个纯测试文件问题，九生产文件未改变且SHA仍逐项匹配冻结值：B1-D含LIKE元字符用户名与性能decimal GUID用户名均超过真实 `VARCHAR(20)`，改为保留攻击字符的短后缀及唯一base36 GUID；两个历史legacy read断言仍期望Admin读取同级/Root为403，按已冻结lower-target隐藏合同改为404。初次RED、共享fixture并包干扰、仅重建MySQL但保留Redis计数的失败均原样保存；最终以MySQL和任务独占Redis同时fresh后通过。404差异另有5 leaf focused PASS，必须由PM和独立QA复核；没有借此改变生产实现。

详细命令、计数、失败历史和脱敏raw输出见 `docs/superpowers/reports/validation/2026-09-03-b1d-admin-users-read/real-fixture/README.md`。本结果不代表FE synthetic已变为真实联调，不完成26联合case、旧管理写/日志/告警/dashboard权限闭环或全PRD。下一门禁为独立QA使用当前静止fixture复跑，之后Root按exact ID/name/label核验并仅停止两容器、删除本批私密文件。

VERDICT: HISTORICAL_WRITER_PASS_PENDING_INDEPENDENT_QA

## Check: 独立真实QA与性能阻断（2026-09-04）

独立 QA 报告 `real-fixture/independent-qa/SECURITY_REPORT.md` 已归档。它重新核对精确容器/标签/镜像/loopback/tmpfs/AutoRemove/无named volume，fresh重建任务库与任务Redis，应用0001–0003，并独立执行相同门禁。功能结果：focused service46 terminal/42 leaf、handler17/8及相同race均PASS；fresh serial full691 terminal PASS/1 opt-in性能SKIP/0FAIL、631 leaf PASS/1SKIP、15 package PASS/4无测试package SKIP；build/vet/diff PASS。两处403→404测试变更与冻结lower-target隐藏合同一致。安全Critical/High/Medium/Low均0，九生产hash再次匹配，证据扫描未发现凭据。

性能总体FAIL：三次各自fresh、100000合成用户、10并发×20请求、page20的warm P95依次为633.956ms（FAIL）、466.753ms（PASS）、504.793ms（FAIL）。一次偶发PASS不能覆盖两次FAIL，门禁要求稳定通过。seed已经暖库，true disk-cold仍NOT_RUN。

根因证据：exact count 选择 `idx_users_active_updated`，在 `is_deleted=0` 后继续过滤role/status，扫描100001行；独立 `EXPLAIN ANALYZE` 的count部分约312ms，而page部分约0.09ms。高并发重复exact count扫描解释warm P95波动。该问题是发布/验收性能阻断，不是权限或凭据漏洞，也不推翻功能、安全、隔离PASS。

后端PM推荐以新forward migration 0004 `idx_users_admin_read_count(is_deleted,role,status)`处理，初始查询不变；optimizer仍选择错误时才另行审核hint。详细实施、三次fresh验收、索引大小/构建/写成本及未来0005回滚要求见 `docs/superpowers/plans/2026-09-04-b1d-admin-users-count-index-plan.md`。0004、额外fixture测试与生产均未获当前授权；不得修改既有迁移或降低500ms门禁。

现有精确MySQL/Redis资源及私密文件继续保留且静止，不在文档记录私密路径。下一步只能是用户扩展授权后实施/验证0004，或Root收到cleanup指令后按exact ID/name/label清理。

VERDICT: PARTIAL_PERFORMANCE_BLOCKED

## Check: 0004 count索引扩展授权与optimizer停点（2026-09-04）

用户已扩展授权0004实现、保留fixture应用/验证、三次fresh性能、独立QA与最终exact cleanup；生产迁移未授权。严格TDD先以缺失0004的runner/schema测试RED，再新增不可变forward migration `0004_admin_users_read_count.up.sql` 创建 `idx_users_admin_read_count(is_deleted,role,status)`，runner依次embed/apply/verify/checksum。0001–0003、API、DTO、exact total query及500ms门禁均未修改；撤销只能另建未来0005。

真实fixture fresh migration ledger确认0001–0004，0004 checksum为`44b5caba0473162c239e6b3035e6d9067e3af0b35e998a79c827a1424621494e`；`SHOW INDEX`确认非唯一三列顺序正确。初次真实verifier因GORM struct scan不匹配在索引建成后报contract mismatch，失败原样保留；窄修为rows direct scan后fresh通过。migration focused20、service focused/race各46、handler focused/race各17均PASS/0FAIL/0SKIP；fresh serial full692PASS/1 opt-in性能SKIP/0FAIL，15 package PASS、4 no-test package SKIP；build/vet/diff exit0。

第1次独立fresh性能为first90.669ms、warm P95470.646ms，单次数值PASS。但同一次fresh `EXPLAIN ANALYZE` 的exact count仍选择旧`idx_users_active_updated`，扫描100001行、actual约203ms，未选择0004新索引；新索引当时三列cardinality均为1。page继续使用`uk_users_guid`且约0.12ms，总量保持100000匹配/100001含fixture actor。

计划明确规定optimizer仍未选择新索引时停止并交PM另审。因此未执行第2/3次fresh，不加hint、不擅自刷新统计或调整索引；索引size、100k populated构建成本和insert/update写成本也因停点标为`NOT_RUN_BY_PRESET_OPTIMIZER_STOP`。历史独立三次633.956/466.753/504.793ms继续有效，不被本次一次470.646ms覆盖。详细脱敏证据见`real-fixture/count-index/README.md`。

资源静止保留等待PM决定与独立QA；未cleanup。`go-016`仍`in_progress / PERFORMANCE_BLOCKED / OPTIMIZER_REVIEW_REQUIRED`。

VERDICT: PARTIAL_PERFORMANCE_BLOCKED_OPTIMIZER_REVIEW_REQUIRED

## Check: PM批准ANALYZE后仍选择旧索引（2026-09-04）

PM在用户已授权的fixture-only 0004性能整改范围内批准对当前100k隔离fixture执行一次`ANALYZE TABLE users`，要求仍不改索引/代码、不加hint；若分析后仍选择旧索引则再次停点。

执行返回OK，墙钟0.10s；总量前后保持100001行、匹配100000行。新`idx_users_admin_read_count`三列估算cardinality前后仍均为1。相同exact count在ANALYZE前选择`idx_users_active_updated`扫描100001行、约106ms，ANALYZE后仍选择同一旧索引扫描100001行、约148ms；page继续选择`uk_users_guid`，分析后约0.054ms。

因此第二个停点已触发：未执行新的三次fresh性能、hint、索引修改、索引size或insert/update/软删除成本采样。资源在ANALYZE与只读采证后静止保留，等待PM的索引/查询设计新范围及独立QA；历史失败与单次470.646ms结果均继续保留。

VERDICT: PARTIAL_PERFORMANCE_BLOCKED_INDEX_DESIGN_REVIEW_REQUIRED

## Check: count-local H1/H2只读实验（2026-09-04）

PM批准在当前fixture保存B0，并以完全相同WHERE做count-local H1 `USE INDEX FOR JOIN (idx_users_admin_read_count)`；filtered/paged保持无hint，只有H1不选新索引时才做H2 `FORCE INDEX`。本阶段不改源码、索引或DB。

B0、H1、H2完整statement均返回total100000及相同20个降序GUID；page始终使用`uk_users_guid`。完整CTE EXPLAIN把counted折叠为执行前标量，因此另存相同count子查询的直接EXPLAIN：B0使用`idx_users_active_updated`扫描100001/输出100000行，约112ms；H1没有使用新索引而改为table scan100001行，约37.9ms；H2按预授权执行并强制选择`idx_users_admin_read_count`，但range scan输出100000行、约184ms，比B0/H1更慢。

当前100k合成数据几乎全部为未删除、user、active，0004前三列缺少选择性；单次EXPLAIN也不能替代10×20并发P95。H1未满足选择新索引判据，H2性能倒退，因此均不具备落生产依据，三次fresh仍未启动。

动态谓词存在合同风险：当前count通过filtered复用同一个`where,args`。若count直接查询users，SQL文本虽可由同一where变量生成两次，placeholder参数必须复制；deleted、role/status、两个escaped LIKE及可选exact GUID OR都可能造成total/page谓词或参数顺序漂移。后续方案必须保持单一谓词构建来源并覆盖全部组合测试。

VERDICT: PARTIAL_PERFORMANCE_BLOCKED_QUERY_AND_INDEX_REDESIGN_REQUIRED

## Check: count-local H3同源谓词只读实验（2026-09-04）

PM批准H3只读实验：counted加入`IGNORE INDEX FOR JOIN (idx_users_active_updated)`，filtered/paged原样。实验使用临时Go overlay直接调用现有`usersReadWhere`一次生成P和args；H3只复制args切片以对应同一P出现两次，没有手写第二套谓词，也没有修改源码、索引或DB。overlay提取源码和初次未export TEST变量的明确SKIP日志均归档。

B0/H3完整statement均返回total100000及完全相同20个降序GUID；page仍选择`uk_users_guid`，约0.067ms。direct count EXPLAIN：B0选择`idx_users_active_updated`、读取100001/输出100000、约77.2ms；H3选择`PRIMARY` range scan、读取100001/输出100000、约30.5ms。7组交替只读墙钟B0 min/median/mean/max为65.115/67.241/68.572/75.007ms，H3为23.317/24.042/26.938/33.421ms，每次total都为100000。

该方向在当前默认无搜索100k fixture内重复改善，但不是10×20 HTTP并发P95，也没有覆盖deleted、role/status、escaped LIKE及可选exact GUID OR。任何实现必须继续从单一`usersReadWhere`结果生成两处P并复制参数，先做全部动态组合的total/page等价测试，再做三次fresh门禁。本阶段在只读证据后停止。

VERDICT: PARTIAL_PERFORMANCE_BLOCKED_H3_PROMISING_PENDING_QUERY_REVIEW

## Check: H3本地候选实现、三次fresh性能与成本（2026-09-04）

PM确认H3证据满足本地候选实现门槛。严格TDD先新增缺helper时compile RED的结构测试，再以最小改动让`usersReadWhere`仅生成一次P，filtered/count直接复用；counted为直接users `IGNORE INDEX FOR JOIN (idx_users_active_updated)`，args严格复制为P/P/limit/offset，paged仍来自filtered，保持单statement snapshot。0004不变。

纯/真实测试覆盖active/disabled/deleted、role/status、escaped LIKE、可选GUID OR、四排序双方向、空结果/空页与total-items等价。focused service46/42 leaf；handler18/9 leaf加1性能opt-in SKIP；对应race相同，全部0FAIL。最终fresh full693 terminal/633 leaf PASS、1性能SKIP、0FAIL，15 package PASS、4 no-test package SKIP；build/vet/diff exit0。

三次各自fresh reset→0001–0004→seed100k→ANALYZE→10×20/page20 benchmark均PASS：ANALYZE 4.652/4.480/4.652ms，first35.218/35.101/36.317ms，warm P9577.817/75.512/74.710ms。第3轮EXPLAIN再次显示H3选择PRIMARY约30.0ms、B0旧索引约70.9ms，page保持`uk_users_guid`约0.051ms，总量100000。disk-cold仍NOT_RUN。

100001行clone的0004在线构建墙钟0.16s；users索引size约2.64MB。10k insert有/无索引0.19/0.17s，5k软删除0.13/0.12s，5k status+role更新0.13/0.09s；单次临时表样本只表明写放大方向，三个临时表已删除。

当前为`WRITER_PASS_PENDING_INDEPENDENT_QA_AND_EXACT_CLEANUP`。无生产迁移、commit/push/deploy；资源保留。

VERDICT: WRITER_PASS_PENDING_INDEPENDENT_QA_AND_EXACT_CLEANUP

## Check: H3独立QA PASS与exact cleanup完成（2026-09-04）

独立QA H3最终PASS：migration/service focused48/44 leaf、handler17/8、service race47/43、handler race17/8、fresh full693 terminal/633 leaf PASS加1性能SKIP/0FAIL；build/vet/diff PASS，安全四级问题均0。独立三次各自fresh ANALYZE+100k性能P95为113.688/76.997/78.737ms，全部低于500ms；H3继续选择PRIMARY约29.4ms，page `uk_users_guid`约0.05ms，总量100000。

用户已授权的exact cleanup随后完成。先核对两个完整ID、精确名称、task label、AutoRemove、tmpfs、计划内只读私密bind及named volume为0；同label inventory恰为两个预期ID。首次过严预检因把计划内bind误当作应为空而安全停止，未改变资源；按原生命周期计划修正验证后，仅停止两个精确ID。AutoRemove后ID/名称均不存在、同label残留0；仅删除精确私密目录并验证不存在。未prune、未删volume、未碰其它容器。

`go-016`最终为`passing / PASS_LIMITED_SCOPE`。本结果不包含disk-cold、FE-BE、26联合用例、旧写链、其它管理域或生产迁移/部署。

VERDICT: PASS_LIMITED_SCOPE
