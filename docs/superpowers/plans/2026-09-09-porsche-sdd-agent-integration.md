# Porsche Subagent-Driven 业务 Agent 集成实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 SDD 阶段显式映射到 Porsche/Porsche-Web 业务 Agent，补齐独立规格审查，并建立单一 Controller、风险分级和安全降级规则。

**Architecture:** 后端由 `project_manager`、前端由 `front_end_project_coordinator` 分别担任唯一仓库 Controller；实现、规格审查、质量审查和测试由不同角色顺序执行。共享编排规则写入各仓库的 `docs/agents/orchestration.md`，`AGENTS.md` 只保留强制入口，角色 TOML 引用该文档并保持既有权限和业务约束。

**Tech Stack:** Markdown、TOML、Go 1.22 与 `github.com/pelletier/go-toml/v2`；前端双份 Agent 配置使用逐字节比较验证。

---

## 执行保护

- 两个仓库当前均有未提交改动；每项任务开始和结束都保存 `git status --short` 与目标文件 diff，不重置、不清理、不覆盖无关内容。
- 本计划不修改业务代码、依赖、`progress.md`、`feature_list.json` 或 `interface-contract.json`。
- 不运行安装依赖、生产构建、部署、迁移、Root 初始化、推送或破坏性清理。
- 未获得单独提交授权时跳过每项任务末尾的 commit，仅报告建议提交文件。
- 本轮配置/文档验证不宣称具名 Agent 已加载，也不宣称业务或联合验收通过。

### Task 1: 建立后端 SDD 编排规范和独立规格 Reviewer

**Files:**
- Create: `/Users/xuzhihao/code/Porsche/docs/agents/orchestration.md`
- Create: `/Users/xuzhihao/code/Porsche/.codex/agents/spec_compliance_reviewer.toml`

- [ ] **Step 1: 记录后端目标文件基线**

Run:

```bash
git -C /Users/xuzhihao/code/Porsche status --short
git -C /Users/xuzhihao/code/Porsche diff -- AGENTS.md .codex/agents docs/agents
```

Expected: 显示当前已有修改；不得把这些修改误认为本任务新增内容。

- [ ] **Step 2: 创建后端编排规范**

Create `docs/agents/orchestration.md` with these sections and rules:

```markdown
# Porsche Agent 编排规范

## 单一 Controller

- 后端执行链唯一 Controller 是 `project_manager`；外层会话只提供用户目标和跨仓库协调，不在其内部再启动第二套 Controller。
- `project_manager` 保持只读，只汇总独立角色证据，不代替实现、规格、安全或测试角色。

## SDD 角色映射与顺序

完整流程严格按以下顺序执行：

1. `architect_explorer`：只读输出影响面、约束、风险和未验证假设。
2. `backend_worker`：在授权 worktree 实现、测试、自审并返回明确状态。
3. `spec_compliance_reviewer`：针对最终实现逐项核对完整任务；未通过时退回原 Worker，修复后重新规格审查。
4. `security_reviewer`：规格通过后审查最终 diff；任何修复都会使旧安全结论失效。
5. `test_engineer`：对通过审查的最终版本执行独立验证，区分 PASS、FAIL、SKIPPED。
6. `project_manager`：仅在前述门禁关闭后汇总结论。

## 任务包与状态

每次委派必须给出完整任务正文与验收标准、仓库/worktree/branch/base revision/current revision、现有修改、允许写入路径、禁止操作、提交授权、必读规范、前置证据、验证命令和证据格式。执行者返回 `DONE`、`DONE_WITH_CONCERNS`、`NEEDS_CONTEXT` 或 `BLOCKED`；不得用未经核验的完成声明推进门禁。

## 风险分级

- 完整流程：认证、会话、权限、用户、MySQL/迁移、Redis、管理操作、支付/余额、SSE、上游协议、部署安全、跨仓库接口或多模块改动。
- 标准流程：单仓库、中等风险、边界明确的行为修改；Explorer 可由 Controller 完成，但保留独立 Implementer、Spec Review、Security/Quality Review 和 Test Verification。
- 精简流程：文案、注释、无行为格式调整和纯文档小改；由授权 writer 修改并运行 `git diff --check` 与适用静态检查。认证、数据库、接口和生产操作不得使用精简流程。

## 故障降级

Role TOML 存在不证明角色可调用。先核对实际工具、角色、模型、权限和工作目录。具名角色不可用但允许通用子 Agent 时，完整注入角色指令和任务包；无法保证只读权限时不得用宽权限 Agent 冒充 Reviewer。子 Agent 不可用时输出 Explorer → Implementer → Spec Review → Security/Test 的人工交接包，未运行、跳过或仅由实现者自证的步骤不得标记通过。
```

- [ ] **Step 3: 创建后端规格审查角色**

Create `.codex/agents/spec_compliance_reviewer.toml`:

