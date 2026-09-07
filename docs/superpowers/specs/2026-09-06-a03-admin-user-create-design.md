# A03 管理端创建用户与管理员设计

日期：2026-09-06  
状态：设计已获用户批准；2026-09-07 按三审结论补充删除隐私、响应完整性、密码哈希顺序、分组 fail-closed 与审计结果契约
范围：PRD-260903 A03；不包含用户编辑、启停、密码重置、角色变更、金额 Mock、分组管理界面或生产发布

## 1. 目标与既定边界

管理端通过一个版本化接口创建普通用户或管理员。Admin 只能创建普通用户；Root 可以创建普通用户和管理员，但不能创建 Root。普通用户创建要求最新 `users.create` 能力和幂等键；管理员创建还要求 Root 使用当前密码取得 `users.create_admin` 单次动作票据。

创建结果必须同时满足：用户名永久唯一、默认启用、真实 `default` 业务分组、默认免费套餐、现有新用户模型和每日调用限制、管理员权限快照、脱敏审计、幂等结果与 outbox 原子提交。生产请求不得包含金额字段，也不产生真实余额。

当前 `CreateAdminIntent` 已覆盖用户名、昵称、原样密码、分组 GUID、套餐、模型范围和每日调用限制，但尚未覆盖权限覆盖；生产 registry 当前仅激活 `users.delete`。A03 会扩展管理员创建意图并单独激活 `users.create_admin`，不激活其他动作。

## 2. API 契约

### 2.1 创建接口

`POST /admin/v2/users`

请求头：

- `Authorization: Bearer ...`：沿用当前认证机制。
- `Idempotency-Key`：必须恰好一个，格式和保密处理沿用管理动作底座。
- `X-Action-Ticket`：`role=admin` 时必须恰好一个；`role=user` 时禁止携带。

请求体必须是一个 JSON object，拒绝未知字段、重复键、尾随值和超过限制的 body：

```json
{
  "username": "alice",
  "nickname": "Alice",
  "password": "initial secret",
  "role": "user",
  "group_guid": null,
  "plan_type": "free",
  "permission_overrides": []
}
```

字段规则：

- `username`：必填，复用 `NormalizeUsername`；创建后不可修改；软删除墓碑仍占用用户名。
- `nickname`：可省略或为 `null`；非空值 trim 后最多 64 个 Unicode 字符；空白字符串拒绝。省略不再用用户名伪造显示名，读取时返回 `null`。
- `password`：必填，保持原样，不 trim；复用现有 8–20 Unicode 字符和弱密码规则。确认密码仅在前端存在。
- `role`：必填，只允许 `user|admin`；`root` 和其他值拒绝。
- `group_guid`：可省略或为 `null`，表示服务端解析当前唯一、启用且未删除的 `default` 分组；显式值必须是 canonical 正十进制 int64 字符串并指向启用分组。
- `plan_type`：可省略，默认 `free`；只允许 `free|professional|enterprise`。
- `permission_overrides`：可省略，默认空数组；仅 `role=admin` 且操作者为 Root 时允许。每项为精确 `{capability,effect}`，`effect` 只允许 `allow|deny`；继承通过不提交该 capability 表示。能力必须存在、可授予、非 Root-only 且非 unavailable，列表按 capability canonical 排序并拒绝重复项。
- `allowed_models`、`daily_call_limit`、`status`、`auth_version`、金额、余额和内部 ID 均不属于公开写入字段。服务端使用现有新用户政策解析为 `allowed_models=[]`、`daily_call_limit=100`、active、`auth_version=1`，并把解析值纳入管理员创建意图。

额外授权：

- 所有创建都重新加载操作者、会话和权限快照，并执行 `Evaluator.Create(role)`。
- 显式非默认分组额外要求 `users.group.change`；非免费套餐额外要求 `users.plan.change`。
- `role=admin` 还要求 Root 和有效的 `users.create_admin` 票据。Admin 即使获得普通可授予覆盖也不能创建管理员。
- 任一依赖读取失败均 fail closed；前端能力投影只用于展示。

成功返回 `201 Created`、`Cache-Control: no-store` 和非空 `X-Request-ID`：

```json
{
  "operation_ref": "op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
  "user": {
    "guid": "123456789012345678",
    "username": "alice",
    "nickname": "Alice",
    "email": null,
    "group": "default",
    "plan_type": "free",
    "role": "user",
    "status": "active",
    "auth_version": 1,
    "created_at": "2026-09-06T00:00:00.000Z",
    "last_login_at": null
  },
  "permissions_version": null
}
```

