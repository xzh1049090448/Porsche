# M3 object 有限分类诊断设计

承接已交付的2026-09-03-m3-object-investigation及用户追加预算后“继续”。本轮落实其中固定分类方案的本地实现、验证与候选准备；不是放宽SSE合同。现有调用预算总4、已用3、剩1，仅在采证就绪后使用；新候选具体部署仍需用户对准确候选授权。

## 目标与边界

在原invalid_value/object拒绝分支补充object_detail，区分字段缺失/null/空/已知非流值/其他字符串和键歧义。原reason/field、大类malformed_chunk、公开503、接受/拒绝、校验优先级及投影输出保持不变。数据库、handler/service、前端及计费写入顺序不改变。

方案比较：已有供应商样例最佳但当前未取得；有限分类能利用已追加的一次请求补充证据；忽略object或允许chat.completion缺乏证据，不采用。有限分类只给类别，other不透露实际值，不能承诺本次请求必然足以决定兼容修复。

## 诊断结构

malformed_chunk_detail下可选object_detail，只在reason=invalid_value、field=object且附加分类存在时输出。

- decoded_kind：empty、known_chat_completion、other、unknown。根据原struct解码完成后的object计算；不再次解码改变该结果。
- field_shape：missing、null、string_empty、string_nonempty、ambiguous、unknown。
- key_match：canonical、case_variant、multiple、none、unknown。

字段值采用闭集类型，入口再白名单归一化；未知值转unknown。不得保存原值、任意键名、长度、哈希、原始帧、提示词、正文、工具参数或JSON错误字符串。Trace必须复制分类值，不能存调用方可变指针；nil trace无副作用，mutex保护记录，不增加成功或无trace日志。

## 解析与优先级

先走原json.Unmarshal及ID校验；仅object不匹配时计算decoded_kind，并辅助扫描该已解析JSON的顶层键。使用encoding/json.Decoder保留重复出现和原顺序；逐个顶层键读Token，再Decode到临时json.RawMessage跳过完整值。键经JSON解码后与object按strings.EqualFold匹配；单个解码键恰好object为canonical，其他大小写为case_variant，多个匹配键均multiple/ambiguous。嵌套同名键不计入。无匹配为none/missing；单匹配按null、空string、非空string分类。畸形结构/扫描失败回退unknown形态与键类别，不改变decoded_kind或公开错误。不得用map覆盖重复键，也不能用最后一个原始null推断struct string被清空。

只在有trace且object拒绝时做结构扫描，保持普通成功及无trace兼容路径不增加不必要工作。可在SSE失败分支拿payload并仅为该reason/field计算分类；原解码后的值由failure的可选字段携带固定decoded_kind，不携带原值。详细helper被直接测试时应能核对decoded_kind；最终日志还须核对形态和键类别。

## 验收与发布准入

先用实际SSE+Trace测试RED，证明缺少object_detail；再实现。覆盖缺失/null/空/已知非流/other、大小写/转义键、重复与null顺序、嵌套、类型错误、多个同时错误、无trace、成功与其他拒绝不新增字段。直接诊断入口未知枚举归一化、指针隔离、并发及秘密哨兵必须验证。

对比6e70784的25项既有假设及17项正常SSE接受矩阵，接受/拒绝与公开字节不变；无需发上游请求。Go定向、diagnostics/whitelabel race、全量默认基线、vet/build及独立规格/质量审查；默认数据库fixture缺失时必须明确记录SKIP，不声称新完整数据库验收。此变更不改持久化，已有数据库验证保持原证据范围。

候选冻结提交后从独立clone构建linux/amd64二进制，校验vcs.revision/modified=false，镜像/二进制/归档哈希绑定。准备对13ada4aa/cb42当前容器的精确发布与回滚脚本，再申请准确新候选部署授权；不使用旧attempt3脚本或旧发布脚本原样重跑。第4次请求计划必须保存新增分类、预算发送前记账、1POST/0自动重放，结束清理测试会话并注销。
