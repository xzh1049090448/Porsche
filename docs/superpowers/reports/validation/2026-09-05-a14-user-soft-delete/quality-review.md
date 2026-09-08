# A14 users.delete 独立质量复审

日期：2026-09-06
后端候选：`811213d557eea7b6b9523a584245252ba4dd7d80`
前端候选：`bace6d167b94b693abd6be4c720152dc0eb905bb`

范围：Task 18 Step 4 整改复审。只读检查实现、联合验收草稿、fixture lifecycle、cleanup manifest、私有证据摘要和现场资源；仅更新本报告。未 reset 或写 fixture，未停止进程、清理、提交、部署、迁移或 push，未读取或输出凭据值。

## 原 Findings 关闭状态

### CLOSED — 密码输入允许自动填充

原 P1 已关闭。前端提交 `96c0708` 将真实密码字段从 `autocomplete="current-password"` 改为 `autocomplete="off"`；提交 `bace6d1` 增加挂载后的 ownership-guarded 清理，同时清空 Vue form、Element Plus 内部原生 input 和 coordinator 私有密码。合同测试不再固化旧行为，并覆盖打开、关闭、remount、native input 与既有 store ownership。

私有可见浏览器证据进一步通过真实 `UserSoftDeleteDialog` 验证三个成功流程和 verifying/submitting/querying 生命周期，均确认 autocomplete off、原生密码已清空、重新打开字段为空且 store 安全。当前候选 focused 测试 fresh rerun 为 64 PASS、0 FAIL、0 SKIP。

### CLOSED — 敏感状态 Playwright 绕过真实 UI/store/transport

原 P1 已关闭。新 `/tmp/playwright-test-a14-real-sensitive-lifecycle.js` 未导入或直接创建 workflow；它通过生产 Vue 页面、真实对话框、Pinia store 和 production adapter 进入 verifying、submitting、querying，只在浏览器网络边界延迟、丢失或控制响应。证据记录了各阶段实际 Issue/Execute/Query 请求类型和数量，并在 reload/close 后检查 DOM、重新打开输入、store、storage、URL、console、原生密码值和外部请求。

`sensitive-ui-lifecycle.json` 三项均为 `visible_real_ui_pinia_production_adapter` PASS；querying 用例先由真实后端提交，再在响应边界制造未知结果并只 Query。`ui-recovery.json` 另外区分真实 commit-lost-response 与纯网络边界的 processing、pending、401、network ambiguity，没有把受控响应描述为后端事实。报告的证据层级现已准确。

### CLOSED — 应用 PID/监听器与 exact cleanup 生命周期

原 P1 已关闭。文档诚实保留 PID `4020`/`4045`/`4084` 随 agent lifetime 消失的第二段历史，并明确不领取 Step 6 cleanup credit。root-held exec sessions 现在持有唯一 accepted live identities：backend PID `10318`/`127.0.0.1:57181`、route bridge PID `10331`/`127.0.0.1:8000`、frontend PID `10376`/`127.0.0.1:55795`。

独立现场只读检查确认三个 PID 的完整 command line、启动时间和 listener ownership 精确匹配；backend `/health` 与 frontend root 均返回 200。两个 replacement fixture 容器的完整 ID、名称、image ID/RepoDigest、task/retention labels、`AutoRemove=true`、read-only rootfs、running/healthy、tmpfs 数量与值、零 mounts 和唯一 loopback binding 也全部匹配。

`cleanup-manifest.md` 现将 42 个 preflight `test` 全部放在第一个 `kill`、`docker stop` 或 `rm` 之前。直接执行同一 code block 截止首个 `kill` 的只读部分返回 exit 0，证明当前 preflight 可执行。shell syntax check PASS；首个 mutation 位于全部 preflight 之后。清单逐项列出 60 个唯一 `/tmp` 路径，无 glob；现场集合也是 60 个，missing、stale 均为 0。Step 6 仍需在真正执行后补写零残留结果，本复审没有提前授予 cleanup 完成状态。

## 复审通过的质量证据

- 后端错误映射仍封闭：只有 `operation_commit_unknown` 可返回 operation reference；限流和 processing 的 Retry-After 边界清晰；依赖错误统一为脱敏 503。当前无 fixture focused service/handler rerun PASS。
- Execute 的 verification 消费、consumer、管理审计、outbox 和 terminal operation 处于同一 MySQL 事务；业务冲突通过 savepoint 回滚 consumer 后写 terminal failed，写故障回滚整个事务。真实 fault matrix 对 session、token、policy、user、auth audit、management audit、outbox、terminal write 的直接回读足以排除部分提交假阳性。
- one-shot execution capability 阻止 Execute 重放；commit acknowledgement 不确定只返回安全 public ref，并通过原 actor/session/scope/key Query 解析。pending recovery 明确只有显式 primitive，本切片未虚构自动 worker。
- replacement fixture 采用新 suffix、独立 `_test` child、Redis DB 8、immutable images、loopback、read-only rootfs、tmpfs、mode-0600 私有文件和 mode-0700 exact reset helper。lifecycle 将旧 AutoRemove 资源因 host restart 消失与新 fixture 创建分开记录，未复用旧身份。
- 所有当前 authoritative artifact 均存在、mode 0600，SHA-256 与 joint draft 一致。重新统计 backend full 为 1395 PASS、0 FAIL、1 明确 performance SKIP；race 为 319 PASS、0 FAIL、0 SKIP。
- 三个整改期 invalid harness run 都被保留并正确归因：DDL authority 缺失的 empty-schema run 有 180 leaf FAIL/3 package FAIL；漏导出 Redis 的 exit-zero run 有 238 SKIP，未被当作完整 gate；错误前端合同环境变量调用保留独立日志。随后 corrected gate 使用独立 reset/migrate 和正确环境，不用后续绿色覆盖历史记录。
- `final-readback-sanitized.json` 对三个真实 UI 删除别名分别证明一个物理墓碑、disabled、session/token revocation、policy tombstone、两类 audit、succeeded outbox、succeeded terminal operation、规范化原因匹配和敏感身份清理。artifact 只含 alias、不可逆 target hash、计数和布尔值。
- post-delete credential artifact 对三个目标共 15 项 Access/session/Refresh/Gateway/login 拒绝均为 401；UI eligibility、成功、恢复和生命周期 artifacts 均保持 UI、API context、network-boundary 和数据库证据层分离。
- 前端 ownership token、generation、abort/cancel、late success/conflict、同 GUID reopen 和 route/page context 测试对状态/store race 有实质覆盖。新增 mount clear 也受 ownership 守卫，不会清空后来打开的对话框。
- 当前变更保持专用 adapter/state/store/component 边界，未增加通用 action dispatcher 或额外生产 action；复杂性主要集中在严格合同和一次性安全流程，维护边界与设计一致。

## Verdict

三个原 P1 均已关闭。质量层允许继续进行其余独立复审、限定的 acceptance 状态更新和计划中的 exact cleanup；cleanup 只有在实际执行并记录零残留后才能标记完成。

PASS
