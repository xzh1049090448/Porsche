# B1-D 新隔离 fixture 生命周期计划（未执行）

2026-09-04。B1-C 资源已销毁，其旧授权和凭据不复用。本计划待本轮授权，当前无新容器/库/迁移。

精确 task label `codex.task=b1d-admin-users-read-260904`；容器名 `b1d-admin-users-read-260904-mysql`、`b1d-admin-users-read-260904-redis`；数据库 `porsche_b1d_admin_users_260904_test`。先核验这两个 name/label 是否存在，冲突即停，不能接管别的 fixture。

仅本地已有 `mysql:8.0` 与 `redis:7-alpine`：授权执行前用 `docker image inspect <精确tag> --format '{{.Id}}'` 固定 image ID，`--pull=never`，禁止自动下载。0700 `/private/tmp/porsche-b1d-private-<mktemp随机后缀>` 私有目录内生成 0600 独立随机密码、mysql-client.cnf、redis.conf、fixture.env（仅 TEST_DATABASE_URL/TEST_REDIS_URL）；不输出 secret/连接串，不读取生产 .env。

MySQL：`docker run --detach --rm --pull=never --name b1d-admin-users-read-260904-mysql --label codex.task=b1d-admin-users-read-260904 --publish 127.0.0.1::3306 --tmpfs /var/lib/mysql:rw,nosuid,nodev --tmpfs /tmp:rw,nosuid,nodev`，只读挂载本批密码/client配置，使用 MYSQL_ROOT_PASSWORD_FILE、MYSQL_ROOT_HOST=%，在这个新容器创建上述精确测试库。Redis：相同任务 label、`--rm --pull=never --publish 127.0.0.1::6379 --tmpfs /data:rw,nosuid,nodev`，只读本批配置，随机 requirepass、禁持久化。没有 named volumes，不连接外部上游。

记录精确容器 IDs、image IDs、task label、127.0.0.1 动态端口与数据库名；exact-ID inspect 仅选必要字段，禁止宽泛输出环境。有限时等待就绪，再验证 SELECT DATABASE() 与 MySQL8/Redis7。

编译现有 cmd/migrate 到本批私有目录，在无 .env 的该目录为 cwd 运行，仅 subprocess 显式设置本批 DATABASE_URL、APP_ENV、SNOWFLAKE_NODE_ID；执行现有 0001/0002/0003，无新 migration。保存脱敏 ledger/checksum；迁移变量不得传给 tests。

测试环境先 unset APP_ENV/SNOWFLAKE_NODE_ID/DATABASE_URL/REDIS_URL/RUN_START_COMMAND，再静默加载本批 TEST_*。顺序：focused service/handler B1-D（不含性能）、同范围 race；窗口交接给独立 QA 后按协调顺序执行 serial 全量 fresh JSON、build/vet/diff。既有 internal/migration/permission_schema_test.go 与 internal/service/mysql_root_test.go 会创建/清理各自唯一命名子库，本次授权须包括这些仅位于新 MySQL 容器中的子库生命周期。禁止并行运行会互相清表的全量套件。

性能独立最后运行 `B1D_RUN_PERFORMANCE=1 go test ./internal/handler -run '^TestAdminUsersReadPerformance$' -count=1 -v`；路径 internal/handler/admin_users_read_performance_test.go。它在本批数据库新增 100000 个独立合成 User（500/批），经真实注册路由、中间件、服务及 MySQL/Redis 测量 page_size20，10 并发共 200 次请求，记录首次应用读、warm P95<=500ms、OS/架构/逻辑核、Docker CPU/内存限制及实际 MySQL/Redis版本。种数已暖数据库，不假称首次请求是磁盘冷缓存；真正磁盘冷样本仍 NOT_RUN，另需明确冷启动方法。性能期间不跑其它测试，合成数据随本批容器整体清理，不删除任何共享数据。

保留 fixture 给独立质量实测；Root 确认所有验证结束后核对 exact IDs/name/tasklabel，再 `docker stop <exact_mysql_id> <exact_redis_id>`，AutoRemove；核验 IDs 不存在。只 unlink 本批私有密码/配置/env/二进制与 runner、rmdir 本批目录；证据脱敏保存仓库。禁止 volume rm/down -v/prune、通配清理、cache清理及其它容器操作。

请求授权范围：上述一次性容器/精确库、现有迁移0001–0003、隔离测试及其 owned 子库、100k合成用户性能测试、独立复核窗口、最终 exact-task 容器/私密文件清理。当前仍 NOT_RUN，不因编译/skip 宣称验收通过。

已于 2026-09-04 只读核验本机 image IDs：MySQL `sha256:7dcddc01f13bab2f15cde676d44d01f61fc9f99fe7785e86196dfc07d358ae2b`；Redis `sha256:ff02b58f971e7d7d156a1267e283fcbbeee91773b6aa36c49dac28ecfe28eadf`。执行前再次精确核对。测试 owned 子库模式为 `porsche_permission_<24hex>_test` 和 `porsche_root_<32hex>_test`，仅成功创建后登记并清理精确名字，非通配删除。
