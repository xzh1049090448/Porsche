# M3 SSE 校验原因细分设计

用户在已说明“先细分解析失败原因，再复测”后回复继续，执行此前双方确认的固定枚举最小方案。来源基线3a35301，线上仍04ed728，本次不改变上游协议接受规则或业务行为。

目标：将上游HTTP200后首帧前的malformed_chunk进一步定位到当前真实校验拒绝分支，给最后一次32-token生成请求提供有用信息。保留sse_stream.reason=malformed_chunk和公开503，仅追加内部固定原因及字段位置；位置如choices[].delta，不含动态索引或任意字段名。日志入口再次白名单化，非法输入回落unknown；不保留原始帧、字段值、prompt、tool参数、错误原文或供应商信息。

实现选择：在原投影器拒绝路径产生固定分类，经内部详细helper或等价方式传递给请求级trace；保留现有调用签名/哨兵及正常输出。相较于失败后另行扫描，这能绑定实际拒绝分支；相较于打印原始帧，不产生正文泄露。多项同时非法按现有校验顺序分类，类型错误只投影固定路径与类别。

代码边界：internal/whitelabel/sse.go及聚焦的新helper/test；internal/diagnostics日志结构及白名单/test。不修改handler、数据库、费用、重试、SSE framing或前端。无trace的compare与公开/v1路径保持无新增日志；每次请求分类有界。

规范核对：前端interface-contract.json保持meta→OpenAI chunk→[DONE]→business done，POST不重放。官方JieKou说明确认stream为data-only SSE，结束标记[DONE]；include_usage末块choices为空，普通块usage可null。当前投影器已接受这些样式，不能把它们当成未经证实的根因。官方主响应示例为非流chat.completion，不能据此推断流式object。来源：https://docs.jiekou.ai/docs/models/reference-llm-create-chat-completion 。旧2026-08设计中SQLite测试描述已被当前domain/MySQL8规范取代，不采用。

验收：先RED证明细分字段缺失，再GREEN覆盖JSON语法/类型、id/object/created、usage计数、choices空、choice index/delta、tool_calls/function各拒绝分支。正例对照包括content/null/空delta/role-only/finish-only/tool增量、usage-only、缺省字段、created0、未知字段投影丢弃、CRLF/多行data/DONE/EOF。秘密哨兵出现在字段名、错误值、tool参数和畸形帧均不进入日志。既有公开错误和投影结果逐字节保持。

验证在一次性独立MySQL8/Redis7完成全量回归，另外执行诊断/whitelabel竞态、vet/build和独立规格与质量审查。记录所有跳过与真实执行范围，不把本地通过当M3通过。

后续候选须绑定源码SHA、未修改VCS构建信息、amd64二进制、镜像ID、镜像归档SHA并准备精确回滚；本轮继续细分方案不自动替换此前获授权的04ed728发布对象。最后一次生成预算仍保留，只有新诊断具备区分能力并运行后才安排复测。