```toml
name = "spec_compliance_reviewer"
description = "Read-only reviewer that checks Porsche implementations against the exact authorized task and acceptance criteria."
model = "gpt-5.6-terra"
model_reasoning_effort = "high"
sandbox_mode = "read-only"
developer_instructions = """
Remain read-only and independent from the implementer. Locate the actual repository/worktree and read AGENTS.md, docs/agents/domain.md, docs/agents/orchestration.md, the complete authorized task and acceptance criteria, and task-relevant conventions. Record branch, base revision, final revision and existing modifications. Database, migration, entity, enum, timestamp or user-association work requires docs/conventions/database-standards.md.

Do not trust the implementer's report. Inspect the actual final diff and relevant surrounding code. Compare every requirement and acceptance criterion line by line. Report missing requirements, incorrect interpretations, unauthorized extra behavior and claims not supported by code or evidence, with file and line references. Do not review general style before determining specification compliance, do not fix code, and do not expand scope.

Return exactly one verdict: SPEC_PASS, SPEC_FAIL or SPEC_BLOCKED. SPEC_FAIL must list each gap and the expected correction. SPEC_BLOCKED must identify the missing task text, revision, repository access or evidence. Any implementation change invalidates the verdict and requires a fresh review. A spec pass does not imply security, test, integration, browser, upstream or production acceptance.
"""
```

- [ ] **Step 4: 检查 Task 1 差异**

Run:

```bash
git -C /Users/xuzhihao/code/Porsche diff --check -- docs/agents/orchestration.md .codex/agents/spec_compliance_reviewer.toml
git -C /Users/xuzhihao/code/Porsche diff --no-index /dev/null .codex/agents/spec_compliance_reviewer.toml
```

Expected: `git diff --check` exit 0；新角色保持 `read-only`，包含三个明确 verdict，且不包含写入或部署授权。

### Task 2: 将后端现有角色接入编排规范

**Files:**
- Modify: `/Users/xuzhihao/code/Porsche/AGENTS.md`
- Modify: `/Users/xuzhihao/code/Porsche/.codex/agents/project_manager.toml`
- Modify: `/Users/xuzhihao/code/Porsche/.codex/agents/architect_explorer.toml`
- Modify: `/Users/xuzhihao/code/Porsche/.codex/agents/backend_worker.toml`
- Modify: `/Users/xuzhihao/code/Porsche/.codex/agents/security_reviewer.toml`
- Modify: `/Users/xuzhihao/code/Porsche/.codex/agents/test_engineer.toml`

- [ ] **Step 1: 在后端入口声明强制编排规范**

Add under `AGENTS.md` 工作规则:

```markdown
- 使用子 Agent 实施或审查时，必须读取并遵守 [Agent 编排规范](docs/agents/orchestration.md)：按风险选择流程，保持单一 Controller，并依次完成规格、最终安全/质量和测试门禁。
```

In the existing 前后端协作 section, include `spec_compliance_reviewer` among roles that cannot bypass coordinators.

- [ ] **Step 2: 更新后端 Controller**

In `.codex/agents/project_manager.toml`:

- Add `docs/agents/orchestration.md` to the mandatory read list.
- Replace any sequence that omits spec review with:

```text
Select the risk tier from docs/agents/orchestration.md. For full flow, sequence architect_explorer -> backend_worker -> spec_compliance_reviewer -> final security_reviewer -> test_engineer. Do not start security quality review until SPEC_PASS. Return findings through this coordinator to the same worker; every new diff requires fresh spec and downstream review. For standard or streamlined flow, record the qualifying facts and retain every gate required by the selected tier.
```

- Preserve the existing cross-repository coordinator paragraphs and all production authorization limits.

- [ ] **Step 3: 更新后端执行和审查角色**

Add `docs/agents/orchestration.md` to the mandatory read list in all four files. Add these role-specific rules without replacing current business constraints:

```text
architect_explorer: Return the context package to project_manager; do not dispatch implementation or approve later stages.
backend_worker: Accept orchestrated implementation from project_manager, report the four SDD statuses, and do not approve specification, security or final acceptance.
security_reviewer: Start final security/quality review only after traceable SPEC_PASS; any subsequent diff invalidates this review.
test_engineer: Verify the final reviewed revision; any test-driven repair returns through project_manager and invalidates earlier spec/security conclusions.
```

- [ ] **Step 4: 验证后端流程文本**

Run:

```bash
rg -n "orchestration.md|spec_compliance_reviewer|SPEC_PASS|DONE_WITH_CONCERNS|single|唯一 Controller|精简流程" /Users/xuzhihao/code/Porsche/AGENTS.md /Users/xuzhihao/code/Porsche/.codex/agents /Users/xuzhihao/code/Porsche/docs/agents/orchestration.md
git -C /Users/xuzhihao/code/Porsche diff --check -- AGENTS.md .codex/agents docs/agents/orchestration.md
```

Expected: 每个现有角色引用编排规范；`project_manager` 的顺序包含独立 Spec Reviewer；没有删除现有数据库、认证、测试隔离或生产权限条款。

### Task 3: 建立前端 SDD 编排规范和独立规格 Reviewer

**Files:**
- Create: `/Users/xuzhihao/code/Porsche-Web/docs/agents/orchestration.md`
- Create: `/Users/xuzhihao/code/Porsche-Web/.agents/front_end_spec_compliance_reviewer.toml`
- Create: `/Users/xuzhihao/code/Porsche-Web/.codex/agents/front_end_spec_compliance_reviewer.toml`

- [ ] **Step 1: 记录前端目标文件基线和副本状态**

Run:

