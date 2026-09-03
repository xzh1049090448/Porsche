# M3 SSE诊断实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** 为首帧前503提供脱敏、可关联的本地诊断候选，保持公开响应和计费行为。

**Architecture:** internal/diagnostics持有请求级context trace与结构化白名单日志。Handler建立trace并延迟结束输出；Service标记本地阶段，WhiteLabel记录网络/HTTP/解析阶段。只覆盖单模型platform chat。

**Tech Stack:** Go1.22、Gin、GORM/MySQL8、现有WhiteLabel适配器；标准库JSON/log，无新增依赖。

## Task 1: 诊断核心与真实调用链

文件：新增internal/diagnostics/trace.go及trace_test.go；修改internal/handler/platform.go、internal/service/platform_chat.go、internal/whitelabel/service.go；必要时增加内部SSE诊断入口，保留原公共投影方法签名。增加internal/handler/platform_diagnostics_test.go与WhiteLabel定向测试。

- [ ] 先写失败测试：真实适配器面对429/500、网络超时、非法首帧、提前EOF、取消时产生可区分诊断，公开错误仍503固定包；载入秘密哨兵证明日志不含URL、body、headers、prompt和原始错误。
- [ ] 执行go test相关包确认RED；允许httptest回环但禁止外部上游。
- [ ] 实现context私有trace，仅允许白名单阶段/原因；request_id_sha256不回显原始ID。结束日志包含UTC、固定路由/方法、HTTP状态、版本、阶段耗时、上游尝试/响应/首帧、扣次/保存结果。缺失trace时无副作用，不记录逐token内容。
- [ ] 插桩真实Handler→Stream→Chat→SSE路径。保留已有响应体、事件顺序、写入和扣费顺序、重放策略；区分首帧前失败、流中失败、结果保存失败。
- [ ] 隔离MySQL/Redis执行HTTP集成矩阵：validation/ACL、额度保存/会话/消息/最终保存失败、网络/非2xx、非法首帧/EOF、正常流、取消。核对日志阶段、公开合同和精确上游请求次数。

## Task 2: 审查与交付

- [ ] 后端PM规格审查；通过后独立质量/安全审查最终diff及定向用例，修复全部实际发现。
- [ ] 明确隔离依赖下go test -p 1 ./... -count=1、关键路径-race、go vet ./...、go build ./...、git diff --check；记录通过/跳过数量，不用缓存或SKIP冒充集成通过。
- [ ] docs/superpowers/reports记录RED/GREEN和本地范围；更新progress/feature_list，仅一个活动功能。
- [ ] 提交本地候选；明确源码SHA、可审查差异、部署/回滚准备和未完成线上验收。测试容器按本轮精确ID清理，保留线上余2次预算。不得自动部署或读取生产凭据。
