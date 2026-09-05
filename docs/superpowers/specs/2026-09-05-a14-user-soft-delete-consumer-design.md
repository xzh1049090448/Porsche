# A14 用户软删除首个生产 Consumer 设计

## 1. 目标与结论边界

本设计以 `users.delete` 作为 B1-E 管理动作安全底座的首个真实业务 consumer，按“后端合同冻结与验证、前端接入、隔离联合验收”的顺序交付。它覆盖 PRD-260903 的用户软删除和 A14 操作票据/未知结果查询链，不激活其他七个危险动作。

交付成功后，只能将 A12 与 A14 标记为用户软删除切片的通过状态。A03、A05–A11、P01、P03–P08、R02 及其他未实现能力保持原状态。本设计不授权 push、生产迁移、部署、真实生产验收、物理删除、用户恢复、金额能力或其他业务 action。

已确认的产品决定：

- 用户删除始终为不可恢复的墓碑软删除；用户名永久占用。
- 后端合同和 consumer 先冻结并验收，再接前端，最后进行真实前后端联合验收。
- 旧 `DELETE /admin/users/:guid` 在新流程启用时固定返回 `410 Gone`，不得继续作为无票据写入口。
- 删除原因写入真实管理审计，最长 200 个 Unicode code point；墓碑、operation 结果、outbox、错误和普通日志不保存原因。
- v2 用户列表与详情增加只读整数 `auth_version`，旧 `/admin/users` DTO 不变。

## 2. 架构

```text
Porsche-Web 用户列表/详情
  -> POST /admin/v2/action-verifications
       -> ActionVerificationService.Issue(users.delete)
  -> POST /admin/v2/users/:guid/actions
       -> ActionOperationService.Begin
       -> ActionOperationService.Execute
            -> DeleteUserConsumer（同一 MySQL 事务）
            -> DeleteUserAuditWriter（同一 MySQL 事务）
            -> AdminActionOutboxWriter（同一 MySQL 事务）
  -> GET /admin/v2/operations?scope=users.delete（仅 unknown/processing）
```

Handler 只负责严格解码、认证上下文、请求 ID、header 与 DTO 映射。B1-E service 继续拥有票据、幂等、锁、授权、事务和 operation 状态。`DeleteUserConsumer` 只执行用户删除业务事实；审计 writer 和 outbox writer 分别负责真实审计与事务内投递记录。

生产 registry 只激活现有稳定整数动作 `ActionUsersDelete=6`。其余七个 descriptor 继续 inactive；不存在通用 execute 路由或任意 action 字符串分派。

## 3. 公开 HTTP 合同

所有新接口经过现有 Access/Refresh 认证，返回 `Cache-Control: no-store` 与非空 `X-Request-ID`。管理动作错误继续使用固定 `admin_action_error` envelope，不回显目标事实、密码、ticket、幂等键、摘要、内部 ID 或依赖原文。

### 3.1 用户读取版本

`GET /admin/v2/users` 的每个 item 与 `GET /admin/v2/users/:guid` 的用户对象增加：

```json
{"auth_version":7}
```

`auth_version` 是正整数只读并发版本。客户端不得通过通用编辑接口修改它。旧 `/admin/users` 列表、详情和 behavior DTO 保持原形状。

### 3.2 签发单次票据

`POST /admin/v2/action-verifications`

禁止 `Idempotency-Key` 与 `X-Action-Ticket`。请求精确为：

```json
{"action":"users.delete","intent":{"target_guid":"123456789012345678","expected_auth_version":7,"reason":"duplicate account"},"current_password":"actor-current-password"}
```

规则：

- body 最大 4 KiB，单个 JSON 对象，拒绝未知字段、重复字段、尾随数据和错误类型。
- `target_guid` 必须是 canonical positive signed-int64 decimal string。
- `expected_auth_version` 为 `1..2147483647`。
- `reason` 去除首尾 Unicode 空白后必须为 1–200 个 Unicode code point；保留中间内容，不做 NFKC、大小写或语义重写。
- `current_password` 使用现有密码验证规则，只在请求与 intent HMAC 计算期间存在；不得持久化或记录。
- Issue 重新读取并锁定操作者、逻辑会话、目标与权限策略；目标隐藏返回 404，缺少权限或密码错误使用固定 403。
- 成功返回 201 和 300 秒单次票据；响应丢失后只能重新输入密码签发新票据，旧 binding 同事务失效。

成功响应：

