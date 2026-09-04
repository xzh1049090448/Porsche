# B1-E 管理动作安全底座设计

## 1. 决策状态

本设计已经获得产品方批准，实施基线为后端提交
`6c74fa82a229cf1a40c527a26b70049717deada4`。B1-E 只交付可复用的管理动作安全原语：
动作意图绑定、五分钟复核票据、幂等操作记录、租约恢复、结果查询、Redis 限流和事务回调边界。

本切片不激活任何业务动作。生产 `ActiveActionRegistry` 必须为空，路由器新增 HTTP 路由数必须为
零；下文冻结的 HTTP、动作 descriptor 和前端状态机是后续 consumer 的兼容合同，不是本切片可访问
的功能。所有对应路径在生产路由测试中必须继续返回 404。

B1-E 对 A14 的验收贡献只能标记为 `limited_subscope`。A14 仍是
`BLOCKED_NOT_IMPLEMENTED`，联合验收的 18 个 blocker 数量不变。

设计勘误（2026-09-04）：迁移必须显式声明两个以 `session_id` 为首列的非唯一外键支撑索引；该修订
只补全 MySQL 8 DDL 与 verifier 合同，不增加业务查询语义或改变其他已批准设计。

## 2. 范围

### 2.1 本切片包含

- 显式 MySQL 迁移 `0005`，仅新增 `admin_action_verifications` 和 `admin_operations` 两张表。
- 独立的 action security HMAC 根密钥及四个用途分离的派生密钥。
- 强类型、未激活的动作 descriptor 目录及其规范化意图编码器。
- 票据签发、操作 Begin、事务 Execute、结果 Query 的内部 Service 原语。
- Redis 失败关闭的复核与 Begin 限流。
- 可注入时钟、随机源、事务 consumer 和审计/outbox primitive 的测试边界。
- MySQL 8、Redis 7 隔离 fixture 中的迁移、并发、故障、时钟和清理证据。

### 2.2 本切片明确排除

- 生产业务 consumer、业务按钮、前端路由、前端 API client 或页面状态变更。
- 用户创建、重置密码、升降级、权限写入、软删除、公共内容发布或回滚的实际副作用。
- 生产 outbox worker；B1-E 只定义事务内 outbox 写 primitive。
- 生产发布、部署、迁移执行、push、真实模型调用、聊天、SSE 或上游请求。
- 自动重试事务 callback、自动接管 `pending_recovery` 或通用后台恢复任务。
- 对现有接口的响应形状、认证 Cookie、会话轮换或权限目录作兼容性变更。

## 3. 架构边界

内部模块分为五个清晰边界：

1. `ActionDescriptor` 只描述一个受支持动作的稳定整数、能力、角色限制、票据要求、目标类型和
   强类型 intent encoder。它不包含 handler 或业务函数。
2. `ActionSecurityCrypto` 负责严格解析外部随机值、派生用途密钥和生成 HMAC。原始 key、ticket、
   password 不得越过此边界进入持久化或日志。
3. `ActionSecurityRedis` 只执行原子限流；Redis 不保存票据或操作结果，也不是真实状态源。
4. `ActionOperationService` 编排最新用户/会话校验、锁、票据和操作记录。它不依赖 Gin context。
5. `TransactionalActionConsumer` 接受 Service 已开启的 `*gorm.DB` 事务，写未来业务效果、脱敏审计
   和 outbox。B1-E 生产 registry 为空，因此生产中不存在 consumer 实例。

路由器不得调用未激活 descriptor，不得根据任意 action 字符串反射或动态创建 consumer。生产
`ActiveActionRegistry()` 返回长度为零的只读切片。冻结目录使用单独的
`InactiveActionDescriptors()`，只能用于静态合同校验，不能传给路由注册或 Service constructor。

## 4. 外部随机值与密码学合同

### 4.1 严格格式

