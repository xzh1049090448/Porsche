# Porsche Subagent-Driven 业务 Agent 集成设计

**日期：** 2026-09-09
**状态：** 用户已于2026-09-09确认
**范围：** `/Users/xuzhihao/code/Porsche` 与 `/Users/xuzhihao/code/Porsche-Web` 的 Agent 配置和协作文档；不修改业务代码、依赖、部署配置或生产状态。

## 目标

将 Subagent-Driven Development（SDD）作为任务生命周期与审查门禁，将 Porsche/Porsche-Web 业务 Agent 显式映射为各阶段的领域执行者。解决当前角色不会自动映射、缺少独立规格审查、双重 Controller、低风险任务成本过高，以及 Agent 不可用时容易误报协作成功的问题。

## 方案选择

采用“受控映射”，不采用完全分离或全量嵌套：

- 完全分离会让通用 SDD 子 Agent 重复或遗漏 Porsche 的认证、MySQL、Redis、SSE、接口与验收约束。
- 全量嵌套会形成外层 Controller 与仓库协调者双重调度，导致任务状态、revision、审查结论和返工责任不清。
- 受控映射保留 SDD 的新鲜上下文、规格审查、质量审查和返工闭环，同时复用业务 Agent 的权限、技术边界和证据要求。

## 编排边界

每个仓库在一个执行链中只能有一个 Controller：

- 后端 Controller：`project_manager`。
- 前端 Controller：`front_end_project_coordinator`。
- 跨仓库目标由当前用户会话协调，但不得在仓库 Controller 内再次启动一套外层 SDD Controller。
- 两个仓库之间仅通过 `project_manager` 与 `front_end_project_coordinator` 交换版本化交接包。
- Role TOML 存在不等于角色已经加载或可调用；运行前必须核对实际工具能力、角色、模型、权限和工作目录。

## 角色映射

### 后端

| SDD 阶段 | Porsche 角色 | 职责 |
| --- | --- | --- |
| Controller | `project_manager` | 拆解任务、控制顺序、回答问题、汇总结论；保持只读 |
| Explorer | `architect_explorer` | 输出架构、影响面、约束、风险和未验证假设 |
| Implementer | `backend_worker` | 在授权 worktree 中实施、测试、自审和报告 |
| Spec Reviewer | 新增 `spec_compliance_reviewer` | 对照完整任务逐项检查缺失、额外实现和误解；保持只读 |
| Security/Quality Reviewer | `security_reviewer` | 审查最终 diff 的认证、授权、数据、协议和运维安全 |
| Test Verification | `test_engineer` | 在明确隔离环境中执行测试，区分 PASS、FAIL、SKIPPED |
| Final Gate | `project_manager` | 仅汇总独立证据，不代替任何审查者 |

顺序为 Explorer → Implementer → Spec Review → final Security Review → Test Verification → Final Gate。初步安全分析可以提前进行，但最终安全结论必须针对最终 diff。

### 前端

| SDD 阶段 | Porsche-Web 角色 | 职责 |
| --- | --- | --- |
| Controller/Explorer | `front_end_project_coordinator` | 需求、影响面、契约差异、可行性和执行交接；保持只读 |
| Implementer | `front_end_developer` | 页面、组件、状态、接口接入、自测和证据 |
| Spec Reviewer | 新增 `front_end_spec_compliance_reviewer` | 对照完整任务和契约检查缺失、越界与误解；保持只读 |
| Quality/Test Gate | `front_end_quality_gate` | 独立审查最终 diff 和测试证据；不得自行修复 |
| Final Gate | `front_end_project_coordinator` | 汇总独立结论，不代替规格或质量审查 |

顺序为 Explorer → Implementer → Spec Review → Quality Gate → Final Gate。只读 Quality Gate 无法执行的写入型测试由授权 writer 运行，并提供命令、退出码和脱敏原始输出供其独立判断。

## 单任务协议

Controller 给每个子 Agent 的任务包必须包含：

- 完整任务正文和验收标准，不只提供计划文件名或任务编号；
- 仓库、worktree、branch、base revision、当前 revision 和现有修改；
- 允许写入的路径、禁止操作和是否允许提交；
- 必读的 `AGENTS.md`、领域文档和任务相关规范；
- 前置任务的已验证输出，而非未经核实的完成声明；
- 必须执行的验证、允许跳过的条件以及证据格式；
- 预期状态：`DONE`、`DONE_WITH_CONCERNS`、`NEEDS_CONTEXT` 或 `BLOCKED`。

实现者完成后必须由不同角色审查。规格未通过不得进入质量审查；质量或测试发现问题必须经 Controller 退回原实现者，形成“修复 → 同一类审查重新验证”的闭环。新 diff 会使旧的最终审查结论失效。

## 内容寻址审查快照

