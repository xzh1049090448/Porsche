# Porsche 开发进度

## 2026-09-08：A03 创建用户/管理员本地联合切片限定通过

- A03 仅本地联合切片由 `BLOCKED_NOT_IMPLEMENTED` 提升为 `PASS_LIMITED_SCOPE`；代码候选为后端 `3a50144e53268f6ef3ae704699ef9fa851e4a5ee`、前端 `9f660a9ca26f5738dd661652596a1e450ff34335`。Admin 仅可按 immutable omitted default 创建普通用户；Root 可创建普通用户/管理员并保存 allow/deny 覆盖。删除后重放返回稳定 410 `created_user_deleted` 且无 PII，`operation_expired` 与之区分。
- 隔离环境为 MySQL 8.4.11、Redis 7.4，迁移账本 `0001`–`0010`。focused 为 673 terminal/610 leaf、race 为 476 terminal/427 leaf；serial full 为 1912 terminal PASS/1 SKIP、1739 leaf PASS/1 SKIP。唯一 skip 是显式 opt-in 的 `TestAdminUsersReadPerformance` 100k 性能夹具。前端 275/275、可见 Chrome 13/13、build/vet/diff 和三项代码/证据复审均通过。
- `ACTION_SECURITY_HMAC_KEY` v1 没有 key ID/多 key verifier；仍有可重放的 post-0010 active snapshot 时禁止轮换，除非先交付单独批准的多 key 验证或原子全量 re-HMAC migration。
- A03 专用 MySQL/Redis、私有环境/临时脚本已 exact cleanup，任务容器、标签、监听、PID、命名卷及私有文件残留均为 0；无关容器、镜像和卷保持不变。证据：`docs/superpowers/reports/validation/2026-09-06-a03-admin-user-create/canonical-3a50144-9f660a9-final/manifest.json`。
- 本次不改变 A14 及其余 tracker 行。金额余额仍为 Mock 边界；真实 ledger/billing/recharge/refund/deduction、生产迁移、部署、生产验收和真实业务账号均 `NOT_RUN`。

## 2026-09-06：A12/A14 users.delete 本地联合切片验收通过

- 仅 `users.delete` 切片在后端 `811213d557eea7b6b9523a584245252ba4dd7d80`、前端 `bace6d167b94b693abd6be4c720152dc0eb905bb` 获得三项独立复审 PASS 与本地联合验收 `PASS_LIMITED_SCOPE`；A12 只覆盖软删除、重复删除、用户名不可复用及凭据失效，不覆盖恢复写链；A14 只覆盖该动作的 ticket/idempotency/operation Query 与依赖失败关闭。
- 证据位于 `docs/superpowers/reports/validation/2026-09-05-a14-user-soft-delete/`：可见真实 UI 三种成功角色路径、UI eligibility、真实 API-context denial/replay、敏感状态生命周期、删除后 15 项凭据拒绝及三组脱敏数据库终态均通过；spec/quality/security re-review 均为 `PASS`。
- 生产 `ActiveActionRegistry()` 仅激活 `users.delete`；其余 7 个管理动作继续 inactive。通用 outbox delivery/recovery worker 仍在切片外，其他联合验收 blocker、P08 产品阻塞、R02 环境阻塞及旧历史均不变。本结论不代表完整 A12、完整 A14、完整 PRD 或 release 验收通过。
- 本批仅在隔离 loopback MySQL/Redis fixture 执行现有迁移与验证。生产迁移、部署、push、真实业务数据操作均未获授权，也未执行。
- 更新前基线：后端 `progress.md` SHA-256 `fbbbd89bcd1283a73261e7e7948a0307b9e44f07ed9e4973c8962c836459785f`；前端 acceptance matrix SHA-256 `6925173b045e77362b8fc096d68727d602cd261545b24e8f38d60e991f93a241`；前端 `progress.md` SHA-256 `21eeb06bc3608554c3bf1ba595897a98c26e4e26b964d6cf2fdd4d66577605cb`。最终 bounded diff 只允许 A12/A14、对应汇总/时间线、两份 progress 和本验证目录发生变化。

## 2026-09-05：B1-E 管理动作安全底座限定子范围通过，A14仍阻塞

- `go-017` 为 `passing / limited_subscope`。Task 1–11 已交付 strict external-value、用途隔离密钥、8 个 inactive typed descriptor、canonical intent、0005 schema/verifier、Redis Lua 限流、Issue/Begin/Query/Execute/lease/recovery/transaction primitives；生产 `ActiveActionRegistry()` 为空，active production consumer 为 0，冻结路由继续 404。
- Task 12 第三次独立 QA 对 `2408ee3ace6f1a2ff07645653aed354a0ca245fa` 给出 `QA_PASS`：migration leaf 1；service 32 pass events / 27 leaf；并发 2/2；serial full 1120 pass events / 1017 leaf / 1 显式性能 SKIP / 0 FAIL；Action race 267 pass events / 237 leaf / 0 FAIL；route registry 15/13、router 24/22；build/vet/diff/source/secret 全部 PASS。
- 历史首次 QA_FAIL、第二次 QA_PASS_PRE_SPEC_FAIL、随后 SPEC_FAIL、5 点 Execute fault 修复、曾报告提交被 amend 替代的过程偏差，以及当前 14 类、19 份含 fail action 的 raw JSON、145 个 fail action 事件均保留。Task 12 在归档时保留的 fixture 后由 Task 14 精确清理；`b18d22ef8d6683bdf6ef55a7369180b72cb75479` 记录 `CLEANUP_PASS`、任务残留全0及无关资源不变。
- 本结论不完成 A14；A14 保持 `BLOCKED_NOT_IMPLEMENTED`。联合验收 18 个 blocker 逐项保持：A03/A05–A12/A14/P01/P03–P07 为 `BLOCKED_NOT_IMPLEMENTED`，P08 为 `BLOCKED_PRODUCT`，R02 为 `BLOCKED_ENV`。
- 无真实业务动作、真实审计投递、生产 outbox worker、recovery worker、前端 adapter、部署、生产迁移或生产验收；未改 Porsche-Web。报告：`docs/superpowers/reports/2026-09-04-b1e-operation-safety-foundation.md`。
- Task 15 clean-tree gates最终通过：无fixture全量780 pass events/695 leaf/285 skip events/0 fail，Action race232/204/26 skip/0 fail，build/vet/diff/invariant均PASS；`42d3487d018a174bbcb85aa1b71947d35e968a93` 归档 `SPEC_PASS`、`IMPLEMENTATION_PASS`、`SECURITY_PASS` 及最终manifest。B1-E当前仍为 `passing / limited_subscope`。


## 2026-09-04：B1-D 0004已在隔离fixture验证，optimizer未选新索引