| 值 | 唯一合法格式 | 生成与处理 |
| --- | --- | --- |
| `Idempotency-Key` | `^ik_[A-Za-z0-9_-]{43}$`，总长 46 个 ASCII 字符 | 客户端 CSPRNG 生成 32 bytes，base64url 无 padding；大小写敏感。只允许一个 header 值，禁止 trim、逗号合并和 Unicode。 |
| Action Ticket | `^av_[A-Za-z0-9_-]{43}$`，总长 46 个 ASCII 字符 | 服务端 CSPRNG 生成 32 bytes，base64url 无 padding。只在 201 响应返回一次。 |
| Public Operation Ref | `^op_[A-Za-z0-9_-]{43}$`，总长 46 个 ASCII 字符 | 服务端 CSPRNG 生成 32 bytes，base64url 无 padding；用于安全关联和结果响应。 |

Idempotency-Key 和 Action Ticket 原文不得写入 MySQL、Redis、普通日志、审计、trace、错误或测试
归档。`public_ref` 是唯一允许进入结构化安全日志的相关标识；它不授予查询权限，也不得拼入自由
文本。解析失败在进入 Redis 或 MySQL 前拒绝。

### 4.2 根密钥与启动规则

新增 `ACTION_SECURITY_HMAC_KEY`。合法值必须恰好是 43 个 base64url 无 padding 字符，解码为
32 个随机 bytes；不接受普通口令、空白、默认值或自动生成值。

- `APP_ENV=production` 时缺失或非法必须使进程启动失败，即使生产 active registry 仍为空。
- staging 同样按生产规则校验。
- development/test 未设置时，现有服务可启动，但 action security Service constructor 必须失败；
  fixture 必须显式注入有效测试密钥。
- 任意环境一旦声明该变量都必须严格校验。它与 `AUTH_HMAC_KEY`、`JWT_SECRET_KEY` 或上游密钥
  字节相同也必须拒绝。
- 配置、错误和诊断只能报告 `missing`、`invalid_length`、`invalid_encoding` 或 `key_reuse`，不得
  回显值、前缀或派生摘要。

以解码后的 32 bytes 为 IKM、nil salt，通过 HKDF-SHA256 派生四个 32-byte key，info 必须逐字为：

- `porsche/admin-action/ticket/v1`
- `porsche/admin-action/intent/v1`
- `porsche/admin-action/idempotency/v1`
- `porsche/admin-action/lease/v1`

每次 HMAC-SHA256 的消息均为 `purpose || NUL || payload`，purpose 为固定 ASCII 常量，不接受调用者
输入。数据库保存 64 位小写十六进制摘要。比较使用 constant-time API。用途映射如下：

- ticket key：`ticket-value`；复核限流键使用 `rate-verification-actor`、
  `rate-verification-ip`、`rate-verification-session`。
- intent key：`intent-v1` 后接 descriptor 的规范编码。
- idempotency key：`idempotency-value`；Begin 限流键使用 `rate-begin-session`。
- lease key：`lease-owner` 后接每次 claim 新生成的 32 bytes。

purpose 字段和 HKDF info 共同阻止跨协议摘要复用。Redis key 只包含对应 rate HMAC，不含 user ID、
session ID、IP、idempotency key 或 ticket 原文。

## 5. 强类型未激活动作目录

动作整数是稳定持久化枚举，只能追加，禁止重排或复用。当前冻结目录如下，全部
`active=false`：

| INT | action | capability | rootOnly | requiresTicket | targetKind |
| ---: | --- | --- | --- | --- | --- |
| 1 | `users.create_admin` | `users.create` | true | true | 1 `none` |
| 2 | `users.reset_password` | `users.reset_password` | false | true | 2 `user` |
| 3 | `users.promote` | `users.promote` | true | true | 2 `user` |
| 4 | `users.demote` | `users.demote` | true | true | 2 `user` |
| 5 | `users.permissions.write` | `users.permissions.write` | true | true | 2 `user` |
| 6 | `users.delete` | `users.delete` | false | true | 2 `user` |
| 7 | `public_content.publish` | `public_content.publish` | false | true | 3 `public_content` |
| 8 | `public_content.rollback` | `public_content.rollback` | false | true | 3 `public_content` |

每个 descriptor 绑定独立 DTO 和 encoder；禁止 `map[string]any`、任意 action 字符串或通用 JSON
序列化参与 intent。字段顺序和语义固定为：