审查门禁以内容寻址的 review snapshot 为准，不能只用 branch、HEAD、commit 或 `git diff` 表示“同一版本”。两仓各自在 `docs/agents/review_snapshot.py` 保存逐字节相同的通用 Python 3.9 标准库 helper，并保存逐字节相同的 `docs/agents/test_review_snapshot.py`；每次变更都用 `cmp` 与 SHA-256 同时验证两仓 helper/test 一致性，防止算法或威胁模型漂移。Helper 的 baseline/snapshot/verify、scope 规范化、Git 候选、index flags、文件 type/mode/symlink、路径 confinement、canonical JSON 和 snapshot ID 语义以测试固定。

Helper 使用 v2 baseline/snapshot schema，同时采集相对 baseline HEAD 的 worktree diff 与 cached diff，并以 `git ls-files --stage -z` 把每个路径的 index mode、blob OID、stage 或 absent 写入 canonical 指纹。这样 staged 内容即使被同路径工作树内容遮蔽也会改变 scope 内 snapshot ID；scope 外 index 漂移则与工作树漂移一样阻塞。Rename/copy 两端同时从 worktree/cached name-status 进入候选。Scope 内候选还 union 不应用 exclude 的全量 untracked 路径，所以 `.gitignore`、`.git/info/exclude` 与 global excludes 都不能隐藏 exact/prefix 内文件，ignore 规则切换也不能使它从 manifest 消失。Scope 外持续 ignored 的 untracked 文件不进入 baseline，内容变化不影响 snapshot；若 scope 外文件在 baseline 后切换 ignored/visible 状态，visible-untracked 的 baseline 对比将其识别为新增或消失并阻塞。V1 evidence 必须拒绝并重新生成，不能迁移或沿用旧 ID；`assume-unchanged`/`skip-worktree` 路径仍由 cached tracked 全集与 index 指纹保护。

Schema 名称通用化为 `review-baseline-v2` 与 `review-snapshot-v2`，旧 branded schema 同样失效。Scope、baseline、snapshot 和 contract 的 JSON 必须走同一严格 loader，在任意嵌套层拒绝重复键，并拒绝 `NaN`、`Infinity`、`-Infinity` 以及 dict key、string value、list 元素中任何不能严格编码为 UTF-8 的未配对 Unicode surrogate。Canonical JSON 编码层另行把 `UnicodeEncodeError` 转换为 `SnapshotError` 作为纵深防护。解析或验证错误统一安全返回 exit 2，不输出 traceback 或继续生成审查结论。

Scope 文件可以事先准备；contract 仍是受路径 confinement 保护的仓内文件。Baseline/snapshot 是私有审查证据，必须由授权 writer 保存到实际 Git worktree 外的私有任务目录。Helper 不再承担证据文件写入：`baseline`/`snapshot --output` 只接受精确的 `-` 并把 canonical JSON 写到 stdout；任何绝对、相对、existing regular/symlink/hardlink 或并发路径目标都必须安全 exit 2、无 traceback 且不写文件。授权 writer 只能把原始 stdout 字节写入外置私有目录中全新 exclusive 创建的文件；目标不得已存在，不得为或链接到既有 regular/symlink/hardlink，且不得覆盖或链接任何现有文件。Snapshot/verify 对 baseline/snapshot 输入保留 resolved worktree 外检查，防止相对路径、父目录和 symlink 绕过。只读角色不得自行落盘或把证据写回仓库。

未合并 index 的 stage 1/2/3 没有唯一可审版本，helper 必须阻塞；gitlink 的 mode `160000` 也不受当前 regular/symlink/absent 内容模型支持，必须显式阻塞而非生成不完整 ID。

前端调用必须始终传 `--contract interface-contract.json`。后端没有本地 contract 文件，调用时不得传 `--contract`，所以后端 manifest 的 `contract` 为 `null`；这不是契约通过结论。任何跨仓库接口任务仍由后端 `project_manager` 与前端 `front_end_project_coordinator` 在任务包中另外绑定 Porsche-Web contract 的内容 SHA-256、version 和 status，并交由能访问该文件的角色核对。

Controller 保持只读，只能用 `--output -` 在 stdout 生成 baseline/snapshot；授权 writer 按全新 exclusive 文件规则原样持久化 canonical stdout 并记录内容哈希。Spec、Security/Quality 和 Test 角色必须针对 writer 保存的同一 scope/baseline/snapshot 独立运行无写 `verify`。后端依次要求同 snapshot ID 与 final revision 的 `SPEC_PASS`、Security 通过和 Test 验证；前端依次要求同 snapshot ID/final revision/contract 证据的 `SPEC_PASS` 与 Quality Gate。任一 manifest 组成或 scope 外 baseline 指纹发生变化，旧 snapshot 及所有审查结论立即失效。

任务包必须包含 scope/baseline/snapshot 路径与内容哈希、helper 命令、退出码、stdout ID、授权 writer，以及跨仓库任务的 contract 独立绑定。Helper 不可用时流程为 `BLOCKED`；人工 fallback 也必须携带同样的 snapshot 证据，不允许退化成未经内容绑定的人工声明。

## 风险分级

