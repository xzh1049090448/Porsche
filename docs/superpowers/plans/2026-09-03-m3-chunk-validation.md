# M3 chunk 校验诊断实施计划

> **For agentic workers:** 使用subagent-driven-development或executing-plans按任务执行。

**Goal:** 在保留公开行为的前提下记录实际chunk拒绝分支的固定分类。
**Architecture:** whitelabel原投影拒绝路径产生固定原因/位置，由diagnostics请求trace白名单化记录；不新增正文日志。
**Tech Stack:** Go1.22、testing/httptest、独立MySQL8/Redis7。

- [x] 核对线上第2次日志与上游官方规范，保留最后1次×32预算。
- [x] 实现者按init.sh核对基础；先以合成坏帧断言新增分类、观察缺失分类的RED。
- [x] 最小实现固定分类/位置与日志入口白名单，保持malformed_chunk及旧公开输出；正常和无trace无额外日志。
- [x] 对所有现有拒绝条件及正常边界运行go test ./internal/diagnostics ./internal/whitelabel -count=1，保存实际命令输出。
- [x] 协调者使用显式隔离TEST_DATABASE_URL/TEST_REDIS_URL执行go test -json -p 1 ./... -count=1；专项-race、go vet ./...、go build ./...、git diff --check。
- [x] 后端PM复核规格，独立质量以新构造输入验证脱敏和接受/拒绝行为对照。
- [x] 按精确ID和所有权标签清理本轮测试容器及临时测试凭据，更新进度与双方交接。
- [x] 冻结代码提交，精确独立clone交叉构建及离线镜像验证，提交具体候选/回滚材料；未授权的新候选不上传部署。

协议不变断言示例：先用含敏感值且object不合法的合成SSE调用ProjectChatCompletionSSEContext，断言公开503/零输出、sse_stream.reason=malformed_chunk、内部固定object分类、日志不含敏感值；再以合法usage-only和tool chunks对照修改前后投影结果。测试用例由执行者围绕既有函数实现，禁止把线上原始正文当fixture。
