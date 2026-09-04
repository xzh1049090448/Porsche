# FE+BE 真实联合验收环境交接

日期：2026-09-04。最终状态：`FINAL_ACCEPTANCE_FAIL_ARCHIVED_AND_CLEANED`。这是一次性、本机loopback、非生产环境；没有修改业务代码，没有commit/push/deploy，也没有连接生产服务或数据。

## 候选基线

- Backend：HEAD `aec1619ee710c80cd71dbe529660e2d12b3fda7b`，branch `feature/admin-public-260903`。H3 `internal/service/admin_users_read.go` SHA256=`7a20df0cb257feb393f930b0d006fc88795acbc0b9dead2784f5ae3b61d4f715`，0004 SHA256=`44b5caba0473162c239e6b3035e6d9067e3af0b35e998a79c827a1424621494e`。
- Frontend：HEAD `25a66a4ad546a481941dfc6e0cc9bc75da2e631b`，branch `feature/admin-public-260903`。完整dirty baseline及候选文件hash见同目录文件；联合环境未改变前后端业务文件。

## 资源与进程

- Task label：`codex.task=b1d-joint-acceptance-260904`。
- MySQL：`b1d-joint-acceptance-260904-mysql`，ID `868308ad13c424fc529e1b401eca1a13702af8adcf866711d5453111903e7b2a`，image `sha256:7dcddc01f13bab2f15cde676d44d01f61fc9f99fe7785e86196dfc07d358ae2b`，`127.0.0.1:61998`，AutoRemove，`/var/lib/mysql`及`/tmp`为tmpfs。
- Redis：`b1d-joint-acceptance-260904-redis`，ID `0f2bdaecb9e8da94e67667eea6487459c957ba62af469406aad41fa521d8f5f8`，image `sha256:ff02b58f971e7d7d156a1267e283fcbbeee91773b6aa36c49dac28ecfe28eadf`，`127.0.0.1:62001`，AutoRemove，`/data`为tmpfs。
- Named volumes：0。两个容器各自只有一个来自本批私密目录的只读配置/密码bind。
- Backend：`http://127.0.0.1:18080`，PID 22213，exec session 37608。
- Frontend：`http://127.0.0.1:15173`，PID 22388，exec session 23561。
- 私密目录：`/private/tmp/porsche-joint-acceptance-260904-td1FYO`，目录0700、凭据文件0600。密码、token、cookie和连接串未进入证据；独立QA从该目录的account index和login payload文件读取，不在聊天或报告中复制值。

Vite使用私密临时config；仅把`/api`和`/admin`代理到本地Backend。Backend使用私密env，仅允许`127.0.0.1,localhost` host及本地FE origin。两端源码未为环境做修改。

## 数据与迁移

任务数据库`porsche_joint_acceptance_260904_test`已fresh应用0001–0004，ledger checksum见`migration-ledger.txt`。一次性Root bootstrap成功。最小数据集：active Root、active Admin、active User、disabled User、soft-deleted User；注册密码均为随机值并仅在私密文件中。Root/Admin真实login已在Redis创建会话。100k数据本轮未加载；此前性能验收已单独完成，本联合UI smoke无需扩大数据集。

## Smoke结果

- FE根路由200，Backend health200。
- Root真实login200，收到HttpOnly refresh cookie；Bearer list200，total3（Admin、active User、disabled User）；Bearer detail active User 200。
- Admin真实login200，收到HttpOnly refresh cookie；Bearer list200，total2（active/disabled User）。
- Root deleted list200，total1且状态deleted。
- `/login`与`/users`的Vite SPA路由均200。

完整脱敏摘要见`smoke-summary.json`。原始login body、cookies、Bearer header仅保存在私密目录，不复制到仓库。

## 已保留的设置失败

见`setup-failures.md`。所有失败都发生在对应动作前或以安全拒绝结束：sandbox loopback探针被拒、bootstrap缺region、Root凭据多余空行、默认AllowedHosts导致health403、zsh未引用query URL。修正均只涉及私密环境/脚本，没有业务代码修复，也没有隐藏失败日志。

## Manifest链路说明

`independent-qa-h3/final-hashes.sha256`中的`26c063...`是独立QA开始时冻结的**输入manifest**，不是cleanup后的最终值。H3 exact cleanup完成后的manifest SHA256为`70427948ad19132f3ad4fc0a48c2da5a1d9276c6fda93589b5abb36691f70922`。本联合验收manifest以704279作为前置最终状态，并明确保留26c063作为历史QA输入，避免把旧值误称当前值。

环境恢复并由Root独立确认前后端均为HTTP 200后的归档前manifest SHA256为`d3d1de8b3609721baba3e1f04ca19ef5990aaad2666e63f9aab7786cb6101641`。本次最终签字文档更新后的当前manifest以同目录`manifest.sha256`为准，manifest本体不自引用。

## 最终PM/QA结论

PM已最终签字，独立QA结论为本地联合验收`FAIL`：26项中6项`PASS_LIMITED_SCOPE`（A01/A02/A04/A13/V01/V02）、2项`FAIL_LOCAL_DEV_ROUTE`（P02/R01）、18项阻塞（16项`BLOCKED_NOT_IMPLEMENTED`、P08 `BLOCKED_PRODUCT`、R02 `BLOCKED_ENV`）。

logout本身通过：204、浏览器上下文Cookie清空、显式refresh 401，且未渲染私有API Key DOM。失败点是私密Vite config的`/api`代理前缀截获了浏览器文档路由`/api-keys`，硬刷新在Vue路由守卫运行前由后端返回404。`SECOND-PHASE-REPORT.md`仅在该logout/direct-route早期解释上supersede `CORE-PHASE-REPORT.md`，两阶段原始证据均保留。

前端最终归档文件：

- `FINAL-ACCEPTANCE-REPORT.md` SHA256=`4147caaef7e745f49126f5689528f580dc8bb1b69e4c2c55d1e59e55324312f2`
- `acceptance-matrix.json` SHA256=`3acf4b8212f106b8a3f20dcc12f3d1b4768b70a3f40584fe7afe9e249c002a75`
- `prd-260903-confirmations.json` SHA256=`952bae579b7aa426fa95342f18d69b4cb1038a9b5e311a10db9f8186fd196d48`
- FE `progress.md` SHA256=`797c8f45d8579c2068d913ae4ada1be5df4c8523b0315e57c58e21f245e04046`
- FE `feature_list.json` SHA256=`ff47ef09f09b4a69ce8f6740eae85e266166b92352f9836d866633103c5e145a`
- FE `evidence-hygiene.txt` SHA256=`5247f49740161e8ef48862b1612b3a3818303b6faf98b7ceb567356abf4b0726`
- FE `final-archive-hashes.sha256` SHA256=`8d442a4992b5765d15f560578878ff48209e236107a5fe5cd86758e995e419d3`，保存上述关键文件的不可自引用哈希清单。

## 下一步与保留边界

ARCHIVE PASS后已按精确授权完成cleanup：仅终止FE session 23561和BE session 37608；两个PID及15173/18080监听均消失；两个容器完整身份预检通过后，仅对两个完整ID执行`docker stop`并由AutoRemove删除；仅删除精确私密目录。复查完整ID、名称、task label、15173/18080/61998/62001端口和私密路径残留均为0。原始预检、停止和复查证据见`cleanup-precheck.txt`、`cleanup-stop.txt`和`cleanup-verification.txt`。

修复本地路由/代理边界并重验P02/R01、真正disk-cold、生产迁移/部署、旧管理写入口及完整PRD验收均未由本环境判定PASS。不得复用本次已删除的凭据或fixture地址。