### 完整流程

用于认证、会话、权限、用户管理、MySQL 模型与迁移、Redis、管理操作、支付或余额、SSE、上游协议、部署安全、跨仓库接口和多模块改动。

### 标准流程

用于单仓库、中等风险、边界明确的行为修改：Explorer 可由 Controller 完成，但保留独立 Implementer、Spec Review、Quality/Test Gate。

### 精简流程

用于文案、注释、无行为变化的格式调整和纯文档小改动：由授权 writer 修改，执行 `git diff --check` 和适用的静态检查；Controller 判断是否需要一次独立只读审阅。不得用精简流程处理认证、数据库、接口或生产操作。

## 跨仓库协作

版本化交接包至少包含：

- 双方仓库、worktree、branch 和 revision；
- `interface-contract.json` 的路径、revision 和状态；
- API、GUID、JSON、SSE、认证、错误码和分页差异；
- 责任人、依赖关系、联调顺序和验收用例；
- Mock、单测、MySQL/Redis、浏览器、真实上游等证据层级；
- 未决事项、阻塞项以及双方可追溯确认。

没有收到对方协调者的实际确认时保持 draft，不得代签。跨任务通信不可用时，输出人工交接包，不自行创建外部任务，也不声称已经建立连接。

## 配置维护

- 后端暂以 `.codex/agents` 为唯一实际配置源，不为追求目录对称而新增 `.agents`。
- 前端继续以 `.agents` 为维护源、`.codex/agents` 为发现副本；同名文件必须逐字节一致。
- 新增前端规格审查角色时同时增加维护源和发现副本。
- Agent 文档记录映射表、启动检查、风险分级和故障降级；`AGENTS.md` 只保留所有 Agent 都必须知道的入口规则。
- 配置验证必须解析 TOML、检查字段/权限/模型、前端副本一致性、引用路径存在性，并包含权限漂移和副本漂移的负向探针。

## 故障降级

若具名 Agent 不可调用：

1. 报告实际缺失的角色或工具能力。
2. 在允许创建通用子 Agent 时，完整注入对应业务角色指令、工作目录、权限和任务包；不得仅赋予角色名称。
3. 无法保证只读权限时，不用宽权限 Agent 冒充 reviewer。
4. 子 Agent完全不可用时，输出 Explorer → Implementer → Spec Review → Quality/Test 的人工执行包。
5. 任何降级都不得把未运行、跳过或仅由实现者自证的步骤标记为通过。

## 文件范围

计划中的允许修改范围：

- Porsche：`AGENTS.md`、`.codex/agents/*.toml`、`docs/agents/*`、本设计对应的实施计划。
- Porsche-Web：`AGENTS.md`、`.agents/*.toml`、`.codex/agents/*.toml`、`docs/agents/*`、本设计对应的实施计划。

不修改 `progress.md`、`feature_list.json`、`interface-contract.json` 内容或业务源代码，除非后续计划明确证明流程文档必须引用但不改变其中的业务状态。

## 验收标准

1. 两个仓库明确单一 Controller 和跨仓库唯一对口。
2. 后端与前端各有独立、只读的规格符合性 Reviewer。
3. 配置和文档明确 SDD 阶段到业务 Agent 的映射及严格审查顺序。
4. 三档风险流程有互斥、可判定的适用条件，高风险事项不能降级为精简流程。
5. 每个任务包包含版本、写入范围、规范、验证和状态协议。
6. 明确角色不可用、只读权限不可保证、跨仓库通信不可用时的降级路径。
7. 前端 `.agents` 与 `.codex/agents` 同名配置逐字节一致。
8. 所有 TOML 可解析，Markdown 本地引用有效，`git diff --check` 通过。
9. 验证只证明配置和文档有效，不宣称 Agent 已加载或业务验收通过。
10. 两仓 review snapshot helper 与测试逐字节一致，52 项测试在两仓分别通过；覆盖 scope 内 staged-only ID 漂移、scope 外 staged-only 阻塞、旧 schema 拒绝、四类 JSON 的重复键/非有限数/未配对 surrogate 严格解析、canonical UTF-8 纵深防护、外置证据输入路径、所有 path output（new/existing/relative/symlink/hardlink/并发）安全拒绝且无写、canonical stdout、三类 ignore 来源下 scope 内内容绑定及规则切换、scope 外持续 ignored 隔离与 visible→ignored 防隐藏、symlink/relative 输入绕过、未合并 index 与 gitlink 阻塞，并保留 index flags 回归；后端 `contract=null` 的边界及跨仓库 contract 独立绑定均有角色规则覆盖。

## 非目标

- 不修改 Subagent-Driven 插件本体。
- 不自动启动子 Agent、创建新 Codex 任务或建立后台调度。
- 不修改模型、推理强度或既有角色的沙箱权限。
- 不运行部署、生产迁移、Root 初始化、密钥轮换、推送或破坏性清理。
- 不把静态配置验证描述为真实的多 Agent 或前后端联合验收。