```bash
git -C /Users/xuzhihao/code/Porsche-Web status --short
git -C /Users/xuzhihao/code/Porsche-Web diff -- AGENTS.md .agents .codex/agents docs/agents
cmp /Users/xuzhihao/code/Porsche-Web/.agents/front_end_project_coordinator.toml /Users/xuzhihao/code/Porsche-Web/.codex/agents/front_end_project_coordinator.toml
cmp /Users/xuzhihao/code/Porsche-Web/.agents/front_end_developer.toml /Users/xuzhihao/code/Porsche-Web/.codex/agents/front_end_developer.toml
cmp /Users/xuzhihao/code/Porsche-Web/.agents/front_end_quality_gate.toml /Users/xuzhihao/code/Porsche-Web/.codex/agents/front_end_quality_gate.toml
```

Expected: 三组现有副本逐字节一致；保留所有未跟踪和未提交文件。

- [ ] **Step 2: 创建前端编排规范**

Create `docs/agents/orchestration.md` with this complete content:

```markdown
# Porsche-Web Agent 编排规范

## 单一 Controller

- 前端执行链唯一 Controller 是 `front_end_project_coordinator`；外层会话只提供用户目标和跨仓库协调，不在其内部再启动第二套 Controller。
- `front_end_project_coordinator` 保持只读，只汇总独立角色证据，不代替实现、规格或质量角色。

## SDD 角色映射与顺序

完整流程严格按以下顺序执行：

1. `front_end_project_coordinator`：执行只读 Explorer，输出需求、影响面、契约差异、可行性和交接包。
2. `front_end_developer`：在授权 worktree 实现、自测、自审并返回明确状态。
3. `front_end_spec_compliance_reviewer`：针对最终实现和 `interface-contract.json` 逐项核对规格；失败经协调者退回 Developer，修复后重新审查。
4. `front_end_quality_gate`：仅在规格通过后审查最终 diff 和验证证据；任何修复都会使旧结论失效。
5. `front_end_project_coordinator`：仅汇总独立证据，不代替 Reviewer 或 Quality Gate。

## 任务包与状态

每次委派必须给出完整任务正文与验收标准、仓库/worktree/branch/base revision/current revision、现有修改、允许写入路径、禁止操作、提交授权、必读规范、前置证据、验证命令和证据格式。执行者返回 `DONE`、`DONE_WITH_CONCERNS`、`NEEDS_CONTEXT` 或 `BLOCKED`；不得用未经核验的完成声明推进门禁。

## 风险分级

- 完整流程：认证、RBAC、路由、状态管理、API/JSON/SSE、接口联调、依赖或构建配置、安全、浏览器/E2E、性能、跨仓库或多模块改动。
- 标准流程：单仓库、中等风险、边界明确的行为修改；Explorer 可由 Controller 完成，但保留独立 Implementer、Spec Review 和 Quality/Test Gate。
- 精简流程：文案、注释、无行为格式调整和纯文档小改；由授权 writer 修改并运行 `git diff --check` 与适用静态检查。认证、权限、接口、依赖、构建和生产操作不得使用精简流程。

## 故障降级

Role TOML 存在不证明角色可调用。先核对实际工具、角色、模型、权限和工作目录。具名角色不可用但允许通用子 Agent 时，完整注入角色指令和任务包；无法保证只读权限时不得用宽权限 Agent 冒充 Reviewer。子 Agent 不可用时输出 Explorer → Implementer → Spec Review → Quality/Test 的人工交接包，未运行、跳过或仅由实现者自证的步骤不得标记通过。
```

- [ ] **Step 3: 创建前端规格审查维护源**

Create `.agents/front_end_spec_compliance_reviewer.toml`:

```toml
name = "front_end_spec_compliance_reviewer"
description = "Read-only reviewer that checks Porsche-Web implementations against the exact authorized task and versioned interface contract."
model = "gpt-5.6-luna"
model_reasoning_effort = "high"
sandbox_mode = "read-only"
developer_instructions = """
Remain read-only and independent from front_end_developer. Accept review scope through front_end_project_coordinator, subject to higher-priority direct user instructions; do not coordinate directly with backend execution roles.

Locate the actual repository/worktree and read AGENTS.md, docs/agents/domain.md, docs/agents/orchestration.md, interface-contract.json, the complete authorized task and acceptance criteria, and task-relevant frontend/API/database conventions. Record branch, base revision, final revision, contract revision/status and existing modifications.

Do not trust the implementer's report. Inspect the actual final diff and relevant surrounding code. Compare every requirement and acceptance criterion line by line. Report missing requirements, incorrect interpretations, unauthorized extra behavior, draft-contract assumptions and unsupported claims with file and line references. Do not review general style before determining specification compliance, do not fix code, do not change the contract and do not expand scope.

Return exactly one verdict: SPEC_PASS, SPEC_FAIL or SPEC_BLOCKED. SPEC_FAIL must list each gap and expected correction. SPEC_BLOCKED must identify missing task text, revision, repository access, contract status or evidence. Any implementation or contract change invalidates the verdict and requires fresh review. A spec pass does not imply quality, security, browser, backend, integration, performance or production acceptance.
"""
```

- [ ] **Step 4: 创建发现副本并核对一致性**

Use `apply_patch` to create `.codex/agents/front_end_spec_compliance_reviewer.toml` with the exact TOML block from Step 3, then run:

