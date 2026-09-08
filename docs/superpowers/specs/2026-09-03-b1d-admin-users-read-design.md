# B1-D 管理用户只读与认证权限投影设计 r1

状态：AGREED_FOR_IMPLEMENTATION。2026-09-03 Root 与后端 PM 双向确认；基线 BE aec1619ee710c80cd71dbe529660e2d12b3fda7b、FE 25a66a4ad546a481941dfc6e0cc9bc75da2e631b，保留 B1A–C 工作树增量。本文是已批准合同的落盘，不另设用户设计 gate。实现后仍须 PM 规格复核及独立质量验证。

批准 r3 原将列表/详情归入 B2；双方同意此次提前在 B1-D 交付该只读子集，打通可见闭环。不能据此宣称 B1 底座、票据、写链、完整 M1/A04 或 26 联合用例已完成。

## 新列表

GET `/admin/v2/users` 返回 `{items,total,page,page_size}`。page 缺省 1，规范正 ASCII 十进制且不大于 int32 最大值；page_size 缺省 20，仅 20/50/100。offset 使用 int64。空页 items 为 []，total 仍准确。

q 先 TrimSpace，必须有效 UTF-8，最多 128 rune，空值不筛选；username/nickname 使用参数化 literal LIKE，转义百分号、下划线及所选转义字符。仅规范正 int64 全数字 q 额外 OR 精确 guid，非规范或溢出数字仅字面匹配；绝不搜索内部 id。

role 仅 user/admin/root；status 仅 active/disabled/deleted，省略时 active+disabled。sort 仅 guid/username/created_at/last_login_at；order asc/desc；默认 guid desc，任一无效则整对回退 guid desc；其他排序追加同方向 guid tie-breaker。未知参数、重复参数及畸形编码 400；group 参数 400，detail 明确「分组筛选尚未接入」。其他参数格式错误使用固定安全说明。

UserReadDTO 固定为 guid 十进制 string、username/nickname nullable、email:null、group:null、plan_type free/professional/enterprise、role user/admin、status active/disabled/deleted、created_at UTC RFC3339、last_login_at UTC RFC3339|null。时间使用实际持久化毫秒字段，LastLoginAt 对应 last_login_at；不填假时间。无内部 id/AuthVersion/ACL/phone/realname/token/money；deleted 返回实际剩余 tombstone 值。

## 新详情与授权

GET `/admin/v2/users/{guid}` 使用 B1-C 规范正 int64 GUID，拒绝任何非空 RawQuery，返回同 DTO。GUID 校验仅适用于匹配路由；未匹配 URL 沿用 Gin 语义。

严格下级、非自己、Root 始终不可见：Admin 仅 User；Root 可 User/Admin。普通 User、users.read deny 均 403；坏 policy 503。deleted 列表还须 users.deleted.read，否则 403；详情不存在/不可见/缺 deleted.read 的 deleted 目标均 404。users.deleted.read 不能替代 users.read。

目标隐藏优先级沿用 B1-C：missing、self、Root、已知非下级角色及不允许显示的删除态先统一隐藏，不因隐藏目标的坏 status/AuthVersion 泄漏存在性；仅仍可见候选的未知 role/status/is_deleted、非正安全版本等视为损坏 503。

匹配到的两个新路由在认证前设置 Cache-Control:no-store 和现有 gateway request ID。新错误仅 `{detail:...}`，X-Request-ID 在响应头：400 固定安全参数说明；401「认证会话无效」；403「无权限访问」；404「用户不存在」；503「用户信息暂不可用」。现有认证 middleware 的 401 envelope/文案保留，不全球改造。

## 一致性与失败

仅接受 root SQL pool，服务自有 READ COMMITTED 事务；拒绝外部事务（含 RR）。actor user SHARE → 可选 detail target SHARE → actor session SHARE → Redis 撤销屏障。持 actor 锁严格读取 policy，复用 B1A evaluator/B1B1 严格行校验。失效身份优先于延迟的角色/目标拒绝；真正 DB I/O 错误 503。提交成功后才可输出 DTO，commit 失败绝不返回候选结果。