```json
{"ticket":"av_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","expires_at":1790000300000}
```

### 3.3 提交软删除

`POST /admin/v2/users/:guid/actions`

要求唯一原始 `Idempotency-Key` 与 `X-Action-Ticket`。请求精确为：

```json
{"action":"delete","expected_auth_version":7,"reason":"duplicate account"}
```

路径 GUID、版本与规范化原因必须重新构造出与 Issue 完全相同的 `DeleteUserIntent`。`action` 只接受精确小写 `delete`；金额、启停、升降级、密码重置、套餐、分组及任意未知动作均返回 422，不能进入 consumer。

成功只在事务已经提交并获得可靠确认后返回 200：

```json
{"operation_ref":"op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","user":{"guid":"123456789012345678","status":"deleted"}}
```

POST 不返回 processing。确定的版本/状态冲突记录 terminal failed operation 并返回固定 409；基础设施错误在提交前返回固定 503。Commit acknowledgement 不确定时返回固定 503，且仅该错误额外包含 `operation_ref`。

### 3.4 查询未知结果

`GET /admin/v2/operations?scope=users.delete`

必须携带原始 `Idempotency-Key`，不接受 ticket、target GUID 或 public ref 作为查询授权。只有 exact users.delete adapter、当前操作者、原逻辑 session、scope 与 key 全部匹配时可见。

响应沿用 B1-E 已冻结字段：

```json
{"operation_ref":"op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","scope":"users.delete","status":"processing","finished_at":null,"failure_code":null}
```

`processing` 带 1–30 秒整数 `Retry-After`；`succeeded`、`failed`、`pending_recovery` 为终态展示；过查询期返回 410；隐藏或无法确认返回 404。Query 不触发 callback，也不消费 Begin 限流。

### 3.5 旧删除入口

`DELETE /admin/users/:guid` 保留路由但固定返回 410。它不查询目标、不执行数据库或 Redis 写入，并返回迁移到 v2 动作流程的稳定脱敏错误。旧 GET 与 PUT 合同不因本设计改变。

## 4. 权限、目标可见性与并发

- Admin 只有在 `users.delete` 最终权限为 allow 时才能删除普通 User。
- Root 可以删除 User 或 Admin；任何操作者都不能删除自己、Root 或同级/更高角色目标。
- 目标缺失、已删除或因层级不可见统一返回 404；有页面资格但缺少动作权限返回 403。
- Issue、Begin 与 Execute 均读取当前持久化操作者、session、目标、auth version 与策略。打开弹窗时的前端能力只控制展示，不是授权依据。
- `expected_auth_version` 必须在 Issue 与 Execute 时匹配。版本、状态或角色在两步之间变化，operation 以 `target_version_conflict` 或 `target_state_conflict` 失败并返回 409。
- 相同操作者、动作和幂等键只对应一个永久防重用 operation 墓碑。同 payload 返回既有状态；不同 payload 或跨 session 使用返回 409。Ticket 只能被一个 operation 消费。
- Execute 使用现有 actor -> session -> operation -> verification -> target -> policy head -> rules 的锁顺序，不引入反向锁序。

## 5. 删除事务与数据事实

`DeleteUserConsumer` 使用 `ActionOperationService.Execute` 传入的 MySQL 事务，不创建嵌套事务，也不访问外部网络或 Redis。它收到构造时冻结的规范化 `DeleteUserIntent`，并以 operation/verification 已锁定的 target 为边界执行：

1. 读取已锁定目标，核对 GUID、`is_deleted=0`、合法角色/状态和 `expected_auth_version`。
2. 检查 `auth_version < 2147483647`。
3. 将全部未删除、未撤销 session 原子设置为已撤销，并递增 session version；保留 session 历史，不物理删除。
4. 将全部仍有效 Gateway API Token 设置为 revoked 并更新审计字段；不删除 token 历史。
5. 将目标权限覆盖与 policy head 逻辑失效，保留历史行；普通权限读取始终过滤已删除策略。
6. 更新用户：`status=disabled`、`is_deleted=1`、`auth_version+1`，清除 `password_hash`、`phone`、`nickname`、`real_name`、`id_card_hash`，设置 `is_verified=false`，更新 `updated_at/updated_by`。
7. 保留用户内部 ID、GUID、username、created audit、角色以及维持历史关联所需的非敏感字段。用户名唯一索引不改，软删后不可复用。
8. 写一条 `auth_audit_events.user_deleted`，不含原因和凭据。
9. 返回 `ResultUser`、目标 GUID 与 HTTP 200 给通用 Execute；随后同一事务内写管理审计、outbox 和 terminal operation。

