# Porsche Agent 编排规范

## 单一 Controller

- 后端执行链唯一 Controller 是 `project_manager`；外层会话只提供用户目标和跨仓库协调，不在其内部再启动第二套 Controller。
- `project_manager` 保持只读，只汇总独立角色证据，不代替实现、规格、安全或测试角色。

## SDD 角色映射与顺序

完整流程严格按以下顺序执行：

1. `architect_explorer`：只读输出影响面、约束、风险和未验证假设。
2. `backend_worker`：在授权 worktree 实现、测试、自审并返回明确状态。
3. `spec_compliance_reviewer`：针对最终实现逐项核对完整任务；未通过时退回原 Worker，修复后重新规格审查。
4. `security_reviewer`：仅在同一 review snapshot 已获得 `SPEC_PASS` 后审查该 snapshot 的最终 diff。
5. `test_engineer`：仅在同一 review snapshot 已获得规格与安全通过证据后执行独立验证，区分 PASS、FAIL、SKIPPED。
6. `project_manager`：仅在前述门禁关闭后汇总结论。

任何 review snapshot 组成变化，包括被审范围的实现、测试、配置或文档变更，都会使 Spec Review 及所有下游结论失效。Security Review 或 Test Verification 发现需要修改的问题时，必须由 `project_manager` 退回原 `backend_worker`；修改后生成新 snapshot，从 Spec Review 开始，并依次重新执行 Security Review 和 Test Verification。

## 任务包与状态

每次委派必须给出完整任务正文与验收标准、仓库/worktree/branch/base revision/current revision、现有修改、允许写入路径、禁止操作、提交授权、必读规范、前置证据、验证命令和证据格式。完整流程任务包还必须给出规范化 scope、writer 持久化的 baseline/snapshot 路径与内容哈希、生成和 verify 的完整 helper 命令、退出码与 stdout ID，以及被授权持久化证据的 writer；`project_manager` 必须核验任何 scope 外变化并在继续前重建 baseline/snapshot。执行者返回 `DONE`、`DONE_WITH_CONCERNS`、`NEEDS_CONTEXT` 或 `BLOCKED`；不得用未经核验的完成声明推进门禁。

## Review snapshot ID

唯一规范算法是 [review_snapshot.py](review_snapshot.py)，使用 Python 3.9 标准库。所有阶段必须在仓库根目录调用本地 `python3 docs/agents/review_snapshot.py`，不得手工构造、编辑或以另一实现替代其结果；helper 不可用或执行失败时流程为 `BLOCKED`，人工交接也必须包含同样的 snapshot 证据，不能降级成只记录 Git revision 或 diff。

```sh
python3 docs/agents/review_snapshot.py baseline --scope <scope.json> --output -
python3 docs/agents/review_snapshot.py snapshot --scope <scope.json> --baseline <private-task-dir>/baseline.json --output -
python3 docs/agents/review_snapshot.py verify --scope <scope.json> --baseline <private-task-dir>/baseline.json --snapshot <private-task-dir>/snapshot.json
```

后端不传 `--contract`，所以 manifest 的 `contract` 必须为 `null`。这只表示后端仓库没有本地 contract 文件，绝不证明跨仓库契约通过。跨仓库接口任务仍必须由 `project_manager` 在任务包中单独绑定 Porsche-Web `interface-contract.json` 的内容 SHA-256、version 和 status，并由能访问该契约的对应协调者/审查角色核验。

Scope JSON 可事先准备，只包含仓库相对的精确 `paths` 和以 `/` 结尾的 `prefixes`。Scope、baseline、snapshot 与 contract 的 JSON 都经过同一严格 loader：任意层重复键及 `NaN`、`Infinity`、`-Infinity` 均以 exit 2 拒绝。Contract 仍是受 confinement 保护的仓内文件；baseline/snapshot 是证据，必须保存到实际 Git worktree 之外的私有任务目录。Helper 的 `baseline`/`snapshot --output` 只接受精确的 `-` 并输出 canonical JSON；任何路径值（包括绝对、相对、existing regular/symlink/hardlink 或并发目标）都以 exit 2 拒绝且不写文件。授权 writer 必须把原始 stdout 字节保存到外置私有目录中的全新 exclusive 文件；目标不得已存在，不得为或链接到 regular/symlink/hardlink，且不得覆盖或链接任何既有文件。Snapshot/verify 继续以 resolved path 检查 baseline/snapshot 输入，拒绝 worktree 内位置以及相对路径、父目录和 symlink 绕过。