1. `users.create_admin`：`username`、`nickname|null`、`password` 原文、固定 `role=admin`、
   `group_guid|null`、`plan_type`、排序去重后的 `allowed_models`、`daily_call_limit`。
2. `users.reset_password`：`target_guid`、`new_password` 原文、`reason`。
3. `users.promote`：`target_guid`、`expected_auth_version`、固定 `role=admin`、`reason`。
4. `users.demote`：`target_guid`、`expected_auth_version`、固定 `role=user`、`reason`。
5. `users.permissions.write`：`target_guid`、`expected_permissions_version`、`catalog_version`、按
   capability code 排序的 `overrides`；每项只含 `capability` 和 `effect`。
6. `users.delete`：`target_guid`、`expected_auth_version`、`reason`。
7. `public_content.publish`：`content_type`、`version_guid`、`expected_base_version`、`reason`。
8. `public_content.rollback`：`content_type`、`version_guid`、`expected_current_version`、`reason`。

编码前必须先按各动作 DTO 规则完成验证与规范化。通用 encoder 不做 trim、大小写转换或 NFKC。
编码逐字段输出固定字段 tag、固定类型 tag、uint32 big-endian byte length 和 UTF-8/value bytes；整数
使用固定宽度 big-endian，null 有独立类型 tag，数组先输出元素个数再逐项编码。字段顺序就是上面的
顺序。raw password 只进入 intent HMAC encoder，计算后立即清除持有它的 byte slice 引用；不得写入
持久化、Redis、日志、错误、审计、operation result 或测试 golden。

这些 DTO 只是兼容合同。每个 action-specific DTO 必须在首个 consumer 激活的同一版本再次做安全
复核；冻结 descriptor 本身不能作为业务已经实现的证据。

测试专用 action 固定为 INT `2147483000`、名称 `test.noop`。它只能存在于 `_test.go`，不能进入
production build、冻结目录或权限 catalog。构建与静态扫描必须证明非测试文件不含该整数或名称。

## 6. MySQL 迁移 0005

迁移只支持 MySQL 8，显式 up/down，不使用 AutoMigrate。所有时间为 UTC Unix 毫秒 `BIGINT`，所有
枚举为 `INT`，所有用户关系使用内部 `users.id`。两个表均使用 InnoDB、`utf8mb4_unicode_ci`，并
满足标准 `id`、雪花 `guid`、四个审计字段和 `is_deleted`。

### 6.1 `admin_action_verifications`

| 字段 | 类型与约束 |
| --- | --- |
| `id` | `BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY` |
| `guid` | `BIGINT NOT NULL`，唯一；共享雪花生成 |
| `actor_user_id` | `BIGINT NOT NULL`，FK `users(id)` |
| `actor_auth_version` | `INT NOT NULL` |
| `session_id` | `BIGINT NOT NULL`，FK `user_sessions(id)` |
| `action` | `INT NOT NULL` |
| `target_kind` | `INT NOT NULL`：1 none、2 user、3 public_content |
| `target_guid` | `BIGINT NULL`；业务 GUID，不设 FK；none 必须 null，其他类型必须非空 |
| `intent_hmac` | `CHAR(64) NOT NULL` |
| `ticket_hmac` | `CHAR(64) NOT NULL`，唯一 |
| `expires_at` | `BIGINT NOT NULL` |
| `consumed_at` | `BIGINT NULL` |
| 审计/删除 | `created_at BIGINT NOT NULL`、`created_by BIGINT NULL`、`updated_at BIGINT NOT NULL`、`updated_by BIGINT NULL`、`is_deleted INT NOT NULL DEFAULT 0` |

索引名称与列顺序固定为：

- `uk_admin_action_verifications_guid (guid)`
- `uk_admin_action_verifications_ticket_hmac (ticket_hmac)`
- `idx_admin_action_verifications_actor_session_active (actor_user_id, session_id, is_deleted, expires_at)`
- `idx_admin_action_verifications_action_target_active (action, target_kind, target_guid, is_deleted)`
- `idx_admin_action_verifications_expiry (is_deleted, expires_at)`
- `fk_admin_action_verifications_session (session_id)`，非唯一外键支撑索引