管理员创建返回 `permissions_version="1"`；普通用户返回 `null`。`operation_ref` 同时用于提交结果未知时查询。响应永不返回密码、密码摘要、内部 user ID、票据、幂等键或权限表主键。

### 2.2 管理员创建票据

沿用 `POST /admin/v2/action-verifications`，启用 `action=users.create_admin`。intent 使用与创建请求相同的业务字段；服务端先做同一套规范化、默认解析和权限覆盖校验，再生成 HMAC。密码原始字节参与 HMAC，使用后立即清零；日志、数据库和错误不记录明文、普通 hash、长度或片段。

票据绑定 actor、当前 session、actor auth version、action 和完整 intent，有效期五分钟且只可消费一次。该动作的 target kind 仍为 `none`；验证阶段不生成或预留用户 GUID，不创建用户、不占用用户名。用户 GUID 只在最终创建事务中由共用雪花服务生成。创建阶段重新核验 actor 与权限，票据不能固化旧授权。

### 2.3 幂等与结果查询

普通用户创建和管理员创建都写入 `admin_operations`。A03 增加一个内部普通创建 action code 和 descriptor，其 `RequiresTicket=false`、`RootOnly=false`、capability 为 `users.create`；该 descriptor 只用于操作编排，不暴露动作验证入口。管理员创建继续使用 `users.create_admin`，`RequiresTicket=true`、`RootOnly=true`。

操作键按 actor/session/action/idempotency digest 唯一。请求 HMAC 覆盖规范化后的完整创建意图：

- 原 key、同 actor/session/action、同请求：终态成功返回原 `201` 结果，不再次创建或再次审计。
- 原 key、同作用域、不同请求：`409 idempotency_conflict`。
- 跨 session 重用：沿用 `409 idempotency_cross_session`。
- 成功提交后响应丢失：客户端用现有 operation query 查询；不得自动生成新 key 重做。
- 失败前未提交业务事务：返回稳定错误；票据消费和 operation 状态遵守现有恢复语义。

终态成功响应由 `admin_operation_responses` 保存。每个新活跃快照都保存正数 `target_guid`；该字段不随 `admin_operations` 到期时清除结果字段，因此 operation 到期后仍能定位全部隐私快照。目标账户仍活跃、operation 尚在可查询期且快照完整时，同一 actor/session/action/key 的相同请求精确重放原 `201`；已到期的创建请求稳定返回 `410 operation_expired`，不会读取或返回保存的成功正文。A14 软删除目标账户时，必须在同一数据库事务内按 `target_guid` 把全部创建快照改为受控脱敏态；之后直接加载任一快照均返回 `410 created_user_deleted`，已到期请求仍返回安全的 `410 operation_expired`，两者都不返回原 username、nickname、GUID、角色、分组或任何旧响应片段。删除事务任一步失败时，账户软删除与全部快照脱敏一起回滚。

快照使用 action-security 根密钥派生的独立 response key，以 HMAC v1 绑定 integrity version、lifecycle、operation ID/ref、action、terminal state、result kind、目标 GUID、HTTP status、media type 和完整 response body。为兼容 0009 已生成的 HMAC v1 字节，`target_guid` 的值继续编码在 HMAC payload 的 `result_guid` slot；直接修改 `target_guid` 同样无法通过验证。活跃 legacy v0 行不再允许重放；直接修改 target/body/digest/version/lifecycle 而不能同时生成有效 HMAC 时 fail closed。成功 outbox 是不随 operation 结果清理的创建标记；删除时 marker 与 snapshot 数量必须精确匹配，直接删除任一快照会因缺失而 fail closed；两者都为零才表示目标没有创建快照。

当前 HMAC v1 没有 key ID 或多 key verifier。只要仍有可能被重放或需要在删除时脱敏的活跃快照，就禁止轮换 `ACTION_SECURITY_HMAC_KEY`；必须先交付单独批准的 key ID/多 key 验证方案，或在同一受控事务中完成全量 re-HMAC migration。没有该生命周期支持的轮换会 fail closed，并会阻止依赖旧快照验证的删除。0009 和 0010 都不创建 trigger，也不要求 `SUPER`、`log_bin_trust_function_creators` 或其他生产服务器策略；应用是唯一受控脱敏写入者。

### 2.4 创建表单分组选项

