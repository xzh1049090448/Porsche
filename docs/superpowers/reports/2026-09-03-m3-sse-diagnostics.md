# M3 SSE诊断本地验证记录

## 问题与边界

前端158a00e在aiportcloud.com的专用账号首个gpt-5.4-nano/max_tokens32请求返回503 gateway_upstream_unavailable，0帧；应用日调用增加1而token计数0。这不能证明供应商零计费，也不能确定根因。线上余2次预算不在本地验证中消耗。

本轮基于后端0bab2b7，fix/m3-sse-diagnostics隔离工作树，只准备脱敏诊断候选。记录后端PM方案确认与本地验证，不声称修复真实上游503或M3通过。

## 基线

- 原始init.sh在沙箱内因httptest无法绑定回环监听失败；在允许回环能力的执行环境原样重跑退出0。未设置TEST_*的这轮有缓存与数据库测试跳过，不作为完整集成依据。
- 本轮独立创建MySQL8/Redis7容器，只开放127.0.0.1随机端口，数据库porsche_m3_diagnostics_test。MySQL数据位于tmpfs；未使用生产.env、DATABASE_URL或任何原有用户容器。
- 在只读原提交worktree运行source /private/tmp/porsche-m3-diagnostic-test.env后，go test -json -p 1 ./... -count=1退出0：247个pass事件（含子测试）、0fail、0skip。
- 原始输出：/private/tmp/porsche-m3-diagnostic-db-baseline.jsonl。临时依赖ID清单在/private/tmp/porsche-m3-diagnostic-fixtures.json；测试凭据仅在0600临时文件，报告与仓库不保存其值。

## 发布边界

当前没有生产操作。现有production-deploy.sh会切换/reset main，auth-acceptance-deploy.sh固定历史认证分支且同时发布前端，均不能直接用于该诊断候选。应使用审核后的候选产物与单独后端发布/回滚步骤；先绑定源码SHA、镜像、二进制哈希和旧容器，确认健康再复测。当前前端158a00e及其静态资源不应被这些历史发布脚本覆盖。

## 定向实现验证

- 先补测试：core初次缺符号失败；adapter真实调用记录attempted=false与connect/stream not_run；完整handler测试因没有诊断JSON失败（/private/tmp/porsche-m3-handler-red.txt）。实现后相同路径通过。
- 额外RED→GREEN：父context仍有效时，reader返回timeout/canceled起初被记stream_read_error，已改为类型化分类；不存在或其他用户会话的查询拒绝起初被记database_error，已按现有业务错误边界归类rejected。
- 最终定向输出/private/tmp/porsche-m3-diagnostic-green.jsonl：45个测试通过事件，0fail、0skip（协调者已重新解析核对）。覆盖上游429/500/连接失败、非法首帧/提前EOF、metadata首次写失败/后续写失败、取消、最终写失败、quota/conversation/message/title/assistant/usage失败、正常SSE顺序、持久化计数、requestID/hash/作用域及日志秘密哨兵。
- 现有diagnostics与whitelabel全包回归通过；最终全量与独立质量结果另列。

## 规格审查与诊断解释

后端project_manager独立读取实际diff、源码和测试，git diff --check通过，书面给出限定本地规格PASS。没有独立发起线上请求，没有为完整M3签收。

- request_id_sha256关联既有公开ID，trace_id区分单次请求。Hash避免直接记录原值，不代表不可关联或完全匿名。
- 保存标记只有对应操作完整成功才为true；false不保证无部分数据库写入。AddMessage可能已insert后再更新会话失败，final_saved=false也不证明助手消息和用量都未落库。诊断没有引入事务或改变计费语义。
- GetConversation既有实现将底层查询错误映射404；日志按现有业务边界记录rejected，不能进一步断定“真的不存在”还是底层查询失败。
- Panic不属于本轮受控错误矩阵。诊断defer可能先于外层Gin Recovery写500，故panic时的http_status及未结束阶段不是最终恢复状态，需结合既有panic日志；不声称panic全覆盖。
- upstream_request_attempted标记开始调用HTTP client，不证明供应商已收到或已计费；首次写出标记只证明本地writer成功返回，不证明远端浏览器已收到。
- Go build info缺失时明确unknown；现有Dockerfile不包含.git，不自动获得源码revision。候选构建与manifest仍需绑定，不能拿未知字段证明线上精确来源。

## 协调者全量、竞态与构建验证

在本轮独占隔离TEST_DATABASE_URL/TEST_REDIS_URL下执行：

```sh
source /private/tmp/porsche-m3-diagnostic-test.env
go test -json -p 1 ./... -count=1
go test -race -json -p 1 ./internal/diagnostics ./internal/whitelabel ./internal/handler -run 'Trace|BuildVersion|NetworkReasons|Diagnostic' -count=1
go vet ./...
go build ./...
git diff --check
```

