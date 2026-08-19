# Go Gateway Agent Configurations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create five project-scoped Codex Agent TOML configurations tailored to the Porsche Go gateway.

**Architecture:** Each TOML file declares one role, a model, reasoning effort, sandbox scope and concise Go-gateway-specific instructions. Read-only roles return reports in their final response; write-capable roles edit only source or tests needed for their assigned task.

**Tech Stack:** Codex project Agent configuration (TOML), Go 1.22, Gin, GORM, MySQL, Go standard testing.

---

### Task 1: Create project Agent configuration directory

**Files:**

- Create: `.codex/agents/architect_explorer.toml`
- Create: `.codex/agents/backend_worker.toml`
- Create: `.codex/agents/security_reviewer.toml`
- Create: `.codex/agents/test_engineer.toml`
- Create: `.codex/agents/project_manager.toml`

- [ ] **Step 1: Add the Explorer configuration**

Define `architect_explorer` as `gpt-5.6-luna`, `high`, and `read-only`. Require a final structured context package covering Gin route registration, middleware chain, Handler → Service → Models/DB dependencies, model gateway, registry, RAG and `config/*.yaml`; prohibit file edits.

- [ ] **Step 2: Add the Backend Worker configuration**

Define `backend_worker` as `gpt-5.6-terra`, `medium`, and `workspace-write`. Require reuse of existing structs, DTOs, services, helpers and errors before introducing abstractions; require concise comments for business rules, compatibility, transactions and security decisions; use same-package `*_test.go` tests and avoid undeclared dependencies.

- [ ] **Step 3: Add the Security Reviewer configuration**

Define `security_reviewer` as `gpt-5.6-terra`, `xhigh`, and `read-only`. Require a final severity-grouped report covering JWT, admin auth, authorization, GORM query boundaries, secrets, upstream URLs, SSE, PII, uploads and RAG isolation; prohibit creating report files.

- [ ] **Step 4: Add the Test Engineer configuration**

Define `test_engineer` as `gpt-5.6-terra`, `medium`, and `workspace-write`. Require Go `testing`, `httptest`, SQLite or test configuration; prohibit live MySQL, Redis and upstream model dependencies; return the test plan in the final response.

- [ ] **Step 5: Add the Project Manager configuration**

Define `project_manager` as `gpt-5.6-luna`, `high`, and `read-only`. Require it to orchestrate only through available subagent mechanisms, gate on no unresolved high-severity security findings and `go test ./...`, and report tool limitations instead of assuming subagent availability.

### Task 2: Validate and commit

**Files:**

- Test: `.codex/agents/*.toml`

- [ ] **Step 1: Parse every TOML file**

Run a Python `tomllib` parser for every `.codex/agents/*.toml` file. For each file, assert that its `name` matches its filename stem and `sandbox_mode` is either `read-only` or `workspace-write`. Expected output: `all agent TOML files parse successfully`.

- [ ] **Step 2: Check the staged diff**

Run `git diff --check` and inspect the diff for `.codex/agents`. Expected result: no whitespace errors and exactly five new Agent configuration files.

- [ ] **Step 3: Commit the configuration**

Stage `.codex/agents` only and commit with `add Go gateway agent roles`.