外键固定为 `fk_admin_action_verifications_actor` 和
`fk_admin_action_verifications_session`。应用必须另外验证 session 属于 actor；两个独立 FK 不能证明
二者匹配。`fk_admin_action_verifications_session` 作为索引名和外键 constraint symbol 分属 MySQL
索引与 constraint 命名空间；SQL 必须显式声明两者，不能依赖 MySQL 隐式生成索引。

### 6.2 `admin_operations`

| 字段 | 类型与约束 |
| --- | --- |
| `id` | `BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY` |
| `guid` | `BIGINT NOT NULL`，唯一；共享雪花生成 |
| `actor_user_id` | `BIGINT NOT NULL`，FK `users(id)` |
| `actor_auth_version` | `INT NOT NULL` |
| `session_id` | `BIGINT NOT NULL`，FK `user_sessions(id)` |
| `action` | `INT NOT NULL` |
| `idempotency_key_hmac` | `CHAR(64) NOT NULL` |
| `request_hmac` | `CHAR(64) NOT NULL`；等于该 action 规范 intent HMAC |
| `verification_id` | `BIGINT NULL`，FK `admin_action_verifications(id)`；非空时唯一，确保票据只绑定一个 operation |
| `state` | `INT NOT NULL`：1 processing、2 succeeded、3 failed、4 pending_recovery、5 expired |
| `public_ref` | `CHAR(46) NOT NULL`，唯一 |
| `lease_owner_hmac` | `CHAR(64) NULL` |
| `lease_expires_at` | `BIGINT NULL` |
| `finished_at` | `BIGINT NULL` |
| `query_expires_at` | `BIGINT NOT NULL` |
| `error_code` | `INT NULL`，稳定内部失败枚举，API adapter 映射为固定字符串 |
| `result_kind` | `INT NULL`：1 none、2 user、3 public_content |
| `result_guid` | `BIGINT NULL`；业务 GUID，不设 FK |
| `result_http_status` | `INT NULL` |
| 审计/删除 | `created_at BIGINT NOT NULL`、`created_by BIGINT NULL`、`updated_at BIGINT NOT NULL`、`updated_by BIGINT NULL`、`is_deleted INT NOT NULL DEFAULT 0` |

索引名称与列顺序固定为：

- `uk_admin_operations_guid (guid)`
- `uk_admin_operations_public_ref (public_ref)`
- `uk_admin_operations_actor_action_key (actor_user_id, action, idempotency_key_hmac)`
- `uk_admin_operations_verification (verification_id)`
- `idx_admin_operations_state_session (state, session_id, is_deleted, lease_expires_at)`
- `idx_admin_operations_recovery (state, is_deleted, lease_expires_at)`
- `idx_admin_operations_expiry (is_deleted, query_expires_at)`
- `fk_admin_operations_session (session_id)`，非唯一外键支撑索引

外键固定为 `fk_admin_operations_actor`、`fk_admin_operations_session`、
`fk_admin_operations_verification`。唯一键按 actor/action/key 而不是 session 建立，专门用于检出跨会话
复用。`fk_admin_operations_session` 作为索引名和外键 constraint symbol 分属 MySQL 索引与 constraint
命名空间；SQL 必须显式声明两者，不能依赖 MySQL 隐式生成索引。两个 session 支撑索引只满足
MySQL 8 外键前缀索引要求，不增加业务查询语义。down 先 drop `admin_operations` 再 drop
`admin_action_verifications`；生产回滚不得自动执行 down。

基础失败枚举固定为 1 `action_rejected`、2 `target_version_conflict`、3
`policy_version_conflict`、4 `target_state_conflict`、5 `consumer_validation_failed`。整数 1..999 仅由
基础层追加；action-specific 值从 1000 起，在激活该 consumer 的同一设计版本追加，已发布值永不
改名或复用。B1-E 的 production active set 为空，因此生产路径不会写入任何 failure code。

两表的实际 Issue/Begin/Execute 写入必须令 `created_by` 和 `updated_by` 等于 actor user ID；字段保持
nullable 只为数据库规范允许的显式系统任务，B1-E 不存在以系统身份创建 operation 的路径。

### 6.3 保留与墓碑

