# B1-B1 权限策略快照设计

## 目标

为已认证账户提供一个只读、fail-closed 的 MySQL 权限策略快照加载器。它将 B1-A 的纯内存 `authz.Evaluator` 与持久化 policy head / override 记录连接起来，但不接入 HTTP、DTO、角色修改、权限写入或前端。

## 数据模型与迁移

新增前向 `0003_permission_policy`，不修改已应用的 `0001` / `0002`。`user_permission_heads` 是每个用户永久唯一的策略标记；`user_permission_overrides` 保存版本化、整数编码的规则历史。两表均使用内部 `id`、雪花 `guid`、四个 Unix 毫秒审计字段及 `INT is_deleted`，仅以 `user_id -> users.id` 关联。

head 的 `policy_version >= 1`、`catalog_version == 1`、`rule_count` 在 0–24；有效行必须是相同版本、非删除、唯一 capability 且可由 B1-A 评估器接受。`is_deleted=1` 是历史，永不作为有效授权。forward-only down migration 不删除表或迁移记录。

## 读取契约

`LoadPermissionSnapshot(ctx, rootDB, userID)` 仅接收根 `*gorm.DB`（其 ConnPool 必须为 `*sql.DB`），拒绝外部 transaction、connection 或包装池。它以自己的 `READ COMMITTED` transaction 读取，并在 commit 前以 `FOR SHARE` 锁住 `users` 行。

用户不存在、状态/角色/删除标志/AuthVersion 无效均返回 actor unavailable。无 head 只在该用户完全没有任何 override（包括软删历史）时构造 policy version 0 的 baseline；有 head 时，软删/非法 head、孤儿规则、版本或计数不符、非法 capability/effect、不可授权规则全返回 policy invalid。DB、锁、context 或 commit 错误只返回 read unavailable 且不返回 snapshot。

返回的 snapshot 仅表示加载时刻的 evaluator、AuthVersion 和 policy version；它不是会话有效性证明，也不能替代未来写事务重新获取 `users FOR UPDATE`、head、rules 的授权判断。

## 验证

迁移在新建 MySQL 8 `*_test` 库中验证 columns、signedness、null/default、索引、FK、InnoDB、重跑、ledger 与 partial-DDL 不入 ledger；旧 0002 数据经 0003 保留。loader 覆盖 baseline、有效空策略、有效覆盖、所有损坏路径、事务/上下文/读错误、外部 transaction 拒绝和双连接锁顺序。所有持久化回归使用任务独占的 MySQL fixture；Redis 仅用于全量认证回归。

## 发布与回滚门禁

0003 落库后，旧二进制会因精确 migration ledger 校验拒绝启动；新二进制也会拒绝缺少 0003 的旧 schema。本变更不授权生产迁移或自动回滚。恢复必须使用另行审核的前向方案，且不得删除 policy history、表或 migration ledger。