Helper 规范化 scope，并以 baseline HEAD 为 Git diff base，同时采集 worktree diff 与 `git diff --cached --name-status -z`，覆盖 tracked/staged/unstaged/untracked/deleted、rename/copy 两端，以及 cached tracked path（包括 `assume-unchanged` 和 `skip-worktree`）。Scope 内 exact/prefix 还从不应用 exclude 的全量 untracked 集合补入候选，因此 `.gitignore`、`.git/info/exclude` 或 global excludes 都不能隐藏待审文件，规则切换也不能让既有 scope 内文件从 manifest 消失。Scope 外持续 ignored 的 untracked 内容不进入 baseline，内容变化不影响 snapshot；若 scope 外文件在 baseline/snapshot 之间切换 ignored/visible 状态，现有 visible-untracked baseline 比较会把新增或消失识别为 drift 并阻塞。每个路径除当前工作树 type/mode/symlink 目标/内容哈希外，还通过 `git ls-files --stage -z` 记录 index 的 mode、blob OID、stage 或 absent；因此 staged-only 内容和 mode 都进入 canonical manifest。任何 stage 非 0 的未合并 index 都不可形成确定快照，helper 必须 `BLOCKED`；index mode `160000` 的 gitlink 也明确不支持并阻塞。Baseline 以同一结构冻结 scope 外候选；任一 scope 外工作树或 index 的新增、删除、内容、类型、mode、OID 或 stage 变化都会阻塞，必须由 `project_manager` 核验后由授权 writer 保存新证据。路径 confinement 会拒绝中间 symlink 或越出 worktree；最终 symlink 只哈希其 UTF-8 链接目标。Canonical JSON、UTF-8 路径排序、`100644`/`100755`/`120000`/absent 语义及 ID 计算均以 helper 为准。

Baseline 与 snapshot 使用通用 `review-*-v2` schema；所有 branded schema 及 v1 baseline/snapshot 证据已经失效并必须被拒绝。升级后不得复用或转换旧 ID，必须由授权 writer 从原始 scope 重新生成外置 baseline 和 snapshot，并重新执行完整审查链。

`project_manager` 保持只读：实施前运行 `baseline --output -`，每次派发 Spec、Security 或 Test 前运行 `snapshot --output -`，只在 stdout 接收 canonical JSON；授权 writer 按上述全新 exclusive 文件规则原样持久化 stdout 到 worktree 外私有任务目录并记录内容哈希，Controller 记录 helper 命令、退出码和 stdout ID。`spec_compliance_reviewer`、`security_reviewer` 与 `test_engineer` 必须对 writer 保存的同一 scope/外置 baseline/外置 snapshot 各自运行无写 `verify`，不得只信任上游报告或把证据复制回仓库。

`SPEC_PASS` 必须绑定已验证的 snapshot ID 与 final revision；缺少 scope、baseline、snapshot，或 verify 失败时只能返回 `SPEC_BLOCKED`。Security 只接受同一 snapshot ID/final revision 的 `SPEC_PASS`，独立 verify 后才可审查。Test 只接受同一 snapshot 的规格与安全通过证据，独立 verify 后才可执行。`test_engineer` 即使拥有 `workspace-write`，也不得修改被审内容后继续沿用旧 snapshot；测试或修复造成任何组成变化时，旧 Spec/Security/Test 全部失效并重新走完整审查链。

## 风险分级

- 完整流程：最高优先级；认证、会话、权限、用户、MySQL/迁移、Redis、管理操作、支付/余额、SSE、上游协议、部署安全、跨仓库接口或多模块改动命中任一项即使用完整流程。
- 标准流程：仅适用于未命中任何完整流程触发项的单仓库、中等风险、边界明确的行为修改；Explorer 可由 Controller 完成，但保留独立 Implementer、Spec Review、Security/Quality Review 和 Test Verification。
- 精简流程：仅适用于未命中完整流程或标准流程，且无行为、契约、权限、依赖或生产影响的文案、注释、无行为格式调整和纯文档小改；由授权 writer 修改并运行 `git diff --check` 与适用静态检查。

若无法确定风险档位，选择更高档流程。

## 故障降级

Role TOML 存在不证明角色可调用。先核对实际工具、角色、模型、权限和工作目录。具名角色不可用但允许通用子 Agent 时，完整注入角色指令和任务包；无法保证只读权限时不得用宽权限 Agent 冒充 Reviewer。子 Agent 不可用时输出 Explorer → Implementer → Spec Review → Security/Test 的人工交接包，未运行、跳过或仅由实现者自证的步骤不得标记通过。
