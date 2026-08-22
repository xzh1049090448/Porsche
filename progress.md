# PRD-260820 白牌上游接入进度

## 当前唯一活动功能

`go-004`：JieKou AI 白牌上游接入（`in_progress`）。本轮完成隔离环境、后端基线与前端恢复验证；未开始 Task 2。

## 已验证基线（2026-08-21）

- 后端：在 `/Users/xuzhihao/code/Porsche/.worktrees/white-label-upstream` 运行 `GOCACHE=/private/tmp/porsche-go-build-cache ./init.sh` 成功。环境为 Go 1.22.12，脚本中的 `go test ./...` 全部通过；未设置 `RUN_START_COMMAND=1`，因此未启动服务。
- 前端隔离：`Porsche-Web/.worktrees` 已由 Git 忽略，已创建 `feature/white-label-upstream` 工作树，主工作区未改动。
- 前端恢复：在 `llm-platform` 工作树中，`npm ping` 成功；`npm install --package-lock=false` 完成且未产生 `package-lock.json` 变更；`npm test` 8/8 通过；`npm run build` 通过，仅输出既有警告。

## Task 2 安全校验（2026-08-22）

- RED：将 Task 2 验证器临时还原至 `ad93332a` 后，`GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/whitelabel -run 'Test(ValidateRequestEnforcesChatContract|ValidateMediaURLRejectsLocalAndMappedAddresses|ValidateDataImageAcceptsTenMiB)' -count=1` 失败：流式/seed/json_schema 请求被拒绝、十六进制 IPv4-like host 被接受、10 MiB data image 被拒绝；旧错误响应结构也缺少 `error.request_id`。
- GREEN：`GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/whitelabel -count=1` 与 `GOCACHE=/private/tmp/porsche-go-build-cache go test ./...` 均通过。

## 阻塞与未验证项

- 未进行任何真实 JieKou AI 上游目录、Chat 或 SSE 冒烟；该验证仍需要部署环境的白牌配置。

## 下一步

在具备部署环境的白牌配置后，完成真实上游目录、Chat 与 SSE 冒烟，再开始 PRD-260820 Task 2 的配置、错误与请求校验 TDD 工作。