票据 TTL 固定为 `300000ms`；`expires_at <= now` 已失效。processing lease 固定为 `30000ms`，之后
还有 `60000ms` grace。只有 `now > lease_expires_at + 60000` 才能原子转为
`pending_recovery`，不能自动重新执行 consumer。

terminal result 自 `finished_at` 起可查询 `2592000000ms`（30 天）。processing 初始
`query_expires_at` 暂取 `created_at + 2592000000`，写 terminal state 时原子改为
`finished_at + 2592000000`。到期后在持锁查询或维护过程将 state 置 5、`is_deleted=1`，清空
lease、failure/result 可选列，只保留 guid、actor/session/action、idempotency HMAC、request HMAC、
public ref、审计和到期事实。未来查询返回 410。

操作墓碑和 idempotency HMAC 永久保留，所有 Begin 查重必须显式包含 `is_deleted=1`，原 key 永远
不能重新使用。票据在消费或过期后也只逻辑删除，ticket HMAC 墓碑永久保留；业务代码不得物理
DELETE。任何未来数据生命周期清理必须单独批准，且不能删除防重用摘要。

## 7. 会话、权限与跨会话规则

逻辑会话唯一等于 `user_sessions.id`。同一 SID 的 Access JWT 更新和 Refresh 轮换仍属于同一逻辑
会话；新的登录记录即使 actor 相同也属于另一会话。

Issue、Begin、Execute 和 Query 每次都必须重新锁定并校验：

- actor user 未软删、状态有效、角色/能力及目标层级仍满足 descriptor；
- JWT 中 user GUID、SID、session version、actor auth version 与最新持久化事实一致；
- session 属于 actor、未软删、未撤销、未过期；
- Redis revocation barrier 可读且未撤销；Redis 错误失败关闭；
- ticket 的 actor、auth version、session、action、target 和 intent HMAC 完全一致。

刷新轮换不使同一 session 的票据失效，但 session version、actor auth version 或权限事实改变都会
使旧票据失效。logout、撤销、禁用、软删、升降级导致的安全版本变化均拒绝旧票据与旧 operation
访问。

跨会话规则固定如下：

- ticket 在另一 session 使用返回 403，且不透露 ticket 是否存在。
- 相同 actor/action/idempotency key 在另一 session Begin 返回 409；唯一键必须跨 tombstone 检出。
- Query 只允许创建 operation 的当前逻辑 session。另一 session 即使 actor 相同也返回 404。
- 隐藏目标、未知 public ref、错误 scope/key 组合也统一返回 404，不能形成存在性 oracle。

## 8. Redis 失败关闭限流

限流先于任何 MySQL 事务。所有 counter/TTL 更新通过单个 Lua 脚本原子完成；首次创建设置 TTL，
后续调用不得延长窗口。为保守防护，后续 MySQL 失败不退还额度。

复核签发同时检查三个窗口：actor 最多 5 次/15 分钟、可信客户端 IP 最多 20 次/15 分钟、逻辑
session 最多签发 10 次/小时。IP 必须使用现有 trusted proxy 规则得到的可信值，不能直接相信
`X-Forwarded-For`。任一超过即整次返回 429，`Retry-After` 为所有超限窗口中最早剩余秒数，向上
取整且至少 1。

Begin 按逻辑 session 最多 60 次/分钟。它在查 operation 前消费额度，重复查询或重复 POST 不绕过
限制。Query 不使用 Begin 配额，但必须受既有认证请求限制。

Redis client 缺失、超时、脚本错误或读写错误统一返回 503，不能回退到进程内 counter 或跳过限制。
限流键只使用第 4.2 节的 rate HMAC。429 和 503 均不得透露是哪一维触发、当前计数或原始键。

## 9. 锁序与事务协议

### 9.1 全局锁序

所有 consumer 必须遵守同一顺序，不能自行调整：

1. Redis limiter（事务外）；
2. actor user；
3. actor session；
4. operation；
5. verification；
6. target；
7. target sessions；
8. gateway keys；
9. permission policy；
10. audit/outbox。

MySQL 行锁使用明确索引条件和 `SELECT ... FOR UPDATE`。callback 不得再获取更靠前的锁，不得发出
HTTP、模型、SSE 或其他外部副作用。