`GET /admin/v2/groups?status=active` 返回当前操作者可读的启用、未删除业务分组，只包含字符串 GUID、稳定 key 和显示名；要求 `groups.read`，使用 `Cache-Control: no-store` 和 `X-Request-ID`。请求只接受唯一的固定 `status=active`，结果按 key 升序且 `default` 必须存在。依赖不可用、重复 default 或用户可见分组数据损坏时返回 503，不用硬编码选项掩盖问题。

该接口只服务 A03 选择器和后续管理页面，不提供创建、改名、停用或删除分组的写能力。若操作者有 `users.create` 但没有 `groups.read`，前端仍可提交省略 `group_guid` 的默认分组创建；选择器只显示不可变的“默认分组”说明，不请求或猜测其他分组。若操作者有 `groups.read`，目录 pending、error、空结果或当前选择不在有效 active catalog 中时均禁止提交，create/verify 请求数必须保持为零。

## 3. 数据模型与迁移

A03 新增前向迁移，遵守 `docs/conventions/database-standards.md`，不使用 AutoMigrate，不修改已发布 migration checksum。

实际基线在 A03 实施期间已由独立批准的响应快照迁移推进至 0008；安全修订新增 0009 和 0010。0009 为快照增加 lifecycle、integrity version 和 response HMAC，为 outbox 增加脱敏 failure code 与成功/失败结果约束。其 runner 探测并只接受四条 DDL 的已提交前缀：response ALTER、outbox column、幂等 backfill、outcome CHECK；在每个前缀崩溃后重跑会从下一安全阶段恢复并在 CHECK 前重复 NULL-only backfill，任意非前缀混合结构 fail closed。

0010 不修改 0009：它为快照增加 nullable `target_guid`，并为 outbox 增加 durable nullable `result_kind`。迁移只把 action、operation ID、public ref 完全相同且满足创建契约的成功 marker 分类为 `ResultUser`：活跃 operation 必须是未删除的成功 `users.create`/`users.create_admin`、ResultUser、正数且真实存在的 user GUID、201 与完整终态字段；已到期 operation 必须是已删除的 expired 终态、结果字段已清空，而 outbox 必须未删除、成功、无 failure、TargetNone、ResultUser、正数且真实存在的 user GUID。活跃路径仅从前一种 operation result 回填，已到期路径仅从后一种 outbox marker 回填；state、failure、action、ref、deleted、result kind、result GUID、operation ID 或 target contract 任一不匹配都不得关联目标。

无论旧 body、digest、HMAC 或目标是否可解析，每个既有 redacted 行都会先改成 lifecycle redacted、固定 201/`application/json`、规范 `{}` body、固定 digest 与全零不可重放 sentinel HMAC。未获得严格目标绑定的活跃行以及结构或摘要不完整的活跃行随后 fail-safe 脱敏；严格绑定但不满足重放完整性的行保留真实 target 以确保实际用户删除仍能定位它，但不保留 PII。最后为 outbox outcome 加入 durable result-kind 约束，并增加 `(target_guid,lifecycle_state,is_deleted,id)` 索引、指向 `users.guid` 的 RESTRICT 外键；每个新活跃快照必须有正数 target 和 HMAC v1，redacted 响应必须精确为固定 201/JSON/`{}`/digest。runner 探测 response column、两列完成、outbox CHECK 完成与最终 response DDL，九条语句的每个已提交前缀均可重跑，数据阶段保持幂等，混合结构 fail closed。迁移和 verifier 只依赖普通表级 DDL 权限，并保留 0001–0009 的既有 checksum。

新增 `business_groups`：

- 内部自增 `id BIGINT`、唯一雪花 `guid BIGINT`。
- `group_key` 为不可修改的稳定键；`display_name` 为展示名；`status INT` 使用稳定枚举 active/inactive。
- 四个 BIGINT Unix 毫秒审计字段和 `is_deleted INT NOT NULL DEFAULT 0`。
- seed 一个唯一 active、未删除的 `default` 分组；系统 seed 的 actor 审计字段使用 `NULL`。

为 `users` 增加 nullable `group_id BIGINT` 及索引/外键，关联 `business_groups.id`。迁移将全部未删除及墓碑历史用户回填到 `default` 后再收紧为 `NOT NULL`，避免列表/详情出现伪造分组。用户关联始终使用内部 ID；API 只接收/返回 GUID 或稳定 group key。

