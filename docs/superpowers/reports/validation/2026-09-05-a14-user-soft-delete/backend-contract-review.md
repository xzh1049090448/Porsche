# A14 users.delete 后端 HTTP 契约冻结审查

日期：2026-09-05  
范围：Task 13，仅冻结并同步 `users.delete` HTTP 契约；未修改业务实现、fixture、生产配置或部署状态。

## 审查结论

`docs/agents/contracts/admin-action-future-contract.json` 已从 `inactive_contract` 提升为 `active_users_delete_contract`。合同只声明以下三个 A14 v2 路由已注册：

- `POST /admin/v2/action-verifications`
- `POST /admin/v2/users/:guid/actions`
- `GET /admin/v2/operations?scope=users.delete`

旧 `DELETE /admin/users/:guid` 保留认证边界并固定返回 410；合同明确它不查询目标，也不执行数据库或 Redis 写入。只有后端 handler 与 route 实现布尔为 `true`，`frontend_client_exists` 保持 `false`。

合同已机器化冻结以下内容：v2 列表/详情正整数 `auth_version`；Issue、Execute、Query 和 legacy 410 的路径、方法、请求/响应示例、请求与响应 header、body 上限与字段规则；operation 状态与 failure code；400/401/403/404/409/410/422/429/503 状态与公开错误码；429 和 Query processing 的 `Retry-After` 规则；POST 零自动重放与 Query 刷新后最多一次 GET；A14 安全属性。

401 继续由现有认证中间件产生，因此错误矩阵将其 envelope 明确标为 `existing_authentication_middleware`。其余 A14 handler 错误使用固定 `admin_action_error`；只有 `operation_commit_unknown` 可公开 `operation_ref`。

## 运行时对齐

`internal/dto/admin_action_contract_test.go` 使用生产 `DecodeUserDeleteIssue`、`DecodeUserDeleteExecute`、`UserDeleteIssueResponse`、`DeleteUserResponse` 与 `UserDeleteQueryResponse` 校验合同 fixture，并检查根结构、四类 endpoint 结构、header、状态矩阵、安全属性和重复 JSON key 拒绝。

前端 draft 只更新既有 `admin_users_list`、`admin_user_detail`、`admin_user_action`、`action_verification`、`operation_query`，并新增 `legacy_admin_user_delete`。其他 22 个既有 interface 未变化；无 interface 被移除。A14 四个写入/查询/legacy 条目标为 `AGREED_FOR_IMPLEMENTATION`，前端实现状态仍由后续 Task 14–17 完成。

## 验证证据

- `python3 -m json.tool docs/agents/contracts/admin-action-future-contract.json >/dev/null`：PASS。
- `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/dto ./internal/handler ./internal/router -run 'UserDelete|AuthVersion|LegacyAdminDeleteGone' -count=1`：3 个 package PASS，0 FAIL。
- 前端 `python3 -m json.tool docs/agents/contracts/prd-260903-interface-draft.json >/dev/null`：PASS。
- 前端显式设置 `A14_BACKEND_CONTRACT` 后运行 `node --test src/api/admin-user-actions-contract.test.js src/api/admin-users.test.js`：13 PASS，0 FAIL，0 SKIP。
- 未设置 `A14_BACKEND_CONTRACT` 时合同测试按设计失败，错误固定包含 `missing_A14_BACKEND_CONTRACT`；测试不猜测 checkout 路径。
- 前端 `npm run build`：PASS；Vite 保留既有 chunk/dynamic-import warning，无构建失败。
- 两仓 `git diff --check`：PASS。

## 保留边界

- `frontend_client_exists=false`，本任务不宣称前端 adapter、状态机或 UI 已完成。
- 只激活 `users.delete`；其他危险动作及路由仍未激活。
- 未运行或修改隔离 MySQL/Redis fixture；未执行生产迁移、部署、push 或任何真实删除动作。