### 9.2 Issue

Issue 在 Redis 三维限流成功后开启事务，按锁序重新验证 actor、session、auth version、密码、能力
与 intent。密码验证失败使用固定 403，不区分用户、密码、目标或动作状态。成功时生成新 ticket、
写 verification 并返回 201。相同 session/action/target 下签发新 ticket 时，在同一事务使尚未消费的
旧 binding 逻辑失效；丢失 201 响应后不得重试同一复核请求，必须由用户重新输入 current password
签发新票据。

### 9.3 Begin

Begin 在 Redis session 限流成功后严格解析 header 和 typed request。事务内锁 actor/session，再以
`(actor_user_id, action, idempotency_key_hmac)` 查 operation，且查询包含墓碑：

- 不存在：创建 state=processing、30 秒 lease 的 operation，再锁 verification，验证 ticket 尚未
  消费且 `expires_at > now`，并通过唯一 verification 约束把票据预留给该 operation。
- 已存在且 request HMAC 和 session 完全相同：只返回已有状态，绝不再次运行 consumer。
- 已存在但 request HMAC 不同：409 `idempotency_conflict`。
- 已存在但 session 不同：409 `idempotency_cross_session`。
- state=expired：410；state=pending_recovery：返回需要人工处置的确定状态。

lease owner 每次 claim 使用新的随机值及 lease HMAC。B1-E 不提供自动 claim；只有显式、后续审核的
恢复流程才可在 `now > lease_expires_at + 60000` 后把 processing 原子转为 pending_recovery。

### 9.4 Execute

Execute 以 `Execute(ctx, identity *OperationIdentity, ...)` 接收 Begin 返回的 operation 身份，并自行拥有
一个新的 `*gorm.DB` 事务。`OperationIdentity` 是一次性的内存能力：调用方把所有权转移给 Execute，
不得复制、序列化、复用或用公开字段重建。其 JSON 精确只允许 `public_ref`；内部数据库 ID、lease owner
和 Begin 时绑定的 actor claims 均不可序列化。Execute 在所有非 nil 返回路径（包括参数拒绝、Redis
失败或撤销、事务失败、已知拒绝、成功和 commit unknown）清零调用方原对象的 `LeaseOwner`，commit
unknown 也必须先清租约再返回。原值传参只会清除方法内部副本，无法撤销调用方持有的能力，因此本段
以指针消费语义修正原冻结签名；该修正不增加路由、不激活动作或生产 consumer。

Execute 重新按锁序锁 actor/session、
operation、verification，核对 lease owner、state、ticket/intent/session/auth version，并以条件更新
`consumed_at IS NULL AND expires_at > now AND is_deleted=0` 消费 ticket。随后 callback 可写 target
效果，并调用同事务的脱敏 audit 与 outbox primitive；最后在同一事务把 operation 写为 succeeded
或 failed、设置 finished/query expiry/result 字段并清除 lease。

已知业务拒绝由 callback 返回强类型 terminal outcome；它不留下部分 target 修改，failed operation、
拒绝审计及所需 outbox 仍在同一事务提交。基础设施错误使整个事务回滚，不得在事务外伪造 succeeded
或 failed。

事务 commit 返回错误即 `commit unknown`：先清零传入 identity 的 lease owner，再返回 503
`operation_commit_unknown` 和已生成的
operation ref，不自动重跑 callback，不另起事务覆盖状态。调用方只能使用原 scope/key Query 确认。
数据库不可用时保持 unknown；能读到 terminal row 才能确认结果。能读到 processing 时按租约规则
等待；超过 lease+grace 只转 pending_recovery，不执行副作用。

### 9.5 无生产 consumer 时的可验证边界

B1-E 不能声称完成任何真实业务效果、真实管理审计或生产 outbox 交付。测试只能在隔离 MySQL schema
创建 fixture-only effect/audit/outbox 表，并由 `_test.go` 中 `test.noop` transactional consumer 证明
以下 primitive：ticket consume、fixture effect、fixture audit/outbox 和 terminal operation 同事务；
任一步 fault injection 时全部回滚；commit unknown 不自动重放。测试表不进入 `0005`，测试后精确
删除，生产 build 中不得存在该 consumer 或 domain side effect。