成功提交后，认证 middleware、Refresh、会话校验与 Gateway Token 认证都必须因用户墓碑或版本不匹配而拒绝旧凭据。Redis 否决标记不是成功正确性的唯一来源；本事务不在提交前执行不可回滚的 Redis 写。

## 6. 管理审计与 outbox

### 6.1 管理审计

复用现有 `audit_logs` 表，通过生产 `DeleteUserAuditWriter` 在 Execute 的同一事务内写入：

- `action="users.delete"`
- `user_id=target users.id`
- `resource="user:<target-guid>"`
- `detail` 允许清单：`operation_ref`、`actor_guid`、`target_guid`、规范化 `reason`、`before_status`、`after_status="deleted"`
- `IP` 不在 action primitive 中传递，本切片保持 null，避免把未验证的代理值混入审计。

审计行使用共享 GUID 与四个审计字段，`created_by/updated_by` 为操作者内部 ID。reason 不进入普通应用日志、operation、outbox 或错误。

### 6.2 outbox

新增前向迁移 `0006_admin_action_outbox`，建立 `admin_action_outbox`：

- 内部自增 `id`、唯一雪花 `guid`
- `created_at/created_by/updated_at/updated_by/is_deleted`
- 唯一 `operation_id -> admin_operations.id`
- stable INT `action`、`target_kind`、`state`、`delivery_state`
- nullable `target_guid`、`result_guid`
- `public_ref` 仅作安全关联，不包含 ticket/key/HMAC
- `available_at`、nullable `delivered_at`、有界 `attempt_count`

本切片只保证原子写入 pending outbox，不实现生产 worker、外部投递或自动恢复。用户墓碑、session/token 数据库失效已经构成同步安全事实，因此 worker 缺失不允许旧凭据继续工作。outbox insert 失败导致整个删除事务回滚。

`0006 down` 只删除新 outbox 表，生产不自动执行 down；既有 `0001..0005` checksum 不变。迁移 verifier 精确检查列、INT enum、BIGINT 毫秒、索引、FK、CHECK、InnoDB 与 down/up。

## 7. 前端交互与状态机

列表和详情仅在当前能力包含 `users.delete`、目标为可管理低角色且 `status != deleted` 时显示危险操作。后端仍是最终权限边界。

弹窗显示用户名、GUID 和“软删除后不可恢复、用户名永久占用、全部会话与调用凭据失效”。表单包含必填原因与当前操作者密码。密码输入不回显、不自动填充；关闭、失败或成功后立即清空。

状态机：

```text
idle -> verifying -> submitting -> succeeded
                    |          -> known_failure -> idle
                    |          -> unknown -> querying -> succeeded|failed|pending_recovery
```

- 开始时生成一次 cryptographically random idempotency key；ticket 由 Issue 返回。两者和 unknown 状态只在 action adapter 内存中存在。
- `verifying` 或 `submitting` 禁止重复点击。敏感 POST 不经通用 401 自动重放；401 后清空密码/ticket/key并要求用户重新确认。
- 收到可靠 200 后才移除列表行或切换详情状态。若本页为空，重新查询最后有效页；保留其余筛选与排序。
- 400/403/404/409 等确定失败保留规范化原因，清空密码、ticket 和 key。409 重新读取目标 `auth_version`，不静默覆盖。
- 网络错误或 `operation_commit_unknown` 进入 unknown，只以原 scope/key 查询。GET 可在认证 refresh 后自动重试一次；POST 永不重放。
- `processing` 按 `Retry-After` 有界轮询；`pending_recovery` 停止轮询并提示联系管理员。关闭页面后不恢复敏感动作状态，不写 localStorage、sessionStorage、URL、analytics 或普通日志。
- 前端中英文文案均明确“软删除”和“不可恢复”，不能使用物理删除或可恢复措辞。弹窗具备焦点锁定、键盘操作、字段错误定位和关闭后焦点恢复。

## 8. 错误与恢复

