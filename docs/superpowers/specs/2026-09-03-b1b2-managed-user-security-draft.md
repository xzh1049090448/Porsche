# B1-B2 管理用户安全变更设计

## 状态与范围

**状态：AGREED_FOR_IMPLEMENTATION。** Root 与 PM 已冻结本切片的输入、比较、错误和事务合同；仅限本地 TDD 与独占 fixture 验证，不授权生产部署、commit 或 push。

本切片只收紧既有 `PUT /admin/users/:guid` 的输入与既有 `AuthService.UpdateManagedUser` 的会话安全边界。它不增加 HTTP 路由、权限覆盖 writer、ticket、outbox、迁移或表结构；也不接入前端。

## 已确定的安全契约

当 `status`、`plan_type` 或 `allowed_models` 的有效授权语义与被锁定 target 的持久化值实际不同时，服务必须按 actor、target 的 `FOR UPDATE` 锁顺序并经既有 `CanManageUser` 重授权，在该 MySQL transaction 内、任何 SQL 安全变更之前写 Redis session deny barrier，再逻辑撤销 target 的每个有效 session、递增 target `auth_version`、更新用户行，并写入安全审计。Redis 不是 MySQL 原子成员；SQL/审计事务失败经过验证会回滚 MySQL 行，已写 Redis barrier 可以保留。commit 返回错误时必须返回 nil 与固定 503，但提交结果可能未知；在有 operation 查询或幂等合同前，调用方不得假定未执行或安全重试。

现有 `revokeUserSessionsLocked` 是会话锁定、Redis barrier 和每会话撤销审计的唯一复用点。新增固定 `AuthAuditEventManagedUserUpdated = 10`、稳定字符串 `managed_user_updated` 与反向 `ParseAuthAuditEventType` 分支；它在每个有效 managed-user 修改中恰写一次，`UserID=target.ID`、审计 actor 为 actor.ID、`SessionGuid=nil`，不存储 token、cookie、before/after、原因 JSON、IP 或 UserAgent。会话逐条事件保持 status disable 的 event 7、其他撤销的 event 6，event 10 不按 session 重复。

同值 `status`、`plan_type`、ACL、daily limit 不产生 UPDATE、TouchAudit、AuthVersion 变化、Redis 写入或 event 10；但请求仍先完成 actor/target 锁定和授权。ACL 比较按精确字符串集合：顺序和重复项不形成权限变化，`nil` 与空数组的授权语义相等；比较使用归一化副本且保留实际输入顺序。真正 status/plan/ACL 变化一次请求只递增一次版本并撤销；即使没有 session 也必须写一次 event 10。enable 永不复活旧 session。

`UpdateManagedUser` 在局部接受 actor 仅为 Admin/Root、target 仅为 User/Admin/Root，再调用既有层级保护；ordinary、disabled、self、equal、Root target 和未知持久化角色全拒绝。actor 或 target 的 `AuthVersion <= 0` 拒绝，安全变更在 `INT32_MAX` 拒绝且不先写 Redis。`revokeLocked` 的 session 行和对应 revoke event 都以 actorID 写 `updated_by` / 审计字段，而 event 的 `UserID` 保持 target；self revoke 的 actor=target 结果不变。

`daily_call_limit` 是已有额度字段：非同值仅更新账户与写 event 10，不撤销 session、不改变 AuthVersion、也不改变既有 0 的额度计算。它不是金额 Mock 能力，也不表示细粒度权限新版已经完成。`{}` 和每个字段显式 JSON `null` 都是 no-op。

## 严格 PUT JSON 契约

`PUT /admin/users/:guid` 保留 200 DTO，guid 必须为正 int64。它只接受最大 64KiB 的一个 JSON object，字段精确为小写 `status`、`plan_type`、`allowed_models`、`daily_call_limit`。空 body、`null`、数组、重复键（含转义后同名）、未知键、大小写别名、尾随第二 JSON、类型错误均返回固定 400；超限返回 413。它不得继续使用忽略错误的 `ShouldBindJSON`。

解析从 `admin.go` 移至小的 `admin_user_update_decode.go`：逐项读取顶层 object key 到 `map[string]struct{}` 识别重复和未知字段，保存 raw JSON value，再解码已验证 object；读到 `}` 后必须再读取一次并要求 `io.EOF`。handler 只把已验证 string 转为 `models.ParseUserStatus` / `models.ParsePlanType`。非法枚举返回 422：status 只接受 `active` / `disabled`，plan 只接受 `free` / `professional` / `enterprise`，不 trim 或做大小写转换。非 null `allowed_models` 必为 string 数组，拒绝 null 项和空字符串项，不 trim/查实时目录，`[]` 仍是现有“无用户 ACL 限制”。非 null `daily_call_limit` 必为 0..2147483647 的 JSON 整数，拒绝小数、指数、负数和溢出。service 对 typed input 也执行同样 enum/range/ACL 验证，防止内部调用绕过 handler。

## 验证边界

服务真实 MySQL/Redis 测试首先证明当前缺陷：管理员将 target `plan_type` 从 `free` 改为 `professional` 后，已签发 session 仍可由 `SessionService.Validate` 验证、`AuthVersion` 未变且没有安全审计。实现后，同一测试必须证明该 session 逻辑撤销、Redis barrier 存在、`AuthVersion + 1` 且审计完整；同值 plan/ACL 测试的预期在 PM 冻结后固定。

handler 测试使用真实 Gin route 与认证 state，分别提交重复 `plan_type`、未知字段、trailing JSON、根数组和类型错误 JSON；每种都返回固定 400/413/422，且目标用户、session、AuthVersion 与审计均未改变。测试还覆盖 plan/ACL/status/mixed 一次版本变化、limit-only、同值/重复 ACL/no-op、零 session event、旧 access/refresh 拒绝与新登录可用、Redis/SQL/audit/commit 失败 503、并发同值一次 event/version，以及 Root/self/equal/ordinary/disabled actor 层级保护。所有持久化测试使用当前独占 `TEST_DATABASE_URL` 和 `TEST_REDIS_URL` fixture，绝不读取 `.env` 或连接生产数据库。