## 10. 状态机

操作持久化状态只允许：

```text
processing(1) -> succeeded(2)
processing(1) -> failed(3)
processing(1) -> pending_recovery(4)  [仅 lease+grace 后原子转换]
succeeded(2)  -> expired(5)           [finished_at + 30d]
failed(3)     -> expired(5)           [finished_at + 30d]
pending_recovery(4) -> expired(5)     [query_expires_at 到期；只清结果，不执行副作用]
```

禁止从 terminal 或 expired 回到 processing，禁止复用 idempotency key。票据只有 active、consumed、
expired 三种派生状态：`consumed_at != null` 为 consumed；否则 `expires_at <= now` 或 `is_deleted=1`
为 expired；只有其余情况 active。票据不另存字符串枚举。

未来前端单次动作状态固定为：`idle -> submitting`。确定失败回到 idle 并显示固定错误；收到 terminal
success 后更新业务数据。网络错误、503 unknown 或提交响应丢失进入 `unknown`，只能携带原 scope/key
发 GET，绝不再次 POST。GET processing 使用服务端 1..30 秒 `Retry-After` 有界轮询；
pending_recovery 停止轮询并提示人工处理；GET 404 仍保持 unknown，不能当作原 POST 未执行。

## 11. 冻结但未注册的未来 HTTP 合同

本切片必须以 router enumeration 和请求测试证明以下路径均未注册并返回 404。合同只供后续同版
激活评审使用。

### 11.1 复核签发

`POST /admin/v2/action-verifications`

请求不带 `Idempotency-Key`，body 精确为：

```json
{"action":"users.delete","intent":{"target_guid":"123456789012345678","expected_auth_version":7,"reason":"duplicate account"},"current_password":"example-only-not-a-secret"}
```

`intent` 在真实 DTO 中必须是对应 action 的强类型对象，不能作为通用 map 解码。字段名固定为
`current_password`，不得使用 `actor_password`。成功 201：

```json
{"ticket":"av_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","expires_at":1790000300000}
```

响应丢失不能重试；新 Issue 会使旧 binding 失效。

### 11.2 未来业务 POST

未来 action-specific consumer 继续使用其 PRD 路由，并同时携带唯一且符合第 4.1 节格式的
`Idempotency-Key` 与 `X-Action-Ticket`。不存在通用执行路由。B1-E 不注册
`/admin/v2/users` 写路由、`/admin/v2/users/:guid/actions` 或公共内容写路由。

成功响应由 action adapter 输出其已有业务 DTO，同时必须包含 `operation_ref`。POST 响应只能代表
已提交 terminal state；processing/unknown 不得伪装为 2xx 成功。

### 11.3 结果查询

精确路径族为 `GET /admin/v2/operations?scope={descriptor action name}`；例如删除动作只能使用
`GET /admin/v2/operations?scope=users.delete`。请求必须同时携带原始 `Idempotency-Key`。不接受
public ref 作为授权、路径或 query 替代。成功/processing 的稳定响应字段精确为：

```json
{"operation_ref":"op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","scope":"users.delete","status":"processing","finished_at":null,"failure_code":null}
```

`status` 只可为 `processing`、`succeeded`、`failed`、`pending_recovery`。expired 返回 410，不返回
terminal payload。只有 exact adapter、scope、key、actor 和原逻辑 session 全部匹配时可见。

所有三个未来 endpoint 均返回 `Cache-Control: no-store` 和非空 `X-Request-ID`。processing 返回
`Retry-After`，整数秒范围 1..30；429 返回按窗口计算且至少 1 的 `Retry-After`。错误 envelope 固定为：

```json
{"error":{"code":"idempotency_conflict","message":"请求无法完成","type":"admin_action_error","request_id":"req-contract-example-1"}}
```

只有 `operation_commit_unknown` 可在 `error` 中额外带 `operation_ref`；其他错误不得带目标、ticket、
key、摘要、内部 ID 或依赖原文。状态语义固定为：