| 情况 | 对外结果 | 数据事实 |
| --- | --- | --- |
| body/header 非法 | 400 | 不访问 Redis/MySQL consumer |
| 认证失效 | 401 | 不签发/不提交 |
| 密码、票据、权限或 session 拒绝 | 403 | 不执行删除 |
| 目标隐藏/已删除 | 404 | 不泄露墓碑事实 |
| key payload/cross-session 冲突 | 409 | 返回固定错误，不重放 |
| 目标版本/状态冲突 | 409 terminal failed | operation 可查询，删除副作用回滚 |
| action 非 delete | 422 | consumer 不可达 |
| Issue/Begin 限流 | 429 + Retry-After | 不执行删除 |
| Redis/MySQL/audit/outbox 提交前错误 | 503 | 全事务回滚 |
| commit acknowledgement unknown | 503 + operation_ref | 只查询，不重放 |
| operation 结果过期 | 410 | 永久 key 墓碑仍禁止重用 |

重复删除对不可见已删除目标返回 404；它不得产生第二审计、outbox、session/token 更新或成功 operation。

## 9. 验证与验收

### 9.1 后端 TDD

- DTO/header/parser：精确 JSON、重复/未知字段、长度、Unicode、GUID/version/reason 边界、header 重复与逗号合并。
- Registry/router：只激活 `users.delete`；其余 descriptor 与写路由仍 inactive；旧 DELETE 精确 410 且零依赖调用。
- Service：Admin/User、Root/User、Root/Admin 成功；self/Root/equal/higher/permission deny/hidden/disabled/version overflow 拒绝。
- Consumer：墓碑字段、永久 username、session/token/policy 失效、认证审计、管理审计 reason、outbox、operation result 全部原子。
- 幂等与票据：过期、重放、不同 key 同 ticket、相同 key 同/异 payload、跨 session、refresh 同逻辑 session。
- 故障矩阵：每个用户/session/token/policy/audit/outbox/terminal write、Redis limiter/auth check、commit return；验证零部分副作用和无 callback replay。
- 并发：至少两个 goroutine 对同一目标/票据/key 使用真实 MySQL 行锁并通过 race detector。
- 凭据失效：删除提交后旧 Access、Refresh、session 与 Gateway Token 的下一请求均拒绝；用户名重新注册冲突。

### 9.2 前端 TDD

- API adapter 精确 header/body、单次 POST、Query GET 一次 refresh 重试、错误映射和无 storage/log 泄漏。
- Store/state machine 覆盖成功、确定失败、unknown、processing、pending recovery、页面卸载和重复点击。
- 列表/详情覆盖按钮权限、弹窗字段与警告、版本冲突刷新、成功页码回退、筛选保持和可访问性。
- Production build 中不存在金额 Mock 或其他 action 激活副作用。

### 9.3 隔离联合验收

创建新的 task-only、loopback-only、AutoRemove、只读 rootfs/tmpfs MySQL 8 与 Redis 7 fixture，预先记录完整身份和精确清理计划。执行 `0001..0006`、`0006 down/up`、schema verifier、后端 full/race/fault、前端 test/build，再由独立 reviewer 使用保留 fixture 复跑。

浏览器验收覆盖：

- Root 删除 User、Root 删除 Admin、授权 Admin 删除 User。
- Admin 删除 self/Admin/Root 不可见或拒绝。
- 版本变化、票据过期/重放、重复删除、同 key 同/异 payload。
- Redis、MySQL、audit、outbox 与 commit unknown；unknown 只查询，页面不重放 POST。
- 删除后登录/Refresh/API Key 拒绝、默认列表消失、已删除只读视图、用户名不可复用、数据库无物理 DELETE。
- 刷新/关闭页面后没有 ticket、password、key 或 unknown 状态残留。

独立 PM 规格、实现与安全审查、秘密扫描、精确 fixture cleanup 全部通过后，A12/A14 才能更新为该切片通过。生产部署与生产迁移仍需另行授权。

## 10. 实施顺序

1. 冻结后端 DTO、路由、错误与 `auth_version` 读取合同。
2. 新增 0006 outbox 模型、迁移与 verifier。
3. 实现生产 users.delete registry、consumer、audit/outbox writers。
4. 激活 Issue、action-specific POST、Query，并将旧 DELETE 改为 410。
5. 在全新隔离 MySQL/Redis 完成后端真实验证和独立审查。
6. 冻结前后端 interface contract，前端实现 action adapter、状态机与 UI。
7. 完成前端测试/build、真实联合浏览器验收、状态更新与精确 cleanup。

每一步独立 TDD、独立提交并经过规格与质量复审。任何阶段失败都保留证据，不能用后续成功覆盖历史。