管理员账户创建时总是创建 `user_permission_heads` version 1，即使没有覆盖；只为 `allow|deny` 项创建 version 1 的 `user_permission_overrides`。普通用户不创建管理权限头或覆盖，因为硬边界禁止其获得管理能力。`rule_count` 等于有效覆盖行数。

本切片只提供 default seed、分组解析及用户读投影，不开放 `/admin/v2/groups` 管理写接口。分组创建、改名和停用仍由后续独立切片实现。

## 4. 原子事务与锁顺序

创建 consumer 在已有管理操作事务内执行，固定顺序为：

1. 锁定并重新核验 operation、票据（管理员创建）、actor、session 和 actor permission head/overrides。
2. 解析并锁定目标分组；重新核验附加 plan/group 权限。
3. 按规范化 username 查询包括墓碑的冲突记录；数据库永久唯一索引处理并发竞态。
4. HTTP 解码只保留 caller-owned `[]byte` 密码并完成字节级强度校验。在调用 `Operation.Begin` 前创建两个 backing array 完全独立的 owned byte slices：descriptor intent 独占一份并允许 Begin/encoder 清零，hash candidate 独占另一份。认证、action rate limit、角色/能力检查和幂等 terminal replay 先完成；replay、forbidden、rate-limited 与其他不可执行请求返回前执行零次 Argon2并清零适用 buffer。只有 `ExecutionReady` 的 fresh attempt 在事务外使用 hash candidate 哈希一次，随后清零两份明文与 hash；该路径及 action verification 不创建 plaintext password string。
5. 创建 user，写 group/plan/模型范围/每日限制和安全默认值。
6. 若为 admin，创建 permission head 与 canonical overrides。
7. 写目标账户的注册安全事实，并写管理创建审计：操作者使用内部 user ID 关联，action 精确为 `users.create` 或 `users.create_admin`；resource/detail 记录 terminal state、脱敏 failure enum、成功时的目标 GUID/角色/分组/套餐/权限覆盖类别、原始 HTTP request ID 和 operation ref。不得记录密码“已设置”标志、内容、摘要或长度。
8. 写带 terminal success/failure 与脱敏 failure code 的 outbox、operation result GUID/status，并消费票据。
9. 一次 commit；commit 后才构造响应。

任何一步失败整体回滚，不留下用户、权限半状态、成功审计、成功 outbox 或终态成功 operation。若 commit 结果未知，返回 `503 operation_commit_unknown` 和 `operation_ref`，由 query/recovery 判定，不报告创建成功。

A14 删除在锁定目标账户后，按成功创建 outbox marker 和 `admin_operation_responses.target_guid` 锁定所有相关记录，再锁定对应 operation 并验证 public ref、action、终态/到期形态、digest 和 HMAC。若 0010 已把可解析目标的 legacy 行改为 redacted `{}` 和全零 sentinel HMAC，删除事务必须用当前 key 生成 canonical redacted HMAC，并以 lifecycle/version/target/body/digest/sentinel 全部仍精确匹配为条件更新一行；影响行数不是 1 就整体回滚，不能跳过 sentinel。marker 与 snapshot 数量不等、任一记录缺失/篡改或任一写入失败都使删除事务 fail closed 并整体回滚；全部快照已经安全脱敏且 HMAC 可验证时允许幂等通过。

## 5. 错误契约

A03 复用 A14 固定 `admin_action_error` envelope 和安全消息，不返回依赖原文。主要映射：

- `400 invalid_admin_user_create_request`：JSON/字段/用户名/昵称/密码/角色/分组 GUID/套餐/覆盖格式非法，或出现金额与其他未知字段。
- `401`：沿用认证 middleware 的既有 `detail` shape。
- `403 action_operation_rejected`：角色层级、`users.create` 或附加能力不足；不泄露不可见资源。
- `404 action_group_not_found`：显式分组不存在、停用或不可见，统一安全响应。
- `409 username_conflict`：用户名已被任何活动或墓碑账户占用；只返回冲突事实。
- `409`：幂等、票据、actor/session/policy 漂移等沿用管理动作错误码。
- `410 operation_expired`：创建 operation 已超过查询保留期；Begin 和 operation query 都返回该分类，不加载快照、不带 operation ref 或旧成功正文。
- `410 created_user_deleted`：仍可定位的成功创建目标已按 A14 软删除；终态 replay 只返回安全错误和 operation ref，不读取或返回旧成功正文。该分类与 `operation_expired` 保持可区分。
- `422 action_inactive`：仅用于服务端尚未激活动作或部署版本不一致。
- `429 action_rate_limited`：带整数秒 `Retry-After`。
- `503 action_dependency_unavailable|operation_commit_unknown`：数据库、Redis、权限或提交状态不可安全判断。