- `go-016` 唯一 `in_progress / PERFORMANCE_BLOCKED`。两个 v2 用户读接口、三个旧 GET 适配及 login/refresh/self/me 同投影已接线；fresh actor→target→session→Redis、严格 policy、commit 后响应；身份无效与单独 policy 不可用分开处理。
- 已批准 B2 列表/详情提前作为 B1-D 只读子集，不代表 B1 写链/票据、M1/A04 或 26 联合用例完成。旧管理写/日志/告警/dashboard 权限风险保留。FE 业务由独立 writer 处理。
- 独立QA确认新隔离MySQL8.0.46/Redis7.4.11的focused service46 terminal/42 leaf、handler17/8及对应race全PASS；fresh serial full691 terminal PASS/1性能opt-in SKIP/0FAIL（631 leaf PASS/1SKIP；15 package PASS/4无测试package SKIP）；build/vet/diff PASS。安全四级问题均0，九生产hash匹配，功能/security/integration均PASS。
- 独立100k、10×20、page20三次fresh warm P95为633.956ms FAIL、466.753ms PASS、504.793ms FAIL，整体性能FAIL；disk-cold仍NOT_RUN。exact count扫描100001行约312ms，page约0.09ms，当前缺少覆盖`is_deleted+role+status`的count索引。
- 用户扩展授权后，0004 `idx_users_admin_read_count(is_deleted,role,status)` 已按TDD实现并仅应用到隔离fixture；真实SHOW INDEX与0001–0004 ledger通过。migration focused20、service focused/race各46、handler focused/race各17、fresh full692PASS/1性能SKIP/0FAIL，build/vet/diff均PASS。0001–0003及九个B1-D业务生产文件hash不变。
- 第1次fresh 100k为first90.669ms、warm P95470.646ms，单次数值PASS；但EXPLAIN exact count仍使用`idx_users_active_updated`扫描100001行、约203ms，未选择新索引。按预设停点未跑第2/3次、未做ANALYZE/hint/索引调整/成本采样；历史三次不稳定结果继续有效，状态为`PERFORMANCE_BLOCKED / OPTIMIZER_REVIEW_REQUIRED`。
- PM随后批准在同一fixture执行`ANALYZE TABLE users`，返回OK、墙钟0.10s；但新索引三列cardinality仍均为1，exact count仍选择旧索引扫描100001行（约148ms），page约0.054ms、总量不变。第二停点触发，新的三次fresh、hint、索引调整与成本采样均未执行。
- PM批准的只读H1/H2确认B0/H1/H2均返回total100000与相同20 GUID，page均保持`uk_users_guid`。B0旧索引约112ms；H1 USE INDEX未选新索引而table scan约37.9ms；H2 FORCE INDEX才选0004新索引但约184ms、更慢。100k分布几乎全为未删除/user/active，不能外推生产混合分布。
- PM批准的H3用临时overlay直接调用现有`usersReadWhere`生成同一P，counted仅`IGNORE INDEX(idx_users_active_updated)`并复制args。B0/H3 total100000、同20 GUID，page仍`uk_users_guid`。direct EXPLAIN约77.2→30.5ms；7组交替墙钟中位数67.241→24.042ms，H3选择PRIMARY且改善重复成立。
- H3仍未覆盖deleted、role/status、escaped LIKE和可选GUID OR，也不是HTTP并发P95；直接count必须维持单一谓词来源与严格参数复制。任务容器与凭据再次静止保留，等待PM查询设计审查及独立QA；当前状态`PERFORMANCE_BLOCKED / H3_PROMISING_PENDING_QUERY_REVIEW`。生产、commit/push/deploy、cleanup、FE-BE联合验收均未执行。
- PM批准后H3已按TDD最小实现：同一`usersReadWhere` P用于filtered/count，args按P/P/limit/offset复制，counted仅IGNORE旧索引，单statement snapshot不变；0004不变。动态筛选、排序、空页/total-items测试已补强。
- focused/race全部0FAIL；最终fresh full693PASS/1性能opt-in SKIP/0FAIL（633 leaf，15 package PASS+4 no-test SKIP），build/vet/diff PASS。三次独立fresh warm P95为77.817/75.512/74.710ms，均通过500ms；disk-cold仍NOT_RUN。
- 0004成本单样本：100001行clone构建0.16s，users索引约2.64MB；10k insert 0.19/0.17s、5k软删除0.13/0.12s、5k status+role 0.13/0.09s（有/无0004），只用于写放大方向。临时表已删除。
- 当前`WRITER_PASS_PENDING_INDEPENDENT_QA_AND_EXACT_CLEANUP`；go-016仍in_progress。资源静止保留，生产、commit/push/deploy、cleanup、FE-BE联合验收均未执行。
- 独立H3 QA最终PASS：动态合同与全部回归通过，四级安全问题0；独立三次fresh P95为113.688/76.997/78.737ms，均<=500。用户授权的exact cleanup已完成：两个精确ID/名称及同label残留均不存在，私密目录不存在；无prune/volume/其它容器操作。
- `go-016`现为`passing / PASS_LIMITED_SCOPE`。disk-cold、FE-BE、26联合用例、旧写链、其它管理域及生产迁移/部署仍NOT_RUN。



## 2026-09-03：B1-C 双只读权限展示接口限定通过，fixture已清理

- `go-015` 已为 `passing / PASS_LIMITED_SCOPE`。PM最终SPEC PASS；独立真实质量VERDICT PASS，四级安全问题均0，8生产hash二次匹配。限定两个GET展示接口，不完成整体B1、权限写操作、FE接线或26项联合验收。
- Writer：focused83/race107/fresh full627全部PASS、0fail/0testskip，15 package pass/4 no-test；build/vet/diff PASS。独立：focused96叶子PASS（service72/handler24）、race96叶子PASS、真实HTTP边界overlay1PASS，均0fail/0skip。
- 本批现有0001–0003迁移ledger和所有脱敏日志已归档。初次归档.go误扫描及runner环境污染的失败属于历史，修后fresh full为最终依据；生产代码/断言/迁移未改。
- 已按用户本次生命周期授权完成exact ID/name/label/image/AutoRemove核验后清理：MySQL133f7729e687、Redisf3c304aaec4b均已不存在；本批fixture.env、随机凭据及私密目录已删除。未触生产、缓存、卷或其它任务。
- 最终报告：`docs/superpowers/reports/2026-09-03-b1c-admin-authz-read.md`；独立报告、JSON与cleanup在同名validation目录。FE仅两GET合同和协调文档更新，其余25entry/root合同/web-012/26jointcases保持原状。以下B1-C NOT_RUN/awaiting/复核中均为已标记历史。


## 历史阶段 2026-09-03：B1-C 真实fixture验证通过，等待独立与PM最终复核

- 本次用户已授权的隔离生命周期已实际执行；MySQL8.0.46/Redis7.4.11与现有0001–0003迁移ledger核对通过。focused83、race107、修正后fresh full627 test全部PASS，0fail/0skip；15 package pass/4 no-test，build/vet/diff PASS。所有8个生产hash不变。
- 初次full失败源于归档探针.go被包扫描及runner迁移APP_ENV污染两config test；改为.go.txt保留原bytes，migration env局限其进程，断言/生产代码/迁移不变；修后fresh full结果是最终计数，失败历史日志保留。
- `go-015` 继续唯一 `in_progress / REAL_FIXTURE_VALIDATING_PENDING_INDEPENDENT_QUALITY`。PM已重计627/83/107并重算8生产hash，最终限定SPEC PASS；fixture窗口已释放给独立实测，MySQL/Redis任务容器暂保留，未清理。当前不再是DB NOT_RUN；以下NOT_RUN/awaiting记录均为前一阶段历史。
- 证据：`docs/superpowers/reports/validation/2026-09-03-b1c-admin-authz-read/real-fixture/`。没有生产、真实模型调用、commit/push或FE业务改动；整体B1及26项联合验收仍未完成。

## 历史阶段 2026-09-03：B1-C 本批隔离fixture生命周期已授权，开始验证

用户对本批具体生命周期请求回复「就行」并要求「继续」。已获授权范围为任务MySQL/Redis容器与库、现有0001–0003迁移、隔离测试及owned子库创建/清理、最终exact task清理，不涉及生产。工作树与8个冻结生产hash已核对，精确标签/名称检查未发现残留；当前 `go-015` 为 `in_progress / validating`。下方NOT_RUN与awaiting记录是上一阶段历史，不能当作本次最终结果。

## 历史阶段 2026-09-03：PRD-260903 B1-C 两个权限展示只读接口已实现候选

- `go-015` 为唯一 `in_progress / awaiting_fixture_authorization`。两个已注册 GET 路由、白名单 DTO、request ID/no-store、私有认证会话版本、READ COMMITTED actor→可选target→session SHARE→Redis 与 commit后返回已落地；disabled Admin仅展示投影，无授权Evaluator或写入口。
- 新鲜认证失效401（原middleware401不变）；先验证session/Redis再返回角色403或隐藏目标404。hidden target规则优先于其坏status/AuthVersion；只有未被隐藏的可见目标腐败503。未匹配路径保留Gin语义。
- 最终无fixture全量：356 test pass、229 test skip、0 fail，15 package pass、4 no-test；HTTP1 pass/5 skip/0 fail，build/vet/专项race及authz/middleware full race通过。初始受限执行因httptest回环bind被拒，正常工具升级后复跑通过；所有DB/Redis验收仍NOT_RUN。
- PM conditional SPEC CODE ALIGNED（代码无剩余规格gap，条件为docs一致和真实fixture）；独立静态/无fixture复核已完成：VERDICT PARTIAL，四级安全问题均0、8生产hash匹配、纯JSON叶子18 PASS/34 fixture SKIP/0 FAIL；focused race、HTTP overlay probe、gofmt/diff/build/vet PASS，真实DB严格NOT_RUN。双连接半策略、commitfail无DTO、session/Redis与corrupt policy用例源码已完成，但没有本批fixture运行证据。
- FE仅同步两GET合同和四份协调文档；其余25项接口与根合同不变，web-012不变、26项联合验收NOT_RUN。本批fixture生命周期脚本只为DRAFT_ONLY，未创建容器/数据库、执行迁移或真实上游；无commit/push/生产操作。报告：`docs/superpowers/reports/2026-09-03-b1c-admin-authz-read.md`。