```bash
cmp /Users/xuzhihao/code/Porsche-Web/.agents/front_end_spec_compliance_reviewer.toml /Users/xuzhihao/code/Porsche-Web/.codex/agents/front_end_spec_compliance_reviewer.toml
```

Expected: exit 0 and no output.

### Task 4: 将前端现有角色接入编排规范

**Files:**
- Modify: `/Users/xuzhihao/code/Porsche-Web/AGENTS.md`
- Modify: `/Users/xuzhihao/code/Porsche-Web/docs/agents/README.md`
- Modify: `/Users/xuzhihao/code/Porsche-Web/.agents/front_end_project_coordinator.toml`
- Modify: `/Users/xuzhihao/code/Porsche-Web/.agents/front_end_developer.toml`
- Modify: `/Users/xuzhihao/code/Porsche-Web/.agents/front_end_quality_gate.toml`
- Modify: `/Users/xuzhihao/code/Porsche-Web/.codex/agents/front_end_project_coordinator.toml`
- Modify: `/Users/xuzhihao/code/Porsche-Web/.codex/agents/front_end_developer.toml`
- Modify: `/Users/xuzhihao/code/Porsche-Web/.codex/agents/front_end_quality_gate.toml`

- [ ] **Step 1: 更新前端仓库入口**

Change the role list in `AGENTS.md` to:

```markdown
- 协调层：front_end_project_coordinator，与后端 project_manager 唯一对口；Explorer 为协调者的只读探索阶段。
- 执行层：front_end_developer，接受协调者委派（用户直接指令优先）。
- 规格层：front_end_spec_compliance_reviewer，独立核对完整任务和版本化接口契约，不修改实现。
- 质量层：front_end_quality_gate，仅在规格通过后独立进行安全、质量和验收核验。
- 配置维护源：.agents 下四个角色 TOML；Codex 发现副本位于 .codex/agents，同名文件须逐字节一致。
- 编排规则：执行或审查任务必须读取 docs/agents/orchestration.md，按风险选择流程并保持单一 Controller。
```

Preserve the existing coordinator-only cross-team rule and acceptance limitations.

- [ ] **Step 2: 更新前端启动说明**

In `docs/agents/README.md`:

- Change “三个角色” to “四个角色”.
- Add `front_end_spec_compliance_reviewer` as high/read-only.
- Change the work loop to `Coordinator Explorer → contract/plan alignment → Developer → Spec Reviewer → final Quality Gate → repair and re-review → joint sign-off`.
- Add an SDD mapping/risk-tier section linking `orchestration.md`.
- State that role availability must be checked before dispatch and that a generic reviewer cannot impersonate read-only when its actual sandbox is wider.

- [ ] **Step 3: 更新前端角色维护源**

Add `docs/agents/orchestration.md` to every role's mandatory read list. Add:

```text
front_end_project_coordinator: Select the documented risk tier. For full flow, sequence Explorer -> front_end_developer -> front_end_spec_compliance_reviewer -> final front_end_quality_gate. Do not start Quality Gate until SPEC_PASS; route every fix through this coordinator and invalidate stale downstream reviews.
front_end_developer: Report DONE, DONE_WITH_CONCERNS, NEEDS_CONTEXT or BLOCKED; do not approve specification, quality or joint acceptance.
front_end_quality_gate: Require traceable SPEC_PASS for the same final revision and contract version; any later implementation or contract change invalidates the review.
```

Do not remove existing Vue/JavaScript, contract, security, evidence or deployment limits.

- [ ] **Step 4: 同步三个现有发现副本**

For each changed `.agents` role, use `apply_patch` to make the corresponding `.codex/agents` file byte-identical. Run:

```bash
cmp /Users/xuzhihao/code/Porsche-Web/.agents/front_end_project_coordinator.toml /Users/xuzhihao/code/Porsche-Web/.codex/agents/front_end_project_coordinator.toml
cmp /Users/xuzhihao/code/Porsche-Web/.agents/front_end_developer.toml /Users/xuzhihao/code/Porsche-Web/.codex/agents/front_end_developer.toml
cmp /Users/xuzhihao/code/Porsche-Web/.agents/front_end_spec_compliance_reviewer.toml /Users/xuzhihao/code/Porsche-Web/.codex/agents/front_end_spec_compliance_reviewer.toml
cmp /Users/xuzhihao/code/Porsche-Web/.agents/front_end_quality_gate.toml /Users/xuzhihao/code/Porsche-Web/.codex/agents/front_end_quality_gate.toml
```

Expected: all four commands exit 0 with no output.

Expected: all commands exit 0 with no output.

### Task 5: 静态验证、负向探针和最终范围审查

**Files:**
- Verify all files from Tasks 1-4
- Create temporarily: `/tmp/verify-porsche-sdd-agents.go`

- [ ] **Step 1: 创建临时 TOML 验证器**

Create `/tmp/verify-porsche-sdd-agents.go` with `apply_patch` using this code:

