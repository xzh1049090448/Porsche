# B1-B3 Gateway Key 最新用户 ACL 设计

**状态：PASS_LIMITED_SCOPE。** PM 最终 SPEC PASS 与独立 VERDICT PASS；不授权生产、commit 或 push。

## 目标

每次 Gateway Key 认证读取 owner 最新已提交的 active、未删除用户 ACL，并把它与 Key ACL 同时约束模型访问。它不写新 schema、不改 Key CRUD DTO，也不提供权限 writer、票据、outbox、幂等或前端接线。

## 认证契约

新增 `AuthenticatePrincipal(secret, ip, model string, now time.Time) (*GatewayTokenPrincipal, error)`。principal 私有保存有效标记、token ACL 与 owner ACL；所有对外 ACL 均为副本，nil/零值 `AllowsModel` 一律 false。旧 `Authenticate` 保持签名并委托新方法。`model != ""` 时 Key 与 owner 必须同时允许；`model == ""` 只认证身份，不能作为模型授权。

顺序固定：token 查找/状态/过期 → owner 最新 `id,status,allowed_models,is_deleted=0` → IP → 非空 model 双 ACL → `last_used_at` 更新。无 JOIN、无合并 ACL 实体、无 owner ACL 缓存；last-used `RowsAffected=0` 在同毫秒下仍可成功。读取、decode、last-used 写或未配置 service 失败均返回 `GatewayTokenUnavailable`，handler 映射固定 503 `gateway_authentication_unavailable` / `api_error` / `Gateway authentication is temporarily unavailable.`，不泄露 SQL、地址或凭据。

token 不存在为 401 invalid；owner 缺失/软删/非 active 为 401 disabled；Key disabled/revoked/expired/IP 维持既有 401/403。两边 raw ACL 严格解析：SQL nil、整体 JSON null、`[]` 都是各自 unrestricted；元素仅允许非空 string，拒绝 null、空字符串、数值和 object。字符串精确比较，不 trim、不通配。

## Handler 契约

所有 `/v1` 入口使用 principal。models 先经过 WhiteLabel Key+global 过滤，再按 owner 过滤并返回独立 slice；空集返回 `[]`。detail 先 owner 拒绝 404 `model_unavailable`，再以 Key ACL 调原 `GetModel`。chat/SSE 在 body 取得 model 后双 ACL AND，拒绝为既有 403 `gateway_model_not_allowed`，并在任何目录/生成上游调用前停止。成功 models/detail 设置 `Cache-Control: no-store`。全局 allowlist、目录 disabled 与 SSE projection 不变。

## 新鲜度边界

承诺认证读取当时最新已提交 owner ACL，变更提交后的下一请求可见。不会保证 token/owner 跨表原子时点、不取消在途 HTTP/SSE，亦不跨网络持有 DB 锁。

## 验证

真实 MySQL/Redis fixture 加 httptest 假上游覆盖：两 ACL unrestricted、单侧/交集/不交、拷贝/零值、owner disable/soft delete、Key 状态/IP/过期、两边损坏 JSON、读/last-used 故障；B1-B2 管理更新后同 Key 的下一目录/detail/chat/SSE 请求立即收窄，扩大仍受 Key/global 限制，目录 cache 不跨用户。无真实模型预算或上游调用。