列表没有 target 锁；持 actor/session 锁，在 MySQL 8 单条 CTE filtered/count/paged LEFT JOIN 内同时计算 total 和分页，保证同 statement snapshot，空页仍返回 total。不得先授权再独立查询。SQL 日志静音，不泄漏连接串或目标安全字段。

## 三个旧只读兼容入口

GET `/admin/users`、`/admin/users/:guid`、`/admin/users/:guid/behavior` 采用相同 fresh actor/session/Redis、users.read 和目标层级过滤，始终排除 deleted。behavior 在同授权事务及 target 锁内调用 UserBehavior(tx,targetID)，保留 model_preferences；无额外虚构 capability。

列表保留数组 AdminUser DTO 与 created_at desc 默认排序；skip 默认 0、limit 默认 50，非数字 Atoi 语法失败仍回退 0/50，数字溢出 400，负值 400；limit 0 返回 []、1..100 有效、超过 100 明确 400（本次批准的 PRD 收紧）；skip 非负安全整数；非法 status 422。旧 GUID 继续接受 ParseUint 合法表示，非法或超 signed int64 均 404，不溢出 cast。

旧写入口及 GET logs/alerts/dashboard 本批不改；这些剩余权限边界是明确风险，不能声称完整管理授权闭环。

## 四来源认证权限投影

login、refresh、auth/self、users/me 复用同一 constructor，在原 user/profile 对象附加 admin_permissions 与 permissions_version，其余字段兼容。先 fresh identity：内部成功签发 session proof 或成功 middleware proof 重新核验 user/session/Redis；known disabled/deleted、AV/SV 不匹配、revoked/expired/missing 为 401；DB/Redis 未知错误、坏安全状态与 commit 失败 503。self/me 使用 fresh user，不能复用旧 middleware 对象作为响应。

持 fresh actor 锁读严格 policy。只有身份已明确有效且仅 policy 读取/校验不可用时，200 同时省略两个投影字段，绝不伪造 []/0。正常 User 为 []；Admin 使用 baseline+三态覆盖；Root 为全部 available；固定 catalog 顺序。permissions_version 为十进制 policy head 版本，合法无 head 为 "0"，空 head 保留真实版本。Root/User 非法 rules 也严格校验并按上述省略处理。

login/refresh 服务已签发后，先 fresh identity，再 SetCookie/成功 JSON。identity 401/503 不返回 access、不设置新 refresh cookie、不主动清原 cookie。不能伪称回滚已提交的 session/refresh：可能产生新 session 孤留或旧 cookie 已旋转的已知局限，保留现有恢复窗口/手动重登，不自动重放 POST 或扩大窗口。仅 policy 不可用则正常 200、正常已签发 cookie/access，省略两字段；commit 失败必 503。旧 auth 错误封装保持兼容。

## 范围、验证与准入

后续 FE 在现有 MainLayout 增加 /users 与 /users/:guid 只读页面，Root 看 Admin 时使用 B1-C permissions GET。group/email 尚未接入而为 null，金额未接入。无权限 PATCH、tickets、idempotency、outbox、真实分组或新 schema。

性能目标保留：100k 用户、并发 10、page20、服务端 P95 <=500ms；记录硬件及冷热状态，未运行不可 PASS。本次先执行无 DB 本地 TDD/回归/race；B1-C fixture 已销毁，不使用旧凭据。新 fixture 先提交明确计划，获本轮授权才执行。关键测试覆盖新旧拒绝/隐藏/失效/坏 policy、query/DTO、fresh projection/双字段省略/cookie语义/commit 失败及并发一致性。go-016 唯一 in_progress；最终须 PM 规格与独立 QA，本 writer 不自行签署最终 PASS。

2026-09-04 PM 对内部 refresh proof 细化书面确认：仅成功 Refresh 返回的内部 IssuedSession 入口锁 fresh user 取当前 AV，再核验 session/SV/归属/有效状态与 Redis，以 fresh user 签 access；login/self/me 的已知 AV 必须强匹配。通用 Actor 禁止 AV=0 跳过；不改变 refresh 持久化锁序/恢复窗。并发安全变更经用户锁与 session 撤销令读取等待或 401，不能降级为仅 policy 不可用。