## 2026-09-03：PRD-260903 B1-B3 Gateway Key 最新用户 ACL 约束限定通过

- `go-014` 为 PASS_LIMITED_SCOPE：Gateway Key 每请求读取最新 owner ACL 并与 Key ACL 双重授权，owner ACL 收窄在下一请求生效；列表先读取 Key/global catalog，再按 owner ACL 过滤；详情、chat/SSE 拒绝路径在目录或生成上游前 fail closed。成功 models/detail 使用 `Cache-Control: no-store`，新 503 为固定 `gateway_authentication_unavailable`。
- 用户明确授权后，仅向任务专属 disposable MySQL 8 库执行现有 0001–0003。PM 最终 SPEC PASS；独立 VERDICT PASS，fresh JSON 544 pass/0 fail/0 skip、15 package pass/4 no-test，race service 1.803s/handler 2.383s，build/vet/diff/feature JSON均通过，四级风险均0。完整证据见 `docs/superpowers/reports/2026-09-03-b1b3-gateway-owner-acl.md`。该通过不完成整体 B1、FE 接线、票据/idempotency/outbox或26项联合验收，也不授权生产、commit或push。

## 2026-09-03：PRD-260903 B1-B2 管理用户安全更新限定通过

- `go-013` 已通过本地限定验收：严格 `PUT /admin/users/:guid` JSON、真实 status/plan/用户 ACL 会话失效与 AuthVersion、daily-limit-only 审计、event10和会话撤销审计归属。`allowed_models: []` 明确保持既有用户 ACL **unrestricted** 语义；daily limit 0 的既有额度计算不变。
- backend PM 为限定合同给出 SPEC PASS；独立 `permission_snapshot_verify` VERDICT PASS，Critical/High/Medium/Low均none。fresh JSON：512 pass、0 fail、0 skip；15 package pass、4 no-test；models/service/handler race分别1.466s/13.693s/3.235s；HTTP adversarial probe 1.515s通过。build/vet/diff/feature JSON均通过。
- 该 PASS 不覆盖旧角色授权、细粒度权限 writer、ticket/idempotency/outbox、Key ACL repair、前端接线或26项联合验收；不授权生产、commit或push。报告：`docs/superpowers/reports/2026-09-03-b1b2-managed-user-security.md`。

## 2026-09-03：PRD-260903 B1-B1 权限策略持久化快照本地验证完成

- 用户已授权仅在任务独占 MySQL 8 / Redis 7 fixture 中实现并验证 0003 policy head/override schema 与只读 snapshot loader；不接 HTTP、DTO、权限写操作、角色变更、前端或部署。
- 任务容器使用 loopback 随机端口、`codex.task=authz-persistence-b1` 标签、AutoRemove 与 tmpfs，不使用既有 `porsche-mysql-test`/`porsche-redis-test`、命名卷、生产 `.env` 或连接串。`TEST_*` 仅存在于私有临时文件，未输出凭据。真实 `TestAuthCoreMigrationOnIsolatedMySQL` 已 PASS；基线 `init.sh` 亦在清除 `TEST_*` / `RUN_START_COMMAND` 后 PASS 且未启动服务。
- B1-B1 规格和按 TDD 拆分的实施计划已建立：`docs/superpowers/specs/2026-09-03-b1b1-permission-snapshot-design.md`、`docs/superpowers/plans/2026-09-03-b1b1-permission-snapshot.md`。真实 fresh 全量为456 test pass、0 skip、0 fail，15 package pass、4 no-test-file，build/vet/diff/JSON通过；backend project manager final SPEC PASS，独立 `permission_snapshot_verify` VERDICT PASS（无Critical/High/Medium/Low）。reader-before-writer probe确认FOR SHARE保留到commit、writer阻塞250ms、v1 allow到commit v2 deny。`go-012`、`go-011`为passing，`go-004`保持blocked；未接HTTP/DTO/前端或生产部署。

## 2026-09-03：PRD-260903 B1-A 纯授权评估器本地验证完成，待独立复核

- 新增仅限 `internal/authz` 的固定 24 能力目录和纯内存评估器；未接入 HTTP、DTO、数据库、迁移、前端、SSE 或模型调用。有效 active 普通用户可构造 evaluator，但全部管理决策保持 deny。
- 先记录 compile RED 和 deny-all behavior RED，再实现 GREEN。focused、race、vet、全仓 build 与显式清除 `TEST_DATABASE_URL`、`TEST_REDIS_URL`、`RUN_START_COMMAND` 的全量 Go 验证均 exit 0。全量 JSON 记录 98 个 DB/Redis fixture 测试跳过和 4 个无测试文件包跳过；跳过不是集成验收通过。
- B1-A 本地实现、规格审查和独立安全/测试复核均完成并 passing。B1 其余项目和全部公开 HTTP 仍未实施，go-004 继续 blocked，未提交、推送、部署或迁移。
- backend_project_manager 已完成限定 B1-A 规格复核并判定 PASS；`admin_authz_security_verify` 最终 PASS 且无 Critical/High/Medium。全量 JSON 为 Test 312 pass/98 skip/0 fail，package 15 pass/4 无测试文件 skip/0 fail；跳过不能当作数据库或 Redis 集成验收。`scope` 和 `Decision` 是当前纯内部、非持久化数值，未来不能当作稳定 DB/API 编码。报告：docs/superpowers/reports/2026-09-03-b1a-pure-authz.md。

## 2026-09-03 15:48：ad3f5b4发布及正常SSE复测通过

本节为最新状态，后续旧记录的“未发布/剩余1次/有效流FAIL”仅适用于当时。

- ad3f5b4已授权发布；第4次正常SSE子项PASS：200、meta→4 delta→[DONE]→done，1 POST/0 refresh，唯一请求哈希与运行日志匹配、全部保存阶段成功、tokens0→1。清理200/logout204，预算4/4耗尽。object拒绝本次未复现，根因未闭环；流内错误/取消等未测，整体M3 PARTIAL。go-004保持blocked、web-009保持in_progress，诊断功能passing不代表整体签收。
- 新容器1665e111、镜像2bc6b866，源站/公网200；旧13ada4aa及另两个更早容器停止保留，前端158a00e不变，私密快照删除。
- 后端PM已书面确认发布及正常终态限定PASS；独立质量亦确认成功流限定PASS、整体M3 PARTIAL；详见2026-09-03-m3-object-release-and-retest.md。没有产生object_detail；提取器unknown/unknown是缺字段默认值，不是拒绝。


## 2026-09-03：object有限分类候选ad3f5b4已准备

- 本地实现固定decoded_kind/field_shape/key_match；保持原SSE接受/拒绝，原值不进入日志。相关包及race各175pass；默认全量304pass/0fail/98个DB或Redis fixture缺失SKIP，不能算完整DB验收。50组公开响应与17正常摘要对照一致，独立规格/质量限定PASS。
- linux/amd64候选ad3f5b4、镜像2bc6b866、归档SHA8154e46b已核验；尚未上传或部署。发布脚本7项mock/绑定检查、第四次脚本3项预检通过。详细发布单2026-09-03-m3-object-release.md与m3-object-diagnostic-candidate.json已准备。
- 线上07:34:15Z只读确认仍6e70784/13ada4aa/cb42，健康200、两个更早回滚对象保留。本轮0真实生成；预算总4、已用3、剩1次gpt-5.4-nano/max_tokens32。
- 待准确新候选部署授权后再核验运行来源并复测。go-010仅本地诊断passing，go-004 blocked、web-009 in_progress；M3-11仍FAIL。


## 2026-09-03：开始object有限分类诊断

用户追加预算后继续，go-010为唯一in_progress。方案沿用既有调查的固定分类，保持object校验和公开协议；剩余1次请求暂不消耗。init.sh首次因沙箱禁止httptest回环监听失败，允许回环后原命令通过（默认无TEST_*，不能替代数据库集成验收）。设计与计划见docs/superpowers/specs/2026-09-03-m3-object-classification-design.md及同名plans文件。


## 2026-09-03 15:22：追加1次诊断调用预算

- 用户明确“增加调用预算”；未指定数量，按最小增量增加1次，总上限3→4，已用3、剩余1。模型仍为gpt-5.4-nano，每次max_tokens=32；原三次记录完整保留。
- 新额度用于补充可区分object原因的诊断复测，先准备并验证具体采证方案，不盲目重跑旧请求、不自动重放。历史attempt3脚本仍是一次性记录，不为增加预算而直接修改重跑。
- 本次仅更新预算和交接记录，未执行第4次生成、未部署新候选。增加调用额度不代表已授权任何尚未确定的新候选部署。M3-11仍FAIL。
- 当前预算以validation/m3-sse-budget.json及本节为准；此前“预算0/3次耗尽”是当时事实。


