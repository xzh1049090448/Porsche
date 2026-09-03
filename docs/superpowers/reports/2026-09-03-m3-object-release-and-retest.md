# M3 object候选发布与第四次真实SSE复测

2026-09-03。用户明确“授权”后发布准确候选ad3f5b4，并执行剩余一次gpt-5.4-nano / max_tokens=32请求。**发布PASS；正常SSE终态子项PASS；整体M3仍PARTIAL。** 本次不修改业务代码或接口契约。

## 发布与运行身份

- 源码：`ad3f5b4416854353ebb2b3647ae4b2a809b4a05c`，运行日志`vcs_modified=false`。
- 镜像：`sha256:2bc6b866911f439545bd3128bf8af24e640b3922706c024651d3a7d2d15096d0`；归档SHA256 `8154e46b93466f13018b8d310a4caf361bc40989cfd5e603e74ffe9ca94eb280`。
- 远端上传后核对归档和已审核脚本SHA256，脚本摘要`f98fdd92e10fca06e600e50283e2c19eca73cfd4850d22f17dc65d41c5b0c0e1`。三锁、候选创建前后配置比较、健康检查均由准确发布脚本执行。
- 新容器：`1665e1118c949441909d8478bd1a88a7b058d5c02d8465701ee586d463ce9ef6`，07:46:42Z启动，07:46:45Z发布成功。
- 旧`13ada4aa…`保留为`ai-gateway-go-acceptance-rollback-1788421597814285207`；更早`9425ea…`与`d2de587…`也停止保留。未执行真实回滚，不把mock恢复测试视为生产回滚演练。
- 07:48:51Z只读复核源站/公网health均200；四个容器无ROOT_BOOTSTRAP_键，私密runtime快照已删除。
- 前端仍158a00e，`/assets/index-i7ZWPv9J.js` SHA256 `7c133cb1197c701cc6288d2718384c92f8b4b24a15fd13d821f3930d8bf09033`。未改前端、Nginx、数据库schema或Redis配置。

## 第四次真实请求

| 项目 | 结果 |
| --- | --- |
| 请求范围 | 专用账号B，gpt-5.4-nano，max_tokens=32，1 POST、0 refresh，无重放 |
| 浏览器响应 | HTTP200，text/event-stream，页面回复非空 |
| 终态 | meta → 4个delta → 唯一upstream_DONE → 唯一业务done；done最后 |
| 用量 | done.tokens=1、done.total_tokens_used=1；usage接口累计tokens0→1、daily_calls_used3→4、剩余日次数97→96 |
| 唯一请求关联 | SHA256 `b9477e5517ce1f52f9a503eb9a50e592320ac0e48a187d10bdc4ee51e6b2fba6`；浏览器与新容器日志完全一致 |
| 服务端 | 07:47:38.158937237Z，上游与下游均200；全部阶段success，first_frame_emitted和final_saved均true |
| 清理 | 仅本次测试对话删除200，退出登录204 |
| 调用预算 | 原3次记录保留，第四次记账后4/4，剩0；未发第五次 |

服务端保存成功标志与公开usage结果一致。本轮未直接查询生产数据库表，不把这些证据扩大成数据库完整一致性审计；应用用量不是上游结算账单。

## 结论范围与未复现问题

本次只证明准确版本和模型的一次正常流成功，不能说明先前间歇性object拒绝已经根治。ad3f5b4只增加有限分类诊断，没有放宽解析。此次sse_stream成功，未触发object拒绝，未产生object_detail。归档日志中的`malformed_chunk_detail={reason:unknown,field:unknown}`是提取器在原日志缺少该字段时合成的默认值，**不是一次未知原因的拒绝**，不应作为故障证据或纳入失败次数。

第1～3次失败证据保留；第3次invalid_value/object真实存在，但原始值和实际形态无法由本次成功反推。此前默认全量304pass/98skip仍仅对应本地候选验证，98项因缺少DB/Redis fixture跳过，不因本次线上成功而转为通过。

## 确认与跟进

- 后端project_manager已书面确认本次发布与正常终态限定PASS，整体M3 PARTIAL；只读复核证据，未独立执行线上操作。
- 前端协调者接受上述版本、请求和用量范围；独立质量已完成只读复核，正常流限定PASS、整体M3 PARTIAL；未发线上请求或读取凭证。
- 后端PM负责原object异常的契约澄清，优先利用已有脱敏证据；当前没有异常object实际形态的结论，不为推断而改宽校验。
- 前端协调者/开发者保留真实取消、流内error、[DONE]后失败及跨身份流回调等待测项；先编排用例与预期，不额外调用。M3-02/05/09/10的既定未覆盖范围仍见p0-m3-readiness.md。
- 质量角色按明确版本审查剩余矩阵；新增真实生成必须有新增预算。预算耗尽时不自动重放、不预先创建新候选。
- go-010诊断范围passing；go-004 blocked、web-009 in_progress。整体M3未联合签收。

## 归档证据

同目录validation下：m3-object-release-result.json、m3-object-postcheck.json、m3-sse-diagnostic-attempt4-results.json、m3-sse-diagnostic-attempt4-log.json、m3-sse-budget.json和更新后的m3-object-diagnostic-candidate.json。脚本与执行前验证保留原版本；原始上游正文、Token、Cookie、密码不写入报告或Git。

双方及质量最终书面复核已归档至[复核记录](validation/m3-object-release-reviews.json)。三方一致接受仅本次正常流与准确发布证据，不签取消/error/EOF等未测路径或整体M3。
