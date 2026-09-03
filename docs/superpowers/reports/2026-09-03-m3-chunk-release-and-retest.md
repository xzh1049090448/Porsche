# M3 细分诊断发布与第三次真实SSE复测

2026-09-03；用户明确授权精确候选6e70784的上传、后端替换及最后一次既有预算复测。发布通过，M3-11仍FAIL，整体M3不签收。

## 发布与可恢复状态

- 源码：6e70784e182a4bae1e5b8399053b45429e8e4400；运行诊断vcs_modified=false。
- 镜像：sha256:cb42daed3581c51a5e6f5ac5a237bec4b4e0c3540afb61d36acb27592dc2947f。
- 归档SHA256：195a6f3870e64d79d6aa1f9dbe994baea230ae49c7ac5b92ec8711ec14b8fc2f；发布脚本SHA256：b5082e937db68f83bac89fc7717ed7443d3c046bfe4d6305042e19ce9aaea3bb。上传后远端校验完全一致。
- 新容器13ada4aa4f1e4460265d778f3447957ea854b234b542e509ffcf6668ef7b3031，06:17:06Z启动，06:17:07Z发布成功，源站及公网health严格200。
- 回滚容器9425ea244ad71944ef78474cc405208fbbbe7eb22fdffd2b81e269d328d85b0c，名称ai-gateway-go-acceptance-rollback-1788416221798672629，镜像a69cfdab；更早d2de587/31abaeb亦保留。06:19:39Z只读复核两者均停止、新容器运行；健康仍200，runtime-private.json已删除，三个相关容器均无ROOT_BOOTSTRAP_键。
- 前端158a00e未重发，入口index-i7ZWPv9J.js及SHA256 7c133cb1197c701cc6288d2718384c92f8b4b24a15fd13d821f3930d8bf09033与发布前一致。无迁移、Root引导、Nginx变更或数据库清理。
- 三把发布锁、身份核验、候选配置比对均由已审核脚本执行；旧容器保留不等于本次演练了生产回滚。脚本为一次性精确对象执行记录，不得原样重跑。

证据：[发布结果](validation/m3-chunk-release-result.json)、[发布后复核](validation/m3-chunk-postcheck.json)、[候选清单](validation/m3-chunk-diagnostic-candidate.json)。

## 第三次请求与失败边界

06:17:57.135Z在真实Chrome、已发布前端上发送既定短提示，gpt-5.4-nano、max_tokens=32。仅1 POST，refresh为0。HTTP503、application/json、gateway_upstream_unavailable，SSE帧0；成功终态、可见回复、取消路径及成功用量同步未通过，不扩大签收。

浏览器request_id_sha256为6061877c3230cf7b09a052ff97c4b0a05210043eb1aac6a279df4eece64f8a0f，与新容器唯一匹配诊断相同。trace_id为5172e99e16deb2e78d0222be537bf175，日志UTC 06:17:59.508521681Z；源码完整revision与候选一致。

上游HTTP200，authentication/validation/catalog/quota/conversation/user_message/title/serialization/upstream_connect均success；sse_stream failed/malformed_chunk，固定详情为invalid_value/object；first_frame_emitted=false，assistant_save/usage_save/final_write均not_run。

依据internal/whitelabel/sse.go的同一拒绝分支：JSON解码未报错，解码id非空，解码object不等于chat.completion.chunk。没有保留原始帧或值，因此不能区分object缺失/null/空串/其他值，不能认定具体响应类型、后续字段合法、全部模型受影响或供应商违约。没有放宽现有校验规则。

应用调用数2→3、剩余98→97、total_tokens_used仍0；这只证明应用计数，不证明供应商零费用。本次测试对话删除200，logout204。预算已用3/3，剩0，不能再次运行本次脚本或发第四次生成。

证据：[浏览器结果](validation/m3-sse-diagnostic-attempt3-results.json)、[固定字段诊断](validation/m3-sse-diagnostic-attempt3-log.json)、[预算台账](validation/m3-sse-budget.json)。

## 双方书面确认与质量结论

前端协调者确认上述版本、请求、预算及清理证据，判定M3-11 FAIL。

后端project_manager只读审阅同组证据和object拒绝分支，书面确认“候选6e70784发布子项通过；第3次真实SSE仍失败，整体M3不签收”。确认范围包括准确镜像/容器、200健康、上游200、503/0帧、invalid_value/object及预算耗尽；实际object值和兼容决策未确认。

独立质量角色对release/log/request/usage/budget执行一致性断言，输出PASS evidence consistency: release/log/request/usage/budget assertions；VERDICT PARTIAL。可签收候选部署健康、诊断关联及固定失败分类；不可签收有效SSE或整体M3。两角色均未独立执行部署或线上生成。

## 下一步、责任与准入

| 事项 | 责任角色 | 当前确认状态与准入 |
| --- | --- | --- |
| object契约核对 | 后端project_manager | 已书面建议：只读官方规范及既有脱敏样例；尚无实际值证据，不直接归责上游 |
| 离线假设矩阵 | 后端执行角色，由PM委派 | 待执行：标准chunk、object缺失/null/空/其他响应类型；保持现有拒绝行为，先复现后决策 |
| 补证据或兼容方案 | 后端PM与独立质量 | 待明确：如需进一步诊断，先审严格有限分类，禁止原始帧或任意值输出；具体兼容修复未批准或实施 |
| 前端后续联调准备 | front_end_developer | 复用原生SSE事件顺序、done、取消/用量与身份隔离清单；当前无需前端业务改动，不得自动重放 |
| 再次真实复测 | 前端协调者 | 需具体修复/诊断候选、独立审查、运行来源核验及新增明确调用预算；当前剩余0 |
| M3联合验收 | 双方协调者+质量 | M3-11 FAIL；有效SSE及历史详情其余矩阵仍未签收，旧认证子项仅保留原版本范围 |

本轮没有修改业务代码或interface-contract.json；object实际协议尚未双方确认，不能将推测写成新合同。go-009仅诊断能力passing、go-004 blocked、web-009 in_progress。后续里程碑依赖契约证据、方案审核和新增预算，不编造日期。