实际结果：全量292个pass事件、0fail/skip，race45个pass事件、0fail/skip；全部退出0，vet/build/diff无错误输出。原始JSONL：/private/tmp/porsche-m3-diagnostic-final-full.jsonl、/private/tmp/porsche-m3-diagnostic-final-race.jsonl。计数包含子测试，不能解释为292个独立顶层测试。Go工具链1.22.12 darwin/arm64。

## 独立质量与额外边界

质量角色只读审查并执行真实socket探针（httptest服务返回含敏感内容的非法SSE），验证上游200、malformed_chunk及日志不含API key、请求体或响应正文。命令：

```sh
go test -count=1 -run '^TestIndependentSocketProbeRedactsMalformedSSEAndRecordsTransport$' -overlay /private/tmp/porsche-m3-diagnostic-overlay.json ./internal/whitelabel
```

实际输出：ok github.com/porsche/ai-gateway-go/internal/whitelabel 0.418s；日志/private/tmp/porsche-m3-socket-probe.log。质量角色给出限定本地PASS，不代表线上恢复。

协调者追加redirect探针发现诊断遗漏：本地fake Transport返回302，http.Client.CheckRedirect返回错误，真实client.Do同时返回response和error，但Chat先处理error，导致upstream_response_received=false/status=0。命令：

```sh
go test -count=1 -run '^TestDiagnosticRedirectResponseWithError$' -overlay /private/tmp/porsche-m3-redirect-overlay.json ./internal/whitelabel
```

RED实际输出：received=false status=0; want true/302。仅诊断信息不准确，原公开503/请求1次保持。将用最小调整先记录非nil响应的安全状态再处理错误，保留客户端重定向策略、不读取Location/body。

## 最终修正与门禁

重定向用例已转为持久TestDiagnosticRedirectResponseWithError；新增redirect_rejected白名单原因，先记录非nil response的安全HTTP状态再处理error，保留原公开503、一次请求及原重定向策略。后端PM已只读复核此最终差异，限定本地规格PASS继续有效。

修正后原样重跑完整门禁：go test -json -p 1 ./... -count=1为293个pass事件、0fail/skip；诊断/适配器/handler专项-race为46个pass事件、0fail/skip；go vet ./...、go build ./...、git diff --check均退出0。最终原始输出：/private/tmp/porsche-m3-diagnostic-final2-full.jsonl、/private/tmp/porsche-m3-diagnostic-final2-race.jsonl，前一轮292/45结果为修正前快照。

两次独立测试容器已按精确ID与codex.task标签验证后移除，inspect确认不存在，临时测试凭据文件已删除；清理证据/private/tmp/porsche-m3-diagnostic-cleanup.json。没有操作数据库命名卷或原有容器。重跑集成测试须重新提供独立TEST_*，不复用已销毁的临时地址。

最终独立质量复核执行 `go test ./internal/diagnostics ./internal/whitelabel -run 'TestDiagnosticRedirect|TestNetwork|TestTrace' -count=1`，实际输出diagnostics 0.744s、whitelabel 1.282s均ok；确认最终response+error修正无公开行为/泄露/重放变化，延续限定本地质量PASS。

交付结论：go-008仅本地诊断能力passing；go-004真实上游验收仍blocked，前端M3-11仍FAIL，线上根因未知。后续发布将重启后端，须独立明确授权，现有前端发布授权不覆盖此操作。保留fix/m3-sse-diagnostics分支及工作树，不自动合并或部署。

## 本地候选产物与未完成镜像

代码候选04ed72806f5ca139d219d166452e0473ba5bf1a1。嵌套worktree最初构建的Go元数据错误指向父仓库e0efac2且modified=true，未采纳；已在独立本地clone精确检出04ed728后重新构建server及bootstrap-root。go version -m分别断言revision=04ed72806f5ca139d219d166452e0473ba5bf1a1、modified=false、Go1.22.12/linux/amd64。

发布材料：/private/tmp/porsche-m3-backend-04ed728-linux-amd64.tar.gz；包SHA256 257160c725d9222550b1a1d4bd6ca86f8b7733a95fef5734fc95e718262537a6。包含源码归档、两个二进制、构建信息、运行镜像Dockerfile与manifest，不包含生产配置。server SHA256 9dd1f8e6f7623a225c4f76907e708955f8303b8f2ddd2219fd6fc0dd78eab9d8。