## 2026-09-03：object契约离线调查完成

- 当前6e70784解析器25项合成object假设均符合断言；调用实际投影/SSE路径，无业务代码变更或真实生成。缺失/null/空/其他字符串仍不可由既有invalid_value/object日志区分；重复键、大小写与转义键行为已验证。
- 官方通用chat.completion响应说明不足以放宽流chunk校验。后端PM书面确认维持原合同，优先取得既有脱敏样例；缺证据时再设计有限分类诊断，当前未实施或准备新发布候选。
- 预算3/3耗尽、M3-11 FAIL保持。详见2026-09-03-m3-object-investigation.md；含协议澄清草稿（未向供应商发送）、25项离线证据及下一步准入。


## 2026-09-03 14:19：6e70784已发布，第三次SSE定位object校验失败

本节为当前状态；下方“未发布/字段未知/剩余预算”等均为历史快照。

- 用户明确“授权”后已上传并部署6e70784；镜像cb42daed、容器13ada4aa，源站/公网health均200，前端158a00e资源哈希未变。旧9425ea及更早d2de587容器均停止保留，私密运行配置快照已删除；未执行生产回滚。
- 第3次真实请求1 POST/0 refresh，HTTP503/0帧。浏览器请求哈希与运行版本日志精确匹配；上游200，sse_stream失败malformed_chunk，固定详情invalid_value/object。仅能确认解码后object不等于chat.completion.chunk，实际值、缺失/null/空值及后续字段有效性仍未知。
- 应用daily_calls_used 2→3、remaining 98→97、tokens仍0，不代表上游零计费；本次测试会话删除200、注销204。
- 预算3/3已耗尽，每次gpt-5.4-nano/max_tokens32；不得重置台账或继续生成。下一步由后端PM核对object契约与既有脱敏样例，先离线验证假设再决定兼容方案；任何新增真实生成需新的明确预算授权。
- 后端project_manager书面确认发布子项通过、M3-11 FAIL；独立质量PARTIAL。两者仅复核证据、未独立执行线上操作。go-009诊断范围passing、go-004 blocked、web-009 in_progress，整体M3不签收。


## 2026-09-03：chunk细分候选6e70784已完成本地验证，待新候选发布授权

- 固定malformed_chunk_detail.reason/field已实现，原公开503、大类和SSE接受/拒绝行为保持；没有记录原始帧或字段值。完整345/race113个测试pass，0fail/skip，vet/build及独立规格/质量PASS；独立22组新旧输出一致。
- linux/amd64镜像cb42daed已本机构建，归档SHA195a6f38，来源/二进制/CA验证通过；尚未上传/部署。具体清单m3-chunk-diagnostic-candidate.json与后端发布单2026-09-03-m3-chunk-release.md已准备，后端PM材料复核通过。
- 线上仍04ed728（容器9425ea/镜像a69），M3-11仍FAIL，具体首帧校验字段尚未知；最后1次gpt-5.4-nano×max_tokens32预算未使用。新候选需单独完成发布授权及运行核验后才能安排最后一次复测。
- 本轮测试fixture和凭据已清理；不能复用其旧TEST_*地址。go-009仅本地passing，go-004保持blocked，前端web-009保持in_progress。


## 2026-09-03：开始chunk固定校验分类

用户继续已说明的细分诊断方案；go-009为唯一in_progress。保留线上04ed728与最后1次×32预算，先做本地分类、行为对照和脱敏验证。设计与计划见docs/superpowers/specs/2026-09-03-m3-chunk-validation-design.md及对应plans文件。

## 2026-09-03：后端诊断已发布，SSE第二次仍失败并定位解析阶段

- 用户明确确认上传及后端替换后完成发布：源码04ed728，镜像sha256:a69cfdab1cc8e18056286ae3991669d37515994041664b3fed5b6290ac602316；新容器9425ea244ad71944ef78474cc405208fbbbe7eb22fdffd2b81e269d328d85b0c于05:50:09Z启动，源站/公网health严格200。旧d2de587容器以ai-gateway-go-acceptance-rollback-1788414605780771454保留，未执行生产回滚；前端158a00e哈希保持。
- 首次预检因OomKillDisable的null/false表示差异安全停止，未停旧服务。公开基础镜像两版本API探针证实创建规范化，限定兼容该默认值后重新预检；true仍拒绝。三锁、私有运行配置JSON快照、候选create后逐字段比对均执行，成功后私有快照删除。脚本已归档，仅适用本次精确对象，不是可直接复用的常规发布入口。
- 真实SSE第2次于05:51:52Z发送：gpt-5.4-nano/max_tokens32，1POST/0refresh，HTTP503、gateway_upstream_unavailable、0帧。请求ID哈希与候选日志匹配，源码revision也匹配；上游返回200，sse_stream failed/malformed_chunk，首帧未发出，auth/catalog/前置quota及消息写入均success，assistant/usage/final_write未运行。尚不确定具体哪个字段或数据类型不兼容，不能宣称修复或归责供应商。
- 应用日调用1→2、剩余99→98、token计数0；这不是供应商零费用证明。测试会话删除200、注销204。预算累计2/3，剩1次×32，不重置、不盲目重放。
- M3-11仍FAIL、go-004仍blocked、web-009仍in_progress。后续先核对解析器拒绝条件与脱敏结构证据，再决定是否使用最后一次预算；不放宽白名单投影、不透传上游正文、不把health通过当M3签收。


## 2026-09-03：镜像已完成，私有上传与后端发布待授权

- 代码04ed728的linux/amd64诊断镜像已在本机离线组装、导出和重新加载；精确ID sha256:a69cfdab1cc8e18056286ae3991669d37515994041664b3fed5b6290ac602316，归档SHA256 af8a122d1fa5018a981d4757aff03b0b204ef8048c38f2632eb47e343ad0c580。
- 来源标签、两个二进制哈希、架构、入口、CA均核验；无凭据/无网络启动按预期缺JIEKOU_API_KEY拒绝，不能当作真实服务健康。
- 原私有源码/二进制构建包上传被自动审批拒绝，未执行。远端仅构建公开基础层并下载，本机加入私有二进制；私有镜像尚未上传，生产后端未替换。
- 具体上传、三锁、运行态配置快照、切换及失败回滚准备见后端docs/superpowers/plans/2026-09-03-m3-backend-release.md；待用户明确授权，计划尚未在生产执行或演练。此前镜像构建超时为已解除的历史阻塞。
- 后端PM书面确认发布准备要求，不代表用户上线授权或线上M3签收。M3-11仍FAIL，剩余2次gpt-5.4-nano×max_tokens32预算保留。


## 2026-09-03：M3诊断本地候选完成

- 从0bab2b7建立独立fix/m3-sse-diagnostics；单模型platform chat新增请求关联、阶段/上游状态/保存标记的白名单日志，公开响应和扣次/保存顺序不变。
- 最终隔离MySQL8/Redis7全量293、专项race46个pass事件，0fail/skip；vet/build/diff通过，后端PM与独立质量限定PASS。完整RED/GREEN、socket探针、redirect返工及边界见docs/superpowers/reports/2026-09-03-m3-sse-diagnostics.md。
- 本轮临时测试依赖/凭据已清理；生产未动，线上剩余2次gpt-5.4-nano×max_tokens32预算保留。go-008仅本地passing，go-004与前端M3不因日志候选而签收。下一步明确后端候选发布及回滚，再带诊断复测。

## 当前活动状态

当前无实现中的功能。go-010有限object诊断已完成本地验证并准备ad3f5b4/2bc6候选，等待准确新候选的部署授权；线上仍6e70784/13ada4aa/cb42。go-004真实SSE继续blocked，M3-11 FAIL。预算总4、已用3、剩1，候选运行和采证准入满足后再复测。默认基线98项DB/Redis fixture缺失SKIP已记录，不能当作完整数据库验收。

## Refresh 与测试隔离修复完成（2026-09-03，开始于 2026-09-02）