| HTTP | 类别 |
| ---: | --- |
| 400 | header/body/格式非法 |
| 401 | 既有认证失败 |
| 403 | 复核、票据、权限或会话固定拒绝 |
| 404 | operation/目标不可确认或为隐藏事实 |
| 409 | idempotency payload 或跨会话冲突 |
| 410 | operation 结果已过查询期 |
| 422 | action descriptor 未激活 |
| 429 | Redis 限流；带 `Retry-After` |
| 503 | Redis/MySQL/audit/outbox 不可用或 commit unknown |

生产 active registry 为空时不会到达 422，因为路由本身不存在，外部观察必须是 404。

## 12. 前端兼容边界

B1-E 的 Porsche-Web 改动数必须为零：不增加路由、按钮、client、store、文案或 mock。未来 consumer
激活时，前端只在内存持有 ticket、idempotency key 和 unknown 状态；不得写 localStorage、
sessionStorage、URL、analytics 或普通日志。

未来 POST 的 replay 次数固定为 0。401 时允许现有认证 client 完成一次 refresh，但不能自动重放
POST；只有结果 Query 在 exact action adapter、原 scope 和原 key 都仍在内存时，允许 refresh 后自动
重试一次 GET。页面卸载后不跨页面恢复敏感动作状态。

## 13. 实施与验收门禁

### 13.1 静态和单元门禁

- production active registry 长度为零；八个 descriptor 全是 inactive；test action 只在 `_test.go`。
- router 新增路由数为零；冻结路径对 authenticated/unauthenticated 请求均为 404。
- 格式解析覆盖长度、padding、Unicode、空白、重复 header、大小写和逗号合并。
- HMAC golden 覆盖四个 HKDF info、purpose NUL、规范 intent、用途不相等和 constant-time 比较路径。
- 配置覆盖 production/staging 缺失、非法、复用和 development/test constructor 失败。
- `git diff --check`、`go test ./... -count=1`、focused `-race`、`go vet ./...` 和 build 全部通过。

### 13.2 fresh fixture 门禁

必须使用全新、可处置、loopback-only 的 MySQL 8 和 Redis 7；连接只从
`TEST_DATABASE_URL`/`TEST_REDIS_URL` 注入，不读取生产 `.env` 或回退到运行配置。证据必须包括完整
容器 identity、image digest、只读挂载、端口、label、迁移 ledger 和脱敏命令摘要。

必须验证：

- `0001..0005 up`、`0005 down/up`、schema/FK/index/列类型与数据库规范一致；verifier 必须按第 6 节
  穷举清单精确核对两个显式 session 支撑索引和两个同名外键 constraint，不能接受隐式索引替代。
- ticket 300 秒边界、`expires_at == now` 拒绝；lease 30 秒、grace 60 秒的三个时钟边界；30 天结果
  到期及永久 key/ticket 防重用墓碑。
- 相同 key 并发、不同 payload、相同 ticket 不同 key、跨 session、Refresh 同 session、actor auth
  version 变化、权限/目标隐藏和过期竞态。
- Redis actor/IP/session 三维 Issue 原子窗口、Begin 60/min、TTL 不滑动、Redis 全故障 503。
- MySQL 在每个锁点、ticket consume、fixture effect、fixture audit、fixture outbox、terminal update 和
  commit 返回点的 fault injection；不存在伪成功或 callback 自动重放。
- 至少两个 goroutine 和真实 MySQL 行锁的 focused race；Go race detector 也通过。
- 原始 password、key、ticket、HMAC root、连接串和 session SID 不出现在 stdout、报告或 Git。

### 13.3 清理与结论门禁

fixture writer 必须按完整 container ID、name、task label 和 private path 做精确清理，先核对 identity
再停止；禁止 prune、glob、named volume 删除或触碰其他资源。独立 reviewer 必须确认容器、端口、
label、进程、私有目录和测试表均为零残留。

只有 spec review、implementation review、独立安全 review、fresh fixture、race、fault、secret scan
和 exact cleanup 全部通过，才能将 B1-E 标为完成。即便全部通过，结论仍只能是 A14
`limited_subscope`；未激活的八个 consumer、前端、生产环境和原 18 个 blocker 均保持未完成。