本地镜像构建未完成：Docker Hub alpine:3.20 metadata拉取DeadlineExceeded；本地已有缓存为arm64，不能冒充amd64镜像。未生成或推送候选镜像，未上传或部署到服务器。Dockerfile仅准备运行镜像，不能据此声称镜像可运行或服务已上线。清单见validation/m3-backend-diagnostic-candidate.json；下步先在可用构建环境完成amd64镜像并核对其二进制/源码标签，具备具体镜像ID与回滚计划后再申请后端发布授权。

## 2026-09-03：镜像已完成，私有上传与后端发布待授权

- 代码04ed728的linux/amd64诊断镜像已在本机离线组装、导出和重新加载；精确ID sha256:a69cfdab1cc8e18056286ae3991669d37515994041664b3fed5b6290ac602316，归档SHA256 af8a122d1fa5018a981d4757aff03b0b204ef8048c38f2632eb47e343ad0c580。
- 来源标签、两个二进制哈希、架构、入口、CA均核验；无凭据/无网络启动按预期缺JIEKOU_API_KEY拒绝，不能当作真实服务健康。
- 原私有源码/二进制构建包上传被自动审批拒绝，未执行。远端仅构建公开基础层并下载，本机加入私有二进制；私有镜像尚未上传，生产后端未替换。
- 具体上传、三锁、运行态配置快照、切换及失败回滚准备见后端docs/superpowers/plans/2026-09-03-m3-backend-release.md；待用户明确授权，计划尚未在生产执行或演练。此前镜像构建超时为已解除的历史阻塞。
- 后端PM书面确认发布准备要求，不代表用户上线授权或线上M3签收。M3-11仍FAIL，剩余2次gpt-5.4-nano×max_tokens32预算保留。

后端PM随后只读复核最终发布计划和两份JSON，书面确认材料足够提交用户授权申请、摘要一致且无阻塞申请的问题；不独立验证镜像内容、不授权部署、不签M3。协调者复核归档SHA、两仓清单一致及JSON/diff检查通过。

## 2026-09-03：后端诊断已发布，SSE第二次仍失败并定位解析阶段

- 用户明确确认上传及后端替换后完成发布：源码04ed728，镜像sha256:a69cfdab1cc8e18056286ae3991669d37515994041664b3fed5b6290ac602316；新容器9425ea244ad71944ef78474cc405208fbbbe7eb22fdffd2b81e269d328d85b0c于05:50:09Z启动，源站/公网health严格200。旧d2de587容器以ai-gateway-go-acceptance-rollback-1788414605780771454保留，未执行生产回滚；前端158a00e哈希保持。
- 首次预检因OomKillDisable的null/false表示差异安全停止，未停旧服务。公开基础镜像两版本API探针证实创建规范化，限定兼容该默认值后重新预检；true仍拒绝。三锁、私有运行配置JSON快照、候选create后逐字段比对均执行，成功后私有快照删除。脚本已归档，仅适用本次精确对象，不是可直接复用的常规发布入口。
- 真实SSE第2次于05:51:52Z发送：gpt-5.4-nano/max_tokens32，1POST/0refresh，HTTP503、gateway_upstream_unavailable、0帧。请求ID哈希与候选日志匹配，源码revision也匹配；上游返回200，sse_stream failed/malformed_chunk，首帧未发出，auth/catalog/前置quota及消息写入均success，assistant/usage/final_write未运行。尚不确定具体哪个字段或数据类型不兼容，不能宣称修复或归责供应商。
- 应用日调用1→2、剩余99→98、token计数0；这不是供应商零费用证明。测试会话删除200、注销204。预算累计2/3，剩1次×32，不重置、不盲目重放。
- M3-11仍FAIL、go-004仍blocked、web-009仍in_progress。后续先核对解析器拒绝条件与脱敏结构证据，再决定是否使用最后一次预算；不放宽白名单投影、不透传上游正文、不把health通过当M3签收。

## 本轮双方与质量书面复核

后端project_manager只读复核release-result、浏览器结果、候选诊断及预算，确认发布记录与第2次失败记录一致，request_id_sha256完全匹配，故障位于upstream200之后、首个公开帧之前的chunk投影校验；M3-11仍FAIL。质量角色独立只读复核同组证据，给出PARTIAL：认可部署健康及诊断链路，不认可有效SSE通过或整体M3签收。两角色均未独立执行部署/线上生成。

下一步责任与准入：后端PM先对照已约定上游SSE规范和静态样例，梳理JSON/字段类型、id/object/created、usage-only、choice/delta/tool_calls各拒绝条件；需要补诊断时仅设计固定原因枚举和固定字段路径/类型类别，不记录原始帧、值、提示词、工具参数或任意字段名，不提前放宽投影白名单。执行角色先以合成fixture证明各分支分类与脱敏，质量角色复核后才能准备后续候选。待下一次能区分失败分支时，协调者再按剩余1次×32预算安排复测，不能盲目重试。此后续方案尚未实施或发布。