- 唯一生产代码改动为 `SessionService.Refresh` 事务后的空轮换结果检查：成功提交重放撤销和审计后返回 401，保留 Redis 否决优先及依赖错误传播。没有修改 schema、迁移、Redis 协议或 API 契约。
- 真实 TLS HTTP 回归先 RED：窗口外重放返回 500；修复后验证注册 201、登录/轮换 200、旧/重复/新 Refresh 与对应 Access 均 401，同时确认 MySQL 撤销、精确一次审计和 Redis 栅栏。Service 回归使用受控时钟，并覆盖审计约束失败时 MySQL 回滚而 Redis 继续拒绝令牌。
- Root 引导用例使用随机、独占创建的临时数据库，关闭连接后仅删除本次创建的库；隔离测试验证父库 sentinel 保留。其他用例使用唯一用户名/IP/SID，密码审计 CHECK 限定目标用户，未清空共享测试库或 Redis。
- 本任务新建的 MySQL 8.0.46 / Redis 7 中：全量 `go test -json -p 1 ./... -count=1` 为 247 个通过事件（含子测试），0 失败、0 跳过；不清空数据再运行 `-count=2 -shuffle=on` 得到 494 通过；认证专项 `-race -p 1 -count=3` 得到 60 通过，均无失败/跳过。build、vet、diff、JSON 检查通过。
- 独立设计审查与质量/安全审查均通过；质量审查另独立执行四项关键回归及 Root 子测试通过。完整命令、RED/GREEN 与清理证据见 `docs/superpowers/reports/2026-09-02-refresh-replay-test-isolation.md`。
- 本任务两个一次性依赖容器已按精确 ID 核验并清理，临时测试数据已销毁；没有操作用户原有容器、命名卷、主工作树或生产服务。仅保留本地分支与提交。真实 JieKou 上游验收、远端推送/合并、生产发布均未执行。

## Refresh 与测试隔离设计（2026-09-02）

- 已核实成功撤销的事务必须先提交，再返回 401，避免将撤销和审计回滚。设计采用业务最小修复、Root 专用临时库、其他测试唯一标识与目标用户级故障约束，不更改生产 Redis 键规则。
- 设计：`docs/superpowers/specs/2026-09-02-refresh-replay-test-isolation-design.md`。本轮尚未修改业务代码或测试；未新建依赖容器、未访问生产服务。
- `GOCACHE=/private/tmp/porsche-go-build-cache bash ./init.sh` 通过（未注入 TEST_*，存在集成测试跳过/缓存，不能替代新设计要求的完整验证）。只做本地文档提交，不推送、合并或部署。

## 测试基线修复（2026-09-02，用户批准继续）

- 仅修改四个 `_test.go`：MySQL 返回 `DATA_TYPE / IS_NULLABLE` 大写列标签，原 GORM 小写映射读为空，改用 positional `Row().Scan`，保留类型/nullable 断言且无行时报错。两处 `session_user_` / `disabled_user_` 长前缀改用共用 `fixtureUsername`（`u%019d`），保留完整 Snowflake、20 字符上限及小 ID 最小长度。
- 三项原始集成回归先在独立 MySQL 8.0.46/Redis 7 中复现失败，再修复通过；新增 ID 边界、唯一性和可逆性回归。四项测试 `-race -count=3` 通过，`go build ./...`、`go vet ./...` 与 `git diff --check` 通过。未改业务代码、迁移、生产 `.env`、前端或跳过规则。
- 全新库 `porsche_baseline_clean_test` 下全量仍失败（225 pass 事件含子测试、0 skip）：审计故障注入的全表 CHECK 被其他测试留下的 password-changed 行阻止；Root bootstrap 测试遇到前面测试创建的 Root；Refresh 过期重放单独运行也在 `auth_session.go:203` nil dereference。后者因撤销事务成功后 `rotated` 仍为空，随后访问 `rotated.Session`。无生产复现或部署操作。
- 当前只保存已批准的两类测试修复，新失败不通过改断言、放宽 schema 或隐藏用例来绕过。完整命令与输出见 `docs/superpowers/reports/2026-09-02-backend-test-baseline.md`；下一步需批准修复 Refresh 业务分支及测试隔离。

## Open issues 核验（2026-09-02）

- 基线为 `origin/main` 的 `4da0dba8b1175d2accfe91080723c64b54eceb4d`；本轮未修改业务实现、生产配置或迁移，也未关闭远端 Issue。
- `GOCACHE=/private/tmp/porsche-go-build-cache bash ./init.sh` 通过；默认未注入测试数据库时会跳过数据库集成用例，不等于完整集成验收。
- 新建本地、仅回环发布端口的 disposable MySQL 8.0.46（tmpfs 数据目录、`porsche_issue2_test`）与 Redis 7 fixture；仅设置显式 `TEST_DATABASE_URL` / `TEST_REDIS_URL`，未读取生产 `.env` 或访问生产服务。
- `go test -p 1 ./internal/whitelabel ./internal/handler -run 'Slash|CatalogWithEmpty|PatternAllowlist|ModelACL' -count=1 -v`：13 个顶层专项测试通过且无跳过，覆盖 slash ID、URL 编码、空上游目录数组、精确 ACL、用户/Token 拒绝时不访问上游。`go vet ./...` 通过。
- `go test -p 1 ./... -count=1`（同一隔离 MySQL/Redis）**失败**，不能标为全量通过：`internal/migration/runner_test.go` 的 `assertColumn` 扫描结果为空，而直接 SQL 查询 `users.username` 返回 `varchar / YES`；`internal/service` 多项测试生成 `session_user_<18位GUID>` 或 `disabled_user_<18位GUID>`，超过 schema 的 `VARCHAR(20)`，报 MySQL 1406。两类均已在未改业务代码的 main 基线上复现；另开测试基线修复任务处理，不放宽生产 schema。
- Issue #2 仍待经授权的真实上游目录、详情、Chat/SSE 验收；本轮证据不代表这些外部验收已完成。

## 用户注册管理一期 Task 1（2026-08-28）

- 除 development 外的认证配置 fail-closed：缺少或无效 Redis URL、固定登录、SMS 开发模式或示例凭据、短于 32 字节/默认/重复或复用的 JWT/HMAC/Admin/Metrics 密钥、非 HTTPS 或空可信 Origin、无效开关/认证数值，以及不完整或不合法的一次性 Root 引导均会拒绝启动。`APP_ENV` 会 trim/lowercase 并限制为 `development`、`test`、`staging` 或 `production`。
- `LoadMigrationSettings` 保持只加载迁移所需配置，不要求上游或认证配置；本 Task 未新增或执行数据库迁移，也未连接数据库。
- 已验证 `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/config -count=1`、`git diff --check` 与 `GOCACHE=/private/tmp/porsche-go-build-cache go test ./... -count=1`。受限环境首次因禁止 `httptest` 绑定 `[::1]` 失败，在允许回环监听的验证环境复跑后全量通过。

## 用户注册管理一期 Task 2（2026-08-28）

- 新增嵌入式、前向 `0002_auth_core` MySQL 迁移：不修改 `0001`；将既有 `users.phone` 改为可空但保留 `uk_users_phone`，新增可空、全局唯一且软删后永久占用的 `username`，以及 `role`、`auth_version`、`last_login_at`。这使后续用户名注册可以不伪造手机号；现有 Python 数据不会被删除或重写。
- `user_sessions` 与 `auth_audit_events` 均使用有符号 `BIGINT guid`、毫秒 `BIGINT` 审计字段、`INT is_deleted` 与默认活跃查询索引；用户关联只使用 `user_id -> users.id`。会话仅存当前/前一 Refresh HMAC，认证审计不保存密码、令牌、Cookie 或原始 Authorization/Header。
- Go `User.Phone` 为不序列化的 `*string`；旧手机号认证仅作兼容写入，未生成假手机号。新增稳定 `UserRole`、`LoginMethod` 与 `AuthAuditEventType` 整数映射，以及 `Session` / `AuthAuditEvent` 持久化实体。尚未实现会话服务、用户名注册或任何 Task 3+ 业务路径。
- 已验证迁移/实体契约与全量 Go 回归：`GOCACHE=/private/tmp/porsche-go-build-cache go test -v ./internal/migration ./internal/models -run Auth -count=1`、允许回环监听环境中的 `GOCACHE=/private/tmp/porsche-go-build-cache go test ./... -count=1`、`go vet ./...` 与 `git diff --check`。
- 环境阻塞：未设置 `TEST_DATABASE_URL`。受控 MySQL 测试只读取该变量并拒绝非 `*_test` 库，当前按设计跳过；未连接 `DATABASE_URL`、`.env` 或任何生产库，因此真实 MySQL 8 的 `0002` 迁移验证仍待提供隔离测试库后执行。

## 用户注册管理一期 Task 3（2026-08-28）

