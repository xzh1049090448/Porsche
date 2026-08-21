# PRD-260820 白牌上游接入进度

## 当前唯一活动功能

`go-004`：JieKou AI 白牌上游接入（`in_progress`）。本轮仅完成隔离环境与基线固定；未开始 Task 2。

## 已验证基线（2026-08-21）

- 后端：在 `/Users/xuzhihao/code/Porsche/.worktrees/white-label-upstream` 运行 `GOCACHE=/private/tmp/porsche-go-build-cache ./init.sh` 成功。环境为 Go 1.22.12，脚本中的 `go test ./...` 全部通过；未设置 `RUN_START_COMMAND=1`，因此未启动服务。
- 前端隔离：`Porsche-Web/.worktrees` 已由 Git 忽略，已创建 `feature/white-label-upstream` 工作树，主工作区未改动。

## 阻塞与未验证项

- 前端已追踪的 `llm-platform` 没有 `package-lock.json`。`npm ci` 因此以 `EUSAGE` 失败，`npm test` 与 `npm run build` 未运行。未添加或提交主工作区中未追踪的 lockfile。
- 未进行任何真实 JieKou AI 上游目录、Chat 或 SSE 冒烟；该验证仍需要部署环境的白牌配置。

## 下一步

在解决前端已追踪 lockfile 缺失的基线阻塞后，再开始 PRD-260820 Task 2 的配置、错误与请求校验 TDD 工作。
