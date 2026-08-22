# PRD-260820 白牌上游接入进度

## 当前唯一活动功能

`go-004`：JieKou AI 白牌上游接入（`in_progress`）。本轮已完成隔离环境、后端基线、前端恢复验证，以及 Task 2 的配置、错误与请求校验；Task 3 为下一步。

## 已验证基线（2026-08-21）

- 后端：在 `/Users/xuzhihao/code/Porsche/.worktrees/white-label-upstream` 运行 `GOCACHE=/private/tmp/porsche-go-build-cache ./init.sh` 成功。环境为 Go 1.22.12，脚本中的 `go test ./...` 全部通过；未设置 `RUN_START_COMMAND=1`，因此未启动服务。
- 前端隔离：`Porsche-Web/.worktrees` 已由 Git 忽略，已创建 `feature/white-label-upstream` 工作树，主工作区未改动。
- 前端恢复：在 `llm-platform` 工作树中，`npm ping` 成功；`npm install --package-lock=false` 完成且未产生 `package-lock.json` 变更；`npm test` 8/8 通过；`npm run build` 通过，仅输出既有警告。

## Task 2 配置、错误与请求校验（2026-08-22，完成）

- RED：在 `0fe9b36` 上加入回归测试后，`GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/whitelabel -run 'Test(PublicInvalidRequestErrorMatchesContract|ValidateRequestEnforcesChatContract|ValidateMediaURLRejectsLocalAndMappedAddresses|ValidateRequestAcceptsSafeVideoURLAndRejectsUnsafeSources)' -count=1` 失败：大于 16384 的正 `max_tokens` 被拒绝、单标签十六进制 IPv4-like host 被接受，且合法 `video_url` content part 被拒绝。
- GREEN：`GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/whitelabel -count=1` 与 `GOCACHE=/private/tmp/porsche-go-build-cache go test ./...` 均通过。
- 校验范围：未知顶层参数返回 `unsupported_parameter`；`max_tokens` 仅要求正整数并交由上游实施上下文限制；图片与视频 HTTPS URL 均拒绝 userinfo 和非公网字面地址，且验证不进行 DNS 解析；视频不接受 data URI。
- P2 收尾：数据图片解码上限为 8 MiB；回归测试确认 8 MiB 图片 data URI 的完整请求体小于 12 MiB 并可接受，而 8 MiB + 1 字节被拒绝。测试先在旧实现上失败（两/三标签十六进制 IPv4-like host 与超限图片均被接受），随后 `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/whitelabel -count=1`、`GOCACHE=/private/tmp/porsche-go-build-cache go test ./...` 与 `git diff --check` 全部通过。

## 阻塞与未验证项

- 未进行任何真实 JieKou AI 上游目录、Chat 或 SSE 冒烟；该验证仍需要部署环境的白牌配置。

## 下一步（Task 3）

在具备部署环境的白牌配置后，完成真实上游目录、Chat 与 SSE 冒烟。