```go
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type Agent struct {
	Name                  string `toml:"name"`
	Description           string `toml:"description"`
	Model                 string `toml:"model"`
	ModelReasoningEffort  string `toml:"model_reasoning_effort"`
	SandboxMode           string `toml:"sandbox_mode"`
	DeveloperInstructions string `toml:"developer_instructions"`
}

func parse(file string, body []byte) (Agent, error) {
	var agent Agent
	if err := toml.Unmarshal(body, &agent); err != nil { return Agent{}, fmt.Errorf("%s: %w", file, err) }
	if agent.Name == "" || agent.Model == "" || agent.SandboxMode == "" || agent.DeveloperInstructions == "" {
		return Agent{}, fmt.Errorf("%s: missing required field", file)
	}
	if strings.Contains(agent.Name, "spec_compliance_reviewer") && agent.SandboxMode != "read-only" {
		return Agent{}, fmt.Errorf("%s: spec reviewer must be read-only", file)
	}
	return agent, nil
}

func main() {
	patterns := []string{
		"/Users/xuzhihao/code/Porsche/.codex/agents/*.toml",
		"/Users/xuzhihao/code/Porsche-Web/.agents/*.toml",
		"/Users/xuzhihao/code/Porsche-Web/.codex/agents/*.toml",
	}
	count := 0
	for _, pattern := range patterns {
		files, err := filepath.Glob(pattern)
		if err != nil { panic(err) }
		for _, file := range files {
			body, err := os.ReadFile(file)
			if err != nil { panic(err) }
			agent, err := parse(file, body)
			if err != nil { panic(err) }
			if strings.Contains(agent.Name, "spec_compliance_reviewer") {
				probe := bytes.Replace(body, []byte(`sandbox_mode = "read-only"`), []byte(`sandbox_mode = "workspace-write"`), 1)
				if _, err := parse(file+" permission probe", probe); err == nil || !strings.Contains(err.Error(), "spec reviewer must be read-only") {
					panic(fmt.Errorf("%s: permission drift probe was not rejected", file))
				}
			}
			count++
		}
	}
	frontRoles := []string{"front_end_project_coordinator", "front_end_developer", "front_end_spec_compliance_reviewer", "front_end_quality_gate"}
	for _, role := range frontRoles {
		left, err := os.ReadFile("/Users/xuzhihao/code/Porsche-Web/.agents/" + role + ".toml")
		if err != nil { panic(err) }
		right, err := os.ReadFile("/Users/xuzhihao/code/Porsche-Web/.codex/agents/" + role + ".toml")
		if err != nil { panic(err) }
		if !bytes.Equal(left, right) { panic(fmt.Errorf("%s: frontend agent copies drifted", role)) }
	}
	fmt.Printf("PASS: parsed %d agent files; reviewer permissions and 4 frontend copies verified\n", count)
}
```

- [ ] **Step 2: 运行正向解析和副本检查**

Run from `/Users/xuzhihao/code/Porsche`:

```bash
go run /tmp/verify-porsche-sdd-agents.go
cmp /Users/xuzhihao/code/Porsche-Web/.agents/front_end_project_coordinator.toml /Users/xuzhihao/code/Porsche-Web/.codex/agents/front_end_project_coordinator.toml
cmp /Users/xuzhihao/code/Porsche-Web/.agents/front_end_developer.toml /Users/xuzhihao/code/Porsche-Web/.codex/agents/front_end_developer.toml
cmp /Users/xuzhihao/code/Porsche-Web/.agents/front_end_spec_compliance_reviewer.toml /Users/xuzhihao/code/Porsche-Web/.codex/agents/front_end_spec_compliance_reviewer.toml
cmp /Users/xuzhihao/code/Porsche-Web/.agents/front_end_quality_gate.toml /Users/xuzhihao/code/Porsche-Web/.codex/agents/front_end_quality_gate.toml
```

Expected: validator prints `PASS`; every `cmp` exits 0.

- [ ] **Step 3: 运行内存权限探针**

The validator mutates each spec reviewer's sandbox value in memory and confirms the probe becomes `workspace-write`; repository files remain unchanged. Re-run:

```bash
go run /tmp/verify-porsche-sdd-agents.go
```

Expected: prints `PASS: parsed ...; reviewer permissions and 4 frontend copies verified`. The normal validation rejects any repository spec reviewer whose actual sandbox is not `read-only`; the in-memory mutation proves the checked field is parseable and effective without writing a probe file.

- [ ] **Step 4: 检查文档引用和空白错误**

Run:

```bash
test -f /Users/xuzhihao/code/Porsche/docs/agents/orchestration.md
test -f /Users/xuzhihao/code/Porsche-Web/docs/agents/orchestration.md
rg -n "orchestration.md|spec_compliance_reviewer|front_end_spec_compliance_reviewer|SPEC_PASS" /Users/xuzhihao/code/Porsche/AGENTS.md /Users/xuzhihao/code/Porsche/.codex/agents /Users/xuzhihao/code/Porsche/docs/agents /Users/xuzhihao/code/Porsche-Web/AGENTS.md /Users/xuzhihao/code/Porsche-Web/.agents /Users/xuzhihao/code/Porsche-Web/.codex/agents /Users/xuzhihao/code/Porsche-Web/docs/agents
git -C /Users/xuzhihao/code/Porsche diff --check
git -C /Users/xuzhihao/code/Porsche-Web diff --check
```

Expected: both files exist; role/mapping references appear in the intended entrypoints; both diff checks exit 0.

- [ ] **Step 5: 审查最终范围和保留的既有改动**

Run:

