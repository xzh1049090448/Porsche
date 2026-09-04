# B1-D 管理用户只读实施计划 r1

> 执行者使用 TDD、verification 技能。Root/后端 PM 已确认上述设计 AGREED_FOR_IMPLEMENTATION；本计划不增加用户 gate。

目标：执行同日期 b1d-admin-users-read-design.md 的完整合同。BE 唯一 writer，保留 B1A–C dirty；不写 FE，不提交/推送/部署，不读生产 .env，不建 schema/fixture。基线 init 已在明确 unset 上批临时变量后通过且文件未变化。

1. **合同与跟踪**：归档设计/计划，通知 Root 供 PM 并行只读审查；feature_list.json 增加 go-016 唯一 in_progress。明确列表/详情是从 B2 提前交付的子集，不完成 B1 全部写链。
2. **纯 TDD**：新增 query parser、固定 UserReadDTO、literal LIKE、canonical GUID、排序回退/分页范围测试；先运行证明缺少实现 RED，再实现并 GREEN。保留 LastLoginAt 的真实字段及 null，拒绝未知/重复/畸形 query。
3. **服务事务**：新增共享 fresh identity 事务边界，actor→target→session→Redis、root pool/READ COMMITTED/静音、严格 policy、commit 后交付。新增列表单 CTE、详情和 legacy behavior 服务。数据库测试覆盖 role/policy/deleted/hidden/corruption、失效与 Redis、外部事务/commit 失败、用户锁和完整分页 snapshot。缺 fixture 明确 SKIP/NOT_RUN，不弱化断言。
4. **HTTP 接入**：新 RegisterAdminUsersRead 路由与 requestID/no-store；替换旧三 GET 为适配器，保留原数组和 behavior；旧写/其他管理 GET 不动。新增新旧无权限、查询、隐藏、上限及鉴权前 headers 测试。
5. **认证投影**：同一构造器处理四来源；内部签发 proof 与 middleware proof 均做 fresh identity。正常固定顺序投影，只有 policy 不可用时双字段省略；self/me fresh 对象。login/refresh 在 cookie/access 返回前复核，401/503 无新 cookie/access且不清旧 cookie；记录已签发会话不可伪回滚局限。TDD 覆盖各 role、坏 policy、失效、commit/cookie 边界。
6. **验证与审查**：运行带显式 unset 临时环境的 focused、必要 race、serial 全量 JSON、build/vet/diff；归档真实命令、PASS/SKIP/FAIL 与 hash。新 fixture 提交独立计划，不执行旧许可。更新 progress/session-handoff/报告，保留性能 NOT_RUN 与旧管理写链风险；交给 Root 安排 PM 规格复核及独立 QA，等待实际证据后才可改 passing。

重现命令前提：unset TEST_DATABASE_URL TEST_REDIS_URL RUN_START_COMMAND APP_ENV DATABASE_URL REDIS_URL SNOWFLAKE_NODE_ID；仅有当前授权的真实 fixture 才显式加载其 TEST_*。先 `go test ./internal/service ./internal/handler ./internal/dto -run 'AdminUsersRead|AuthProjection' -count=1`；之后对应 race 和 `go test -p 1 ./... -count=1 -json`、`go build ./...`、`go vet ./...`、`git diff --check`。不启动服务、不迁移，不将归档 .go 探针留在包扫描范围（使用 .go.txt）。