所有成功与路由错误带 `X-Request-ID` 和 `Cache-Control: no-store`。错误正文不得包含 username 对应旧用户的 GUID、状态、角色或删除信息。

## 6. 前端流程

`/users` 在具备 `users.create` 展示能力时显示创建入口。表单规则与后端一致，但前端校验只改善交互，后端仍独立核验。

- Admin 的角色固定为普通用户；Root 可选普通用户或管理员。
- 有 `groups.read` 时加载启用分组目录；没有时固定使用省略 `group_guid` 的默认分组。选管理员时加载权限目录并显示三态编辑器；提交时只发送显式 allow/deny，inherit 不发送。
- 生产表单没有初始金额字段，不读取 query/localStorage 开启 Mock。
- 点击提交时生成一次 idempotency key，并在同一逻辑尝试和 operation query 中复用。
- 普通用户直接创建；管理员先弹出当前密码复核，取得 ticket 后立即提交创建。ticket、密码和 idempotency key 仅在组件内存中短暂持有，关闭、成功、明确失败和卸载时清零。
- `operation_commit_unknown` 只进入查询流程；查询终态成功后按原结果展示，不盲目重发。
- 成功展示 GUID 和 username，刷新列表并导航或提供查看详情入口；不再次显示密码。
- `409 username_conflict` 保留表单内容但清除密码、票据和幂等键；权限或策略漂移刷新当前能力与目录。

## 7. 验证与验收

后端必须覆盖：

- 严格 JSON、Unicode/边界校验、未知/重复字段和金额拒绝。
- Admin/Root/普通用户角色矩阵、Root 创建 Root 拒绝、附加 plan/group 权限。
- default group seed/回填/迁移 verifier、group 外键和读 DTO 投影。
- 管理员 intent HMAC 对密码、resolved defaults、group、plan 与排序后 overrides 的绑定和 secret 清理。
- 同 key 同请求、同 key 异请求、跨 session、并发用户名、票据单次消费和 commit unknown/query。
- 用户/权限/审计/outbox/operation 的事务回滚注入测试。
- 创建→operation Query 到期→删除、创建→Begin 到期→删除及到期/删除并发的真实 MySQL 测试，证明到期不丢失 durable target，删除与全部响应脱敏原子、删除后稳定 410 且无 PII。
- response HMAC 的 target/body/status 等字段绑定、直接数据库 mutation/delete 拒绝、多 snapshot、脱敏回滚，以及 key/version 生命周期测试。
- 0010 在默认 MySQL 8.4、普通 DDL 账号下的九个 committed-prefix 恢复、每个 redacted legacy 行无条件规范化、active/outbox 严格分路回填、outbox mismatch 矩阵和无法解析 PII fail-safe 脱敏测试。
- hasher 注入计数与并发门禁：forbidden/rate-limited/replay 为 0，fresh 为 1，同一 operation 并发只允许 fresh owner 哈希一次。
- 密码、ticket、幂等键、旧用户详情和依赖原文不进入日志/响应/持久化。
- MySQL 8 与 Redis 7 隔离 fixture、focused race、全量 `go test ./...`、build、vet、migration ledger/verifier。

前端必须覆盖：

- 角色和字段展示矩阵、确认密码、默认值及金额字段缺失。
- 普通创建零 action-verification；管理员创建 verification→create→query 顺序。
- 双击/迟到响应/关闭弹窗/冲突/提交未知/权限撤销竞态。
- 敏感字段只在内存且按生命周期清理；API adapter 严格响应 DTO。
- Node 测试、production build、真实本地 FE+BE Chrome 创建普通用户和管理员，以及无权限拒绝。

联合验收只把 A03 从 `BLOCKED_NOT_IMPLEMENTED` 提升为 `PASS_LIMITED_SCOPE`；其余 A05–A11、P01/P03–P08、R02 和完整 release 状态不随本切片改变。生产迁移、部署与真实业务账户创建需要独立授权。

## 8. 实施拆分

实施按依赖拆为：契约与迁移 → 后端普通创建 operation → 后端管理员票据/consumer → HTTP adapters → 前端 adapter/store/form → 隔离 fixture 与联合浏览器验收 → 双方 PM 和独立质量/安全复核。每一步使用 TDD，并保持生产 registry 只激活已具备真实 consumer 和完整验证的动作。