- 新增 fail-closed `go-redis/v9` 认证存储：账户/IP 双维登录失败锁、24 小时会话签发上限、会话否决栅栏，以及仅以 SID-AAD 绑定、用途 KDF 隔离的 AEAD 加密保存 30 秒并发 Refresh 结果。刷新明文不写 MySQL、审计事件或日志；MySQL 只保存 HMAC-SHA256 摘要。
- `SessionService` 在 MySQL 事务内创建、轮换和撤销会话，并复用共享雪花 `guid` 与毫秒审计 helper；常规读取固定 `is_deleted = 0`，第 51 个活跃会话会逻辑吊销最旧会话，窗口外旧 Refresh 重放会先写 Redis 否决栅栏再吊销 MySQL 会话并写审计事件。
- 已按 RED→GREEN 验证：新增 API 不存在时 `go test ./internal/service -run 'Test(AuthSession|RefreshRotation|LoginRateLimit)' -count=1` 编译失败；实现后定向测试通过但在未设置 `TEST_REDIS_URL` 时四项真实 Redis/MySQL 用例显式跳过。允许回环监听的环境中 `GOCACHE=/private/tmp/porsche-go-build-cache go test ./... -count=1`、`go vet ./...` 和 `git diff --check` 通过。
- 环境阻塞：`TEST_REDIS_URL` 与 `TEST_DATABASE_URL` 均未提供。测试从不读取 `.env`、`REDIS_URL` 或 `DATABASE_URL`，因此真实 Redis/MySQL 的 5 次限流、51 会话淘汰、30 秒并发 Refresh 与窗口外重放吊销仍待隔离环境执行。
- 安全返工：Refresh 改为 SID 行锁内先写 Redis 加密 pending 结果、MySQL 提交后可恢复发布；后提交发布失败时，持有旧 Cookie 的并发请求可在验证前一 HMAC 后恢复同一结果，不会误吊销会话。`RevokeOthers` 与创建会话共享 `users` 行锁，避免并发下漏吊销目标会话。
- 状态机返工：Redis public/pending rotation 记录均携带目标 Refresh HMAC 指纹；Lua 脚本只返回匹配当前 MySQL HMAC 的代次，并原子替换 stale public 结果。连续 A→B→C 且 B TTL 尚存时，B Cookie 的并发请求只能恢复 C，不能返回 B。
- 新增真实受控 MySQL/Redis 集成用例覆盖 A→B→C 后 8 个并发旧 B Refresh 全部返回 C，并断言数据库仅保存 C 当前 HMAC 与 B 前一 HMAC。无 `TEST_DATABASE_URL` 或 `TEST_REDIS_URL` 时显式跳过；本地 `go test -race ./internal/service -run 'Test(RefreshRotationConcurrentOldBReturnsC|AuthSession|RefreshRotation|LoginRateLimit|AuthRedis)' -count=1`、全量测试、vet 与 diff 检查通过。

## 用户注册管理一期 Task 4（2026-08-28）

- 新增用户名认证领域收口：用户名 trim 后限制为 3–20 个 ASCII 字母/数字/`_`/`-`；注册在事务中跨墓碑检查永久唯一性，并由既有 MySQL `uk_users_username` 强制兜底。用户名注册不创建或伪造手机号，密码以带参数的 Argon2id 编码存储；弱密码和非 8–20 字符密码会被拒绝。
- Root 仅由部署配置引导：启动时仅在不存在任何 Root（包括软删墓碑）时创建，使用同一 MySQL 连接的命名锁串行多副本引导，成功后清空进程内 bootstrap 值；Root 创建与认证审计在同一事务中写入。常规用户读取仍以 `is_deleted = 0` 限定，后续管理端点必须禁止 Root 软删。
- 新 Access JWT 仅包含 `sub=<用户guid>`、`sid`、`sv`、`av`、`role`；不含内部用户 `id`、密码哈希或 Refresh。`RequireUser`/`RequireUserID` 均严格校验签名、完整 claims、用户 `guid + is_deleted=0`、状态、`auth_version`、持久化角色与 `SessionService.Validate` 的会话版本/否决状态。
- `RequireAdmin` / `RequireRoot` 改为认证会话上的最低持久化角色检查，Analytics 管理权限不再以手机号判断；`ADMIN_TOKEN` 不能绕过管理员门禁，也不再回退为 Metrics 凭据。
- P1 管理权限竞态修复：`mutateManagedUser` 在事务中锁定操作者后通过 `CanManageUser` 重新要求其为 active；即使请求先前已通过 `RequireAdmin`，随后被禁用的管理员也会收到 403，目标用户状态与 `auth_version` 保持不变。真实 MySQL/Redis 回归仅使用显式 `TEST_DATABASE_URL` 与 `TEST_REDIS_URL`；本地未设置时按安全规则跳过。
- 验证：RED 阶段因缺少用户名函数与会话 claims parser 发生预期编译失败；GREEN 后 `GOCACHE=/private/tmp/porsche-go-build-cache go test -v ./internal/service ./internal/middleware -run 'Test(Username|RootBootstrap|LoginUsername|PasswordUsesArgon2id|AccessTokenSubjectUsesUserGUID|SessionClaims|MinimumRole)' -count=1`、管理员旧 `ADMIN_TOKEN` 拒绝测试、`go vet ./...` 与 `git diff --check` 通过。`go test ./... -count=1` 的剩余失败均为既有测试在受限沙箱无法监听 `[::1]`，未出现 Task 4 业务断言失败。
- 环境阻塞：`TEST_DATABASE_URL` 与 `TEST_REDIS_URL` 未提供，永久用户名、Root 首次引导、禁用/软删登录拒绝与实际 `SessionService.Validate` 的集成用例按安全规则显式跳过；未读取 `.env`、`DATABASE_URL` 或生产凭据。

## 用户注册管理一期 Task 5（2026-08-31）

- 新增 `/api/v1/auth` 的用户名注册、登录、刷新、登出、本人资料、会话列表/本人撤销/撤销其他设备以及密码和实名入口；旧短信与固定账号端点继续明确返回 410。Access JWT 与 Refresh 均不在 DTO 或日志中回显，Refresh 仅通过 `porsche_refresh` 的 `HttpOnly; Secure; SameSite=Lax` Cookie 传输。
- Refresh 与 logout 强制可信 HTTPS Origin，且请求携带 `X-Auth-Session` 时必须与 Cookie 的 SID 一致；会话 SID、内部 `id`/`user_id`、密码哈希和 Refresh HMAC 不会序列化。管理员列表/详情限定严格低角色，删除复用现有事务化软删除服务，Root/同级无法管理。
- RED：新增认证路由/DTO 测试后，因 `dto.AuthUser` 和 `dto.AuthSession` 尚不存在而发生预期编译失败。GREEN：定向 handler/dto/middleware 测试、`go vet ./...` 和 `git diff --check` 通过。
- 测试验收补充：`internal/handler/auth_sessions_integration_test.go` 的真实 HTTP 流仅使用显式 `TEST_DATABASE_URL` 与 `TEST_REDIS_URL`，并在迁移前强制数据库名为 `*_test` 或 `porsche_test`。它覆盖注册、登录、`/self`、会话列表、撤销其他设备、`X-Auth-Session` 与 Cookie SID 不一致拒绝、刷新后的 Access/Cookie SID 一致、登出清 Cookie 及旧 Access 拒绝；另覆盖管理员列表/详情的同级与 Root 拒绝、Root 对低角色放行和响应脱敏。
- 环境阻塞：当前未设置显式 `TEST_DATABASE_URL` 与 `TEST_REDIS_URL`，所以新增真实 HTTP 用例显示 `SKIP`，尚未验收；未读取 `.env`、`DATABASE_URL` 或生产凭据。`GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/handler ./internal/dto ./internal/middleware -run 'Test(AuthSessionHTTPFlow|AdminUsersHTTPHierarchy|AuthSession|AdminUser|LegacyPhone|UserDTO|SessionClaims|MinimumRole)' -count=1`、相同包的 `go vet` 与 `git diff --check` 通过。未加筛选的 handler 包测试仍因既有 `httptest` 无法监听 `[::1]` 而中止，不是 Task 5 断言失败。

`go-004`：JieKou AI 白牌上游接入（`blocked`）。真实部署环境 JieKou 冒烟待办；该外部验证完成前不得标记为通过。

## 已验证基线（2026-08-21）

