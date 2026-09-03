# M3 object有限分类候选验证

> 更新：准确候选已发布，第四次正常SSE子项PASS，预算4/4。下文为发布前本地验证历史；当前状态见[发布与复测](2026-09-03-m3-object-release-and-retest.md)。

2026-09-03。源码ad3f5b4416854353ebb2b3647ae4b2a809b4a05c，本地镜像sha256:2bc6b866911f439545bd3128bf8af24e640b3922706c024651d3a7d2d15096d0；尚未上传或部署，线上仍6e70784。用户追加后预算总4、已用3、剩1，本轮0次真实生成。M3-11仍FAIL，诊断候选不等于SSE修复。

## 变更与行为边界

六文件仅修改diagnostics/chunk.go、新增diagnostics/object.go与object_test.go、修改whitelabel/sse.go、新增sse_object_diagnostics.go与对应test。原struct解码、ID/object/created等拒绝优先级保持。invalid_value/object分支仅携带固定decoded_kind；仅有Trace的该拒绝路径扫描顶层键，补field_shape/key_match。闭集枚举、二次白名单、记录时复制值、nil Trace安全；重复键不以map覆盖，嵌套同名不计数。无原值、任意键名、长度/哈希或提示词正文进入日志。

## 实际验证

| 检查 | 实际结果与范围 |
| --- | --- |
| RED | 实际SSE+Trace的object:null，失败object_detail=map[]，缺少empty/null/canonical；/private/tmp/porsche-m3-object-red.log |
| 相关包GREEN | 175测试pass事件，0fail/skip；/private/tmp/porsche-m3-object-green.jsonl |
| 默认完整基线 | 304pass、0fail、98skip；未提供隔离DB/Redis，包括部分handler pipeline；不等于完整数据库集成验收 |
| 相关包race | 175pass、0fail/skip |
| vet/build/diff | 退出0 |
| 新旧公开行为 | 25字段形态×有/无Trace共50组，status/code/输出字节与6e70784一致；另17正常SSE摘要完全一致 |
| 独立对抗 | 重复/大小写/嵌套键、闭集注入、敏感哨兵、并发、无trace及其他错误范围，race命令ok1.956s |
| 发布脚本 | 7项mock行为及精确manifest绑定PASS；非生产回滚演练 |
| 第4次脚本 | 3项VM检查PASS：错误候选/预算耗尽/已有结果均在浏览器前停止，0预算写入、0结果覆盖 |

完整命令、默认测试跳过清单、旧/新对照路径见[验证摘要](validation/m3-object-validation-results.json)。未重新创建DB/Redis容器，无生产数据库访问。init.sh最初因沙箱禁止httptest回环监听失败，在允许回环环境同命令通过。新对照夹具首次存在Code→string编译问题，修正夹具后对新旧源码均完成同一运行；不记为产品失败。新增大小写类型错误测试的首个假设已与6e70784实测核对，采用原json_type/object而非unknown，未修改原测试断言。

## 双方与质量确认

后端project_manager完成设计和冻结六文件规格审查，限定本地PASS；确认原解码权威、扫描触发范围、重复键处理、失败回退及日志闭集复制。独立质量运行对抗probe并复核根任务全量/race/对照证据，VERDICT PASS，仅本地诊断范围。两者不签真实SSE/M3、不授权新候选部署，未运行线上生成。前端协调者接受此范围，并准备已授权剩余一次请求的受控脚本。

[候选清单](validation/m3-object-diagnostic-candidate.json)包含源码、镜像、归档、二进制及预算。归档SHA256 8154e46b93466f13018b8d310a4caf361bc40989cfd5e603e74ffe9ca94eb280。独立clone构建vcs.modified=false；离线镜像二进制/CA检查通过，无凭据启动按预期拒绝，不能当作真实健康。当前环境只读证据见[发布前快照](validation/m3-object-release-precheck.json)。

## 下一步与遗留

提交准确ad3f5b4候选的上传/后端替换/保留旧容器发布申请。详细执行与失败恢复见后端docs/superpowers/plans/2026-09-03-m3-object-release.md；发布后按版本绑定的第4次脚本使用剩余1次预算，唯一请求哈希关联新分类，清理测试对话并注销。当前尚未发出第4次请求，没有消耗新增额度。

go-010仅本地诊断passing，go-004 blocked、web-009 in_progress。原线上object实际值仍未知；other/ambiguous也可能需要供应商已有证据，不能直接放宽校验或保证单次修复。98项默认测试SKIP是本轮验证限制，既有DB结果保持其原版本范围。后续具体部署必须获准确候选授权。

最终材料复核：后端project_manager确认准确候选/旧容器/回滚/98项SKIP/授权边界一致，可提交用户部署授权申请；独立质量实际检查manifest、发布保存与锁、attempt4预算/结果独占及脱敏提取器，材料与脚本限定PASS。未执行上传、部署、模型调用；剩余1次已授权预算保留。