```bash
git -C /Users/xuzhihao/code/Porsche status --short
git -C /Users/xuzhihao/code/Porsche diff -- AGENTS.md .codex/agents docs/agents docs/superpowers/specs/2026-09-09-porsche-sdd-agent-integration-design.md docs/superpowers/plans/2026-09-09-porsche-sdd-agent-integration.md
git -C /Users/xuzhihao/code/Porsche-Web status --short
git -C /Users/xuzhihao/code/Porsche-Web diff -- AGENTS.md .agents .codex/agents docs/agents
```

Expected: only计划列出的 Agent/文档路径包含本任务修改；已有 `progress.md`、业务代码、依赖和其他未跟踪文件保持原状。最终报告分别列出本任务新增内容、进入前已存在的修改、验证结果、跳过项和尚未证明的运行时能力。

- [ ] **Step 6: 条件式提交**

Only if the user separately authorizes commits, stage exact reviewed files per repository and create separate documentation/configuration commits. Without that authorization, do not run `git add`, `git commit` or `git push`.

- [x] **Step 7: Task 5 最终审查快照补充门禁**

Task 5 的最终静态验证还必须包含下列证据；这是后续审查加固，不改写前述步骤是否已经执行：

```bash
python3 -m unittest docs/agents/test_review_snapshot.py -v
python3 -m unittest discover -s /Users/xuzhihao/code/Porsche-Web/docs/agents -p 'test_review_snapshot.py' -v
cmp /Users/xuzhihao/code/Porsche/docs/agents/review_snapshot.py /Users/xuzhihao/code/Porsche-Web/docs/agents/review_snapshot.py
cmp /Users/xuzhihao/code/Porsche/docs/agents/test_review_snapshot.py /Users/xuzhihao/code/Porsche-Web/docs/agents/test_review_snapshot.py
shasum -a 256 /Users/xuzhihao/code/Porsche/docs/agents/review_snapshot.py /Users/xuzhihao/code/Porsche-Web/docs/agents/review_snapshot.py /Users/xuzhihao/code/Porsche/docs/agents/test_review_snapshot.py /Users/xuzhihao/code/Porsche-Web/docs/agents/test_review_snapshot.py
```

Expected: 两仓各 `Ran 52 tests` 且 `OK`；两个 `cmp` 均 exit 0；helper 两份哈希均为 `b9d67add44e9cb36c5e0138f62cb046ed2f4654b5863f4e5c9d0005436b58c2b`，test 两份哈希均为 `9e4da48e2a17c7b311c2c0c934e8e8e041286e28ffe2c799b84c319535645d9b`。少于 52 项、任一失败或副本漂移都不能完成 Task 5。

### Task 6: 后端接入内容寻址 review snapshot

**Files:**
- Create: `/Users/xuzhihao/code/Porsche/docs/agents/review_snapshot.py`
- Create: `/Users/xuzhihao/code/Porsche/docs/agents/test_review_snapshot.py`
- Modify: `/Users/xuzhihao/code/Porsche/docs/agents/orchestration.md`
- Modify: `/Users/xuzhihao/code/Porsche/.codex/agents/project_manager.toml`
- Modify: `/Users/xuzhihao/code/Porsche/.codex/agents/spec_compliance_reviewer.toml`
- Modify: `/Users/xuzhihao/code/Porsche/.codex/agents/security_reviewer.toml`
- Modify: `/Users/xuzhihao/code/Porsche/.codex/agents/test_engineer.toml`
- Modify: `/Users/xuzhihao/code/Porsche/docs/superpowers/specs/2026-09-09-porsche-sdd-agent-integration-design.md`
- Modify: `/Users/xuzhihao/code/Porsche/docs/superpowers/plans/2026-09-09-porsche-sdd-agent-integration.md`
- Modify: `/Users/xuzhihao/code/Porsche-Web/docs/agents/review_snapshot.py`
- Modify: `/Users/xuzhihao/code/Porsche-Web/docs/agents/test_review_snapshot.py`
- Modify: `/Users/xuzhihao/code/Porsche-Web/docs/agents/orchestration.md`
- Modify: `/Users/xuzhihao/code/Porsche-Web/.agents/front_end_project_coordinator.toml`
- Modify: `/Users/xuzhihao/code/Porsche-Web/.agents/front_end_spec_compliance_reviewer.toml`
- Modify: `/Users/xuzhihao/code/Porsche-Web/.agents/front_end_quality_gate.toml`
- Modify: `/Users/xuzhihao/code/Porsche-Web/.agents/front_end_developer.toml`
- Modify: `/Users/xuzhihao/code/Porsche-Web/.codex/agents/front_end_project_coordinator.toml`
- Modify: `/Users/xuzhihao/code/Porsche-Web/.codex/agents/front_end_spec_compliance_reviewer.toml`
- Modify: `/Users/xuzhihao/code/Porsche-Web/.codex/agents/front_end_quality_gate.toml`
- Modify: `/Users/xuzhihao/code/Porsche-Web/.codex/agents/front_end_developer.toml`

- [x] **Step 1: 保存 dirty 基线与源算法证据**

记录后端 `git status --short`、目标文件 diff 与 SHA-256，不覆盖前序任务修改。完整阅读两仓 Agent 规范、所有后端 role TOML、前端 helper/tests/orchestration 及 Coordinator/Spec/Quality 三类 role TOML。先在 Porsche-Web 运行 25 项测试并记录 helper/test 哈希。