- 后端：在 `/Users/xuzhihao/code/Porsche/.worktrees/white-label-upstream` 运行 `GOCACHE=/private/tmp/porsche-go-build-cache ./init.sh` 成功。环境为 Go 1.22.12，脚本中的 `go test ./...` 全部通过；未设置 `RUN_START_COMMAND=1`，因此未启动服务。
- 前端隔离：`Porsche-Web/.worktrees` 已由 Git 忽略，已创建 `feature/white-label-upstream` 工作树，主工作区未改动。
- 前端恢复：在 `llm-platform` 工作树中，`npm ping` 成功；`npm install --package-lock=false` 完成且未产生 `package-lock.json` 变更；`npm test` 8/8 通过；`npm run build` 通过，仅输出既有警告。

## Task 2 配置、错误与请求校验（2026-08-22，完成）

- RED：在 `0fe9b36` 上加入回归测试后，`GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/whitelabel -run 'Test(PublicInvalidRequestErrorMatchesContract|ValidateRequestEnforcesChatContract|ValidateMediaURLRejectsLocalAndMappedAddresses|ValidateRequestAcceptsSafeVideoURLAndRejectsUnsafeSources)' -count=1` 失败：大于 16384 的正 `max_tokens` 被拒绝、单标签十六进制 IPv4-like host 被接受，且合法 `video_url` content part 被拒绝。
- GREEN：`GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/whitelabel -count=1` 与 `GOCACHE=/private/tmp/porsche-go-build-cache go test ./...` 均通过。
- 校验范围：未知顶层参数返回 `unsupported_parameter`；`max_tokens` 仅要求正整数并交由上游实施上下文限制；图片与视频 HTTPS URL 均拒绝 userinfo 和非公网字面地址，且验证不进行 DNS 解析；视频不接受 data URI。
- P2 收尾：数据图片解码上限为 8 MiB；回归测试确认 8 MiB 图片 data URI 的完整请求体小于 12 MiB 并可接受，而 8 MiB + 1 字节被拒绝。测试先在旧实现上失败（两/三标签十六进制 IPv4-like host 与超限图片均被接受），随后 `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/whitelabel -count=1`、`GOCACHE=/private/tmp/porsche-go-build-cache go test ./...` 与 `git diff --check` 全部通过。

## 阻塞与未验证项

- 未进行任何真实 JieKou AI 上游目录、Chat 或 SSE 冒烟；该验证仍需要部署环境的白牌配置。

## 全局模型 allowlist 正则（2026-08-27）

- `JIEKOU_ALLOWED_MODELS` 支持逗号分隔的精确模型 ID，以及仅限全局配置的显式
  `re:` RE2 模式；用户与 Gateway Token 的 `allowed_models` 仍只按精确 ID 匹配。
- 已验证 `go test ./internal/config -count=1`、`go test ./internal/whitelabel -count=1`、
  `go test ./... -count=1` 与 `go vet ./...`；无效或空的正则会在启动配置解析时失败。
  未执行真实 JieKou 上游目录、Chat 或 SSE 冒烟，`go-004` 保持 `blocked`。

## Task 6 旧上游清理（2026-08-24）

- 删除静态 `config/models.yaml`、`config/clients.yaml`、旧厂商密钥加载、旧 Gateway/Registry 运行时代码与静态客户端回退。
- `.env.example`、README 与领域文档仅保留 `UPSTREAM_REGION`、`JIEKOU_API_KEY`、`JIEKOU_ALLOWED_MODELS` 白牌配置说明。
- 不修改数据库连接、GORM 模型或迁移，因此不会删除或改写 Python 服务共享的 MySQL 数据。

## 预发布 Nginx 代理安全修复（2026-08-24）

- `deploy/nginx/aiportcloud.conf` 不再转发客户端可控的 XFF 链；它以
  `$remote_addr` 覆盖 `X-Forwarded-For`，避免在应用信任 Nginx 时绕过
  Gateway Token IP allowlist。
- 为 OpenAI-compatible SSE 配置 HTTP/1.1、`proxy_buffering off` 和 300 秒
  read/send timeout。
- `deploy/nginx/test-aiportcloud-conf.sh` 静态校验配置与部署说明；本机未安装
  Nginx 二进制，因此未执行 `nginx -t`。Go 全量测试与 `go vet ./...` 已通过。
- 部署时必须将 `TRUSTED_PROXY_CIDRS` 配置为 Nginx 实际连接容器时的源 IP/CIDR，
  不可从其他主机照抄 Docker gateway；需要使用 XFF 时还必须设置
  `TRUST_PROXY_HEADERS=true`。

## Task 3 生产部署文档与静态验证（2026-08-24）

- README 记录默认部署命令 `sudo bash deploy/production-deploy.sh`，以及当 `.env`
  使用 Docker host 时的 `APP_DOCKER_NETWORK=... sudo -E bash
  deploy/production-deploy.sh`。部署会短暂中断应用；候选容器启动或健康检查失败时
  脚本自动恢复旧应用容器。
- README 明确脚本会 fetch、switch、hard-reset `main` 到 `origin/main`，只替换应用
  容器、不管理 MySQL，拒绝生产 `ENV_FILE` 覆盖；部署前必须执行 `sudo nginx -t`。
- 部署拓扑仍须正确设置 `TRUST_PROXY_HEADERS=true` 和 Nginx 实际源 IP/CIDR 的
  `TRUSTED_PROXY_CIDRS`。真实 JieKou 目录、Chat 与 SSE 冒烟必须在部署环境另行执行，
  未标记为已通过。

## 生产部署预发布加固（2026-08-24）

- 部署脚本只读取所在 checkout 的 `.env`；mock 回归测试在临时 fixture repository 中
  创建该文件并 symlink 真实脚本，因此不会触碰真实 checkout 的 `.env`。
- 每次健康探测使用 2 秒连接和 3 秒总超时，至多 30 次；超时会删除候选并恢复旧容器。
  `/var/lock/${APP_NAME}.deploy.lock` 的非阻塞 `flock` 会拒绝并发部署，且竞争者没有
  Docker 写操作。
- 新增严格 `.dockerignore`，排除 `.env`、Git/worktree/agent 本地目录、data 与测试/IDE
  输出，同时保留 Go 源码、Dockerfile 和 `.env.example`。成功部署输出容器 ID 与 Git revision。

## 前后端一键更新重启（2026-08-26）

- 新增生产入口 `sudo /opt/Porsche/deploy/restart-all.sh`，固定使用后端
  `/opt/Porsche`、前端 `/opt/Porsche-Web`、静态目录 `/var/www/porsche-web` 与
  Docker 网络 `porsche-app`。脚本先拉取并构建前端，再复用后端可回滚发布脚本；
  后端成功后才同步静态资源，并在 Nginx 配置校验通过后重载服务。
- 前端依赖明确使用 `npm install --package-lock=false`，因为仓库未提交 lockfile。
  静态资源以 `rsync --archive --delete --delay-updates` 发布；该方式清理过期文件，
  但不是跨文件原子切换，短时间内可能出现新旧资源混合响应。
- 脚本不创建、迁移、停止或删除 MySQL、数据库卷、Docker 网络或无关容器。Nginx
  从 `/var/www/porsche-web` 提供 SPA，并将 `/api/`、`/v1/`、`/admin/` 和
  `/health` 反代至 loopback 应用；不带尾斜杠的 `/api`、`/v1` 与 `/admin`
  也保留为后端路由，避免被 SPA fallback 吞掉。
- 验证证据：`bash -n deploy/production-deploy.sh deploy/test-production-deploy.sh
  deploy/restart-all.sh deploy/test-restart-all.sh`、`bash deploy/test-production-deploy.sh`、
  `bash deploy/test-restart-all.sh`、`bash deploy/nginx/test-aiportcloud-conf.sh`、
  `GOCACHE=/private/tmp/porsche-go-build-cache go test ./... -count=1`、
  `GOCACHE=/private/tmp/porsche-go-build-cache go vet ./...` 与 `git diff --check`
  于 2026-08-26 通过。Go 全量测试在受限沙箱中因禁止绑定 `[::1]` 临时端口失败，
  在允许 loopback listener 的执行环境中复跑后全部包通过。

`go-004` 的真实 JieKou 目录、Chat 与 SSE 冒烟仍需部署环境的白牌配置；上述
部署编排验证不替代该上游验收，故其状态保持 `blocked`。

## 认证生产域验收部署工具（2026-09-01）

- 新增内部 Redis bootstrap、显式确认的认证 schema 迁移、候选部署和 manifest
  回滚入口。部署只接受两个指定 feature 分支的干净远端一致 checkout，不切换或
  reset Git；失败会恢复旧应用容器和已变更的静态文件，数据库迁移不会自动回滚。
