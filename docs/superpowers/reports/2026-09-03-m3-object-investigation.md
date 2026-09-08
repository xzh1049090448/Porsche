# M3 object 契约调查与离线验证

2026-09-03，承接用户“继续”。调查基于代码6e70784、后端文档HEAD 66df28d；沿用上一轮线上证据，未再次访问运行配置或发出真实生成。本轮只更新调查文档和离线证据，不修改业务代码、接口合同或部署服务。预算仍3/3用完。

## 结论与来源

现有证据不足以放宽object校验。线上第三次请求只证明Go解码后的object不等于chat.completion.chunk；实际字段可能缺失、null、空串或其他字符串。25项合成离线用例全部符合当前行为，这不是25项真实上游验收。

[接口AI官方创建聊天请求文档](https://docs.jiekou.ai/docs/models/reference-llm-create-chat-completion)确认stream的SSE/DONE及include_usage行为。该页面通用响应说明object为chat.completion，但邻近结构是choices.message而非流式delta，未提供可据以确认本次模型异常的流chunk样例。因此不能把通用响应说明视为允许所有流chunk返回chat.completion的明确依据。核验日期2026-09-03。[官方目录](https://docs.jiekou.ai/llms.txt)仅用于定位文档；特定stream chunk搜索未命中并不证明供应商不存在其他说明。两个指南Markdown链接本轮工具读取失败，不作为已核实来源。

代码只读追踪：internal/service/platform_chat.go的Stream路径构造Stream:true，whiteLabelPayload在body.Stream为true时覆盖出站stream为JSON true；internal/whitelabel/service.go将payload送往/chat/completions。没有发现此路径将stream改为false的逻辑，但本轮没有抓取实际生产出站包，不能将静态分析当作抓包事实。

前端interface-contract.json仍要求meta→OpenAI delta chunk→[DONE]→业务done。保持非幂等POST不自动重放；没有依据修改合同。

## 25项离线假设矩阵

通过Go overlay向当前包注入临时测试文件，调用实际projectChatCompletionChunkDetail及实际SSE投影路径，不复制或重写解析器。失败路径断言legacy sentinel、503公开错误、首帧0、malformed_chunk和固定详情；SENSITIVE哨兵不得进入诊断。成功路径断言标准object输出与两个帧（chunk及DONE）。

| 形态 | 实际结果 |
| --- | --- |
| 标准object | 接受，公开输出chat.completion.chunk |
| 缺失/null/空串/chat.completion/response/其他字符串/带空白字符串 | invalid_value/object |
| number/bool/array/map | json_type/object，与本次线上分类不同 |
| OBJECT或转义后的object键 | 按Go struct匹配规则识别，标准值接受 |
| 标准→其他值/标准→空串/大小写同名后出现其他值 | invalid_value/object |
| 其他值→标准/null→标准/标准→null | 接受；后置null不会清空已有非指针string |
| 仅嵌套object | 顶层仍缺失，invalid_value/object |
| 标准顶层+非法嵌套同名键 | 接受，未知嵌套字段不改变投影 |
| 非法object+负created | 先记录invalid_value/object |
| 非法object+created错误类型 | 先记录json_type/created |
| 空id+非法object | 先记录missing_required/id |

[汇总](validation/m3-object-investigation-summary.json)、[原始测试事件](validation/m3-object-investigation-results.jsonl)、[合成测试文件](validation/m3-object-investigation-test.go.txt)。25个子用例通过，另父测试通过；0失败。定向调查没有运行全量/数据库/race，临时overlay使用-vet=off，因此不宣称新的完整质量门禁通过。GOPROXY=off、GOSUMDB=off，无生产凭证、数据库或上游调用。

## 最小补证据方案（未实施）

可选路径：

1. 优先取得已存在的脱敏失败帧或该模型明确stream:true的供应商协议说明，不产生额外生成费用。需要保留object缺失/null/字符串类别以及重复键、大小写形式；正文、提示词、工具参数、标识和认证材料应去除。普通JSON map重编码会丢重复键信息，不可当成原始结构证据。
2. 若无法取得现有材料，采用后端PM建议的固定分类诊断：仅原invalid_value/object分支记录decoded_kind（empty/known_chat_completion/other/unknown）、field_shape（missing/null/string_empty/string_nonempty/ambiguous/unknown）、key_match（canonical/case_variant/multiple/none/unknown）。不得记录原值、任意键名、长度、哈希或正文；日志入口二次白名单约束。结构扫描只用于补分类，不能替代现有struct校验；扫描失败回退unknown，不改变公开结果。顶层重复或大小写碰撞记multiple/ambiguous，保留原解码结果为拒绝依据。此方案仍须具体设计/实现/独立审查、准确候选发布授权及新增调用预算，当前没有准备新发布包。
3. 直接兼容chat.completion或忽略object：当前不采用。可能把非流message、其他协议或错误载荷当作正常chunk，且无法证明能修复线上故障。

建议先走路径1；路径2作为无法补齐现有证据时的后续准备方向。当前阻塞不是目录、认证或部署健康，而是缺少被拒绝字段的可区分类别。不得盲目使用原脚本发第四次请求。

## 后端协议澄清请求草稿（未发送供应商）

请确认贵方gpt-5.4-nano在stream:true、max_tokens:32的/chat/completions请求中，普通文本增量、结束块和usage-only块的object约定，是否可能缺省、null、空串或返回chat.completion；请提供去除正文、ID、提示词及认证信息的SSE结构样例，同时注明choices使用delta还是message、DONE顺序及Content-Type。本平台2026-09-03T06:17:57Z请求上游HTTP200，但首个公开帧前命中object固定值校验。此处未附原始请求ID，只有平台请求哈希，不要求供应商凭该哈希定位其日志。请优先提供已有协议材料，不为本请求额外发起计费调用。

该草稿仅供后端project_manager组织澄清；未向外部联系人发送消息。后端PM已书面确认当前不能放宽校验，并建议有限分类和上述重复键边界。前端协调者接受调查结论；M3-11保持FAIL，go-004 blocked，web-009 in_progress。新的真实生成必须另获明确预算，现有预算0不因“继续调查”增加。

独立质量最终只读确认：25项假设全部通过、0 fail/skip（含父测试共26个pass事件），测试调用真实投影/SSE及脱敏断言。限定接受离线分类调查结论，不接受线上原值推断或M3通过；没有独立执行线上生成。