- [x] **Step 2: 以已验证算法复用完成 TDD 证据链**

初始 25 项算法复用引用前端已经完成的 RED→GREEN 证据，不人为删除 helper 重演 RED。用 `apply_patch` 在后端创建逐字节相同的 helper 和 tests；先 `cmp`/SHA-256 确认复制无漂移，再在 Porsche 后端运行同一 25 项套件作为 GREEN。后续 index-binding 规格缺陷另按 Step 6 新增失败测试并完成独立 RED→GREEN。不得用 `cp` 作为编辑手段。

- [x] **Step 3: 接入后端角色与门禁**

后端唯一算法命令为本地 `python3 docs/agents/review_snapshot.py`。Backend 的 baseline/snapshot/verify 不传 `--contract`，manifest `contract=null`；跨仓库接口任务仍在 PM 任务包中单独绑定 Porsche-Web contract 内容哈希/version/status，且必须由可访问该契约的角色核对。

PM 只读运行 baseline/snapshot 的 `--output -`，授权 writer 原样持久化；任务包记录 scope/baseline/snapshot、内容哈希、helper 命令、退出码、stdout ID 和 writer。Spec/Security/Test 各自对同一 writer-persisted snapshot 运行无写 verify。Security 只接受绑定同 snapshot ID/final revision 的 `SPEC_PASS`，Test 只接受同 snapshot 的 Spec 与 Security 通过证据。任一组成变化使全链失效；Test 的 workspace-write 不允许在修改被审内容后沿用旧 snapshot。Scope 外变化由 PM 核验并重建证据；helper 不可用时无论自动或人工 fallback 均为 `BLOCKED`。

- [x] **Step 4: 威胁模型与测试覆盖**

52 项测试必须覆盖：scope 路径/前缀校验、重复与重叠拒绝、tracked/staged/unstaged/untracked/deleted、worktree/cached rename/copy 两端、baseline 后 scope 内/外提交、scope 外增删改阻塞、`assume-unchanged`/`skip-worktree` 的 scope 内 ID 变化与 scope 外阻塞、scope 内 staged-only index 内容改变 ID、scope 外 staged-only index 漂移阻塞、v1 baseline evidence 拒绝、scope/baseline/snapshot/contract 的非有限数、任意层重复键和未配对 Unicode surrogate 拒绝且无 traceback、canonical UTF-8 编码纵深防护、baseline/snapshot 输入的 worktree 外约束及 relative/symlink 绕过、所有 path output（new/existing/relative/symlink/hardlink/并发）统一 exit 2 且无写、canonical stdout `--output -` 无写、`.gitignore`/`.git/info/exclude`/global excludes 三类 scope 内 ignored 内容绑定、ignore 规则切换不可隐藏 scope 内文件、scope 外持续 ignored 内容隔离及 visible→ignored 防隐藏阻塞、未合并 index stage 与 gitlink 阻塞、exact absent、匹配/漂移 verify、regular 执行位、symlink 目标哈希、中间 symlink confinement、相同内容稳定 ID、无 contract 时 `manifest.contract=null`，以及 contract 模式的无效 JSON/空字段拒绝。后端测试复用同一套件，因此同时保留可选 `--contract` 的通用语义。

- [x] **Step 5: 最终验证与保护路径检查**

Run:

```bash
python3 -m unittest docs/agents/test_review_snapshot.py -v
python3 -m unittest discover -s /Users/xuzhihao/code/Porsche-Web/docs/agents -p 'test_review_snapshot.py' -v
cmp docs/agents/review_snapshot.py /Users/xuzhihao/code/Porsche-Web/docs/agents/review_snapshot.py
cmp docs/agents/test_review_snapshot.py /Users/xuzhihao/code/Porsche-Web/docs/agents/test_review_snapshot.py
go run /tmp/verify-porsche-sdd-agents.go
git diff --check
git status --short
```

Expected: 两仓各 52/52；helper/test byte equality，helper SHA-256 为 `b9d67add44e9cb36c5e0138f62cb046ed2f4654b5863f4e5c9d0005436b58c2b`，test SHA-256 为 `9e4da48e2a17c7b311c2c0c934e8e8e041286e28ffe2c799b84c319535645d9b`；全部 TOML 解析且前端四组维护源/发现副本一致；diff check exit 0。最终 diff 只包含本 Task 允许的 Agent/文档路径；业务代码、依赖、`progress.md`、`feature_list.json` 及其他既有 dirty/untracked 内容的状态与基线一致。不得提交、推送、部署、清理或修改这些保护路径。

- [x] **Step 6: 修复 index 未绑定的规格缺陷**

独立规格审查发现 staged 新内容被恢复后的工作树内容遮蔽，旧 v1 snapshot ID 可退回 clean 值。先新增并运行三项 RED：scope 内 staged-only ID 必须变化、scope 外 staged-only 必须阻塞、v1 baseline 必须拒绝；旧实现分别得到 ID 相同、exit 0、exit 0。随后升级 v2，同时 union worktree/cached name-status 的 rename/copy 两端，并通过 `git ls-files --stage -z` 把 index mode/blob OID/stage/absent 纳入 scope 内 manifest 与 scope 外 baseline 指纹。三项定向 GREEN 及两仓完整 28 项均通过；既有 `assume-unchanged`/`skip-worktree` 四个用例继续通过。