- Shell 行为测试改为在无网络、无 Docker socket、只挂载临时 fixture 的
  `bash:5.2` 容器内运行目标脚本；结构化 argv 日志仅用于行为断言，不再依赖手写
  Shell 词法扫描器作为安全边界。
- 已验证新旧 Shell 回归、Nginx 静态检查、`go test ./... -count=1`、
  `go vet ./...` 与 `git diff --check`；空/短 Redis 密码、错误确认、错误分支、脏
  checkout、远端 SHA 不一致、候选健康失败、rsync/reload 失败均被隔离夹具拒绝或回滚。
- 浏览器生产域验收尚未执行，`go-006` 继续保持唯一 `in_progress`。

## 认证 Root 引导安全返工（2026-09-01）

- GORM SQL logger 已加泄露防护，Root bootstrap 凭据、环境值和其派生的敏感参数不应写入 SQL 日志。一次性 Root wrapper 只从已验证、远端一致的 feature SHA 创建 Git archive，并以构建返回的不可变 Docker image ID 执行，不依赖可变标签或工作树中的未跟踪输入。
- Root runtime environment startup path 已退役：生产服务拒绝任何 `ROOT_BOOTSTRAP_` 声明并且绝不自动引导；唯一流程是 root-controlled one-shot wrapper。Root credential 与 `/opt/Porsche/.env` 的 snapshot 源路径 metadata 校验只属于该 wrapper：凭据文件及其父目录、backend 目录和 `.env` 必须通过属主、权限、non-symlink 校验；复制到私有 `0700` snapshot 后才以只读 mount 传入 disposable `--rm` bootstrap 容器。
- 候选 deploy 与 manifest rollback 在任何 npm/build、Nginx、container stop/remove/rename、rsync 或 static write 前，均 fail-closed 扫描 `docker ps -a` 中 exact `ai-gateway-go` 与 `ai-gateway-go-acceptance-rollback-<digits>` 的运行/停止容器；rollback manifest target 未出现在列表时也会显式 inspect。`ROOT_BOOTSTRAP_` 命中和 inspect/list 失败均不回显值并拒绝继续；fixture contract 覆盖 current/stopped rollback、inspect/list error、unrelated helper 与 stdout/stderr/argv 脱敏。
- 已以 disposable no-network fixture containers 验证 `docs deploy rollback`：当前/停止 rollback 容器的 Root key、inspect/list failure、manifest target 的显式 inspect、helper 排除和敏感值不进入 stdout/stderr/argv 均通过；`bash -n`、`jq` 的唯一 `in_progress` 检查与 `git diff --check` 也通过。真实 test machine 的 one-shot/bootstrap、candidate deploy 及 browser acceptance 仍待受控生产域/隔离依赖环境执行，不能据此把生产验收标为 passing。`go-006` 保持唯一 `in_progress`。

## 认证验收部署最终加固（2026-09-02）

- 候选镜像与一次性 Root 引导镜像均只从已验证远端 SHA 的 `git archive` 私有构建上下文生成；活工作树中的未跟踪、忽略文件和 `.env` 不会进入镜像。生产配置、迁移、Root wrapper 与候选部署在任何构建或容器写操作前统一拒绝任意 `ROOT_BOOTSTRAP_` 环境声明，包括空值和未知后缀。
- deploy/rollback 使用同一部署锁，在破坏性切换前复扫运行及停止容器，并将 stop/remove/rename/start 与失败恢复绑定到校验后的裸 64 位 Docker container ID。候选返回畸形 ID、环境 inspect 失败、健康检查失败，以及 rollback 的 rename/start/rsync/Nginx reload 失败均由隔离 fixture 注入验证；失败路径恢复原应用与静态快照，不按可变名称误操作容器。
- 最终验证通过：完整 `bash:5.2` 无网络、无 Docker socket fixture，既有 production/restart/Nginx 回归，`bash -n`、`jq empty feature_list.json`、`git diff --check`、`GOCACHE=/private/tmp/porsche-go-build-cache go test ./... -count=1` 与 `go vet ./...`。真实测试机的候选部署及浏览器生产域验收仍是下一步，`go-006` 保持唯一 `in_progress`。

## 下一步（部署冒烟）

在具备部署环境的白牌配置后，完成真实上游目录、Chat 与 SSE 冒烟。

## 认证会话列名修复（2026-09-02）

- 测试机 Root 登录沿服务链定位到 `SessionService.Create`：MySQL 返回 1054，
  GORM 将 `Session.SID` 推导为 `s_id`，但已应用的 0002 migration 正确创建的是
  `user_sessions.sid`。模型现显式声明 `column:sid`，不修改生产数据库以兼容错误列名。
- 新增 GORM schema contract 回归测试，修复前稳定得到 `s_id` 并失败，修复后定向
  models/service 测试、全量 `go test ./... -count=1`、`go vet ./...` 和
  `git diff --check` 通过。仍需在测试机重新部署候选并完成 Root 登录/登出浏览器验收，
  因此 `go-006` 继续保持 `in_progress`。

## 前端发布权限修复（2026-09-02）

- 测试机在调用者 `umask 077` 下重新部署后，Nginx 对 `index.html` 返回
  `open() failed (13: Permission denied)`，导致 `/`、`/profile` 与 `/api-keys`
  等 SPA 路由统一返回 403。现场已将静态目录恢复为 `0755`、普通文件恢复为
  `0644`，源站和公网路由重新返回 200。
- `auth-acceptance-deploy.sh` 与常规 `restart-all.sh` 现在都在发布前规范化私有
  stage tree 的目录/文件权限，不再继承调用者或构建工具的 restrictive umask。
  两套行为测试均以 `0700` 目录和 `0600` 文件复现 RED，并验证发布后分别为
  `0755` 与 `0644`。

## 用户注册管理一期生产验收完成（2026-09-02）

- 测试机已完成 MySQL 8 migration 0002、认证 Redis、一次性 Root 引导和候选部署；
  Root login/self/refresh/session revoke/logout 的真实 HTTP 链路通过，刷新保持登录，
  退出后受保护请求返回 401，活跃 Root 会话计数为 0。
- Cloudflare 已强制 HTTP 跳转 HTTPS；Nginx 只从 Cloudflare 官方代理网段接受
  `CF-Connecting-IP`，应用只信任实际 Docker gateway `172.19.0.1/32`，浏览器
  当前设备展示的 IP 与真实客户端 IP 一致。源站及公网 health 均正常，SPA 首页、
  profile 与 api-keys 路由恢复为 200。
- 最终完整 Shell 隔离矩阵、production/restart/Nginx 回归、Go 全量测试、
  `go vet ./...`、`bash -n`、JSON 和 diff 检查通过。`go-006` 更新为 `passing`；
  当前没有 `in_progress` 条目。

## Agent 说明同步（2026-09-02）

- 对齐已发布的用户名认证、可撤销会话、RBAC 和固定白牌上游架构，更新五个 `.codex/agents/*.toml` 及 `docs/agents/domain.md`；角色名称、模型、推理强度、sandbox 模式及配置键保持不变。
- 移除旧架构引用与 SQLite 测试假设，补充 MySQL 8/Redis 隔离、one-shot Root、GUID/显式迁移、最终 diff 安全审查、凭据保护及生产/验收部署回滚边界。缺少集成 fixture 时必须明确报告跳过，不能算验收通过。
- 初次编辑保留了本地旧 main 的既有修改。用户随后明确要求推送远端 main，因此从 `origin/main` 的 `90abbdc49513039fa9218a6ce72d3147fbf1721e` 创建隔离提交，只收录本次六个说明文件、实施计划和本节记录；不包含遗留文档提交、旧 PRD 或其他无关文件。
- 最新 main 基线及修改后的 `GOCACHE=/private/tmp/porsche-go-build-cache bash ./init.sh`、`go test ./... -count=1`、`go build ./...`、`go vet ./...`（均使用该 GOCACHE）和 `git diff --check` 验证通过。TOML 校验器确认 5 个文件可解析、非说明设置与 Git 原件一致、34 个代码路径存在、文档链接有效；负向探针拒绝损坏 TOML 和 sandbox 模式变化。
- 未修改业务代码、数据库规范或业务功能状态；未执行生产操作、读取生产凭据或启动 Agent。语法/静态检查不代表当前会话已重新加载角色，也不代表模型执行行为已验证。计划见 `docs/superpowers/plans/2026-09-02-agent-guidance-refresh.md`。