- [x] **Step 7: 修复严格 JSON 与证据隔离质量缺陷**

Quality Review 要求所有 JSON 输入统一拒绝非有限数与任意层重复键，并禁止 baseline/snapshot 证据进入实际 Git worktree。新增九项测试并确认旧实现产生 16 个 RED 断言：scope/contract/baseline/snapshot strict JSON、baseline/snapshot worktree 内输出、snapshot 的 worktree 内 baseline 输入、verify 的直接/relative/symlink evidence 绕过、非零 index stage 与 gitlink。实现统一 strict loader、absolute realpath/commonpath 外置证据门禁、通用 `review-*-v2` schema、stage/gitlink 阻塞；同步后端/前端 orchestration 和相关 Controller/Reviewer/Writer 角色。九项定向 GREEN 和两仓完整 37 项通过。

- [x] **Step 8: 修复未配对 Unicode surrogate 质量缺陷**

Quality Review 发现 Python `json.loads` 会接受未配对 surrogate：contract baseline 可错误 exit 0，baseline/snapshot 则在 canonical UTF-8 编码时以 exit 1 和 traceback 崩溃。先为 scope、contract、baseline、snapshot 各新增一个负向测试；旧实现四项均 RED，分别表现为非统一 loader 错误、exit 0、exit 1、exit 1。随后让 strict loader 递归校验全部 dict key、string value 与 list 元素中的字符串都可严格 UTF-8 编码，并让 canonical encoder 将 `UnicodeEncodeError` 转换为 `SnapshotError`。四项定向 GREEN 与两仓完整 41 项均通过，所有场景均 exit 2 且 stderr 无 traceback。

- [x] **Step 9: 修复 hardlink 覆盖与 ignored-untracked 漏检**

跨仓终审发现 `Path.write_bytes` 会截断 existing regular/symlink/hardlink，且 `git ls-files --others --exclude-standard` 会漏掉 scope prefix 内被三类 ignore 来源隐藏的新文件。先新增九项测试：existing regular/symlink、external hardlink-to-worktree inode、正常新文件 mode/bytes、`.gitignore`、`.git/info/exclude`、global excludes、scope 内 ignore 规则切换、scope 外持续 ignored 隔离，以及 scope 外 visible→ignored 防隐藏；旧实现的修复目标按预期 RED，既有 scope 外完整性回归保持 GREEN。File-output 改为解析外置 parent 后逐组件 dirfd/`O_NOFOLLOW` 固定目录，以 `O_EXCL`、mode `0600` 原子只创建并完整 write/flush/fsync/close，失败只清理本次新建文件；缺少安全平台能力时阻塞 file-output。候选集合保留 visible untracked 的 scope 外 baseline 语义，并额外仅为 scope 内 union 不应用 exclude 的全量 untracked，从而使三类 ignore 与规则切换都不能隐藏待审内容，同时持续 ignored 的 scope 外文件不产生噪声、原本 visible 的 scope 外文件也不能通过 ignore 切换逃避 baseline。九项定向 GREEN 与两仓完整 50 项均通过。

- [x] **Step 10: 修复 basename replacement 清理竞态**

最新规格审查发现仅记录 `created=True` 会在攻击者替换 basename 后产生两种问题：注入 write/fsync 失败时异常清理会 unlink replacement，正常写完时则会错误返回成功。先新增两项定向 RED，旧实现分别表现为 replacement 被删除及未抛出 `SnapshotError`。修复在 `os.open` 成功后立即 `fstat` 保存 created `st_dev/st_ino`；成功返回前使用同一 parent dirfd 的 `stat(follow_symlinks=False)` 核对 basename 仍指向该 inode，否则阻塞；异常清理前执行同样核对，只在仍为本次 created inode 时 unlink，replacement 必须保留。两项定向 GREEN 与两仓完整 52 项均通过。

- [x] **Step 11: 移除 helper 文件输出以消除便携 POSIX 竞态**

最新质量审查确认：在缺少跨平台、不可替换目录项原语的便携 POSIX 环境中，公开 basename 后仍无法由 helper 完全排除 replacement 竞态。采用更小的信任边界：先把正常新外部路径、并发同路径和 worktree 内路径的期望改为统一拒绝，旧实现按预期 RED（正常新外部路径 exit 0、并发至少一次 exit 0、worktree 内错误仍为旧路径门禁）；随后删除自制 atomic/file writer，令 `baseline`/`snapshot --output` 只接受精确 `-`，任何其他值均安全 exit 2、无 traceback 且不写。Controller 继续只读接收 canonical stdout；授权 writer 必须在 resolved worktree 外私有任务目录中 exclusive 创建全新文件，不得覆盖或链接 existing regular/symlink/hardlink，并报告路径与内容哈希。重写仅针对已移除内部 writer 的竞态测试，保留 stdout canonical/no-write 与 verify 外置输入约束；两仓完整 52 项均通过，helper/test 逐字节一致，SHA-256 分别为 `b9d67add44e9cb36c5e0138f62cb046ed2f4654b5863f4e5c9d0005436b58c2b` 与 `9e4da48e2a17c7b311c2c0c934e8e8e041286e28ffe2c799b84c319535645d9b`，角色校验及 diff-check 作为最终 GREEN。
