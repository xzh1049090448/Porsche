# Agent Guidance Refresh Implementation Plan

**Goal:** Align five agent descriptions and their shared domain guide with released backend `90abbdc49513039fa9218a6ce72d3147fbf1721e`, without business-code, model or permission changes.

**Architecture:** Shared rules belong in `docs/agents/domain.md`; each TOML supplies role-specific obligations and reading requirements. The six targets match released main; edit in place without resetting the older, dirty local checkout. User approved this scope on 2026-09-02. No automatic commit, push, deployment or agent launch.

**Tech Stack:** Existing TOML/Markdown and Go 1.22 with the existing TOML dependency for temporary validation.

## Checklist

- [x] Read target files, AGENTS.md, database conventions and released authentication, migration, deployment and test code; confirm the six targets have no pre-existing changes.
- [x] Run `GOCACHE=/private/tmp/porsche-go-build-cache bash ./init.sh`; confirm dependency files remain unchanged.
- [x] Update `.codex/agents/architect_explorer.toml`, `backend_worker.toml`, `project_manager.toml`, `security_reviewer.toml` and `test_engineer.toml`: preserve role settings, replace obsolete architecture/testing assumptions, and add authorization/evidence/secret-handling limits.
- [x] Update `docs/agents/domain.md`: current authentication/RBAC, MySQL 8/Redis isolation, one-shot Root, GUID/migrations and production versus acceptance rollback behavior.
- [x] Parse TOML with `github.com/pelletier/go-toml/v2`; compare non-description fields to Git originals. Check paths against released main, documentation links locally, and absence of obsolete paths. Probe malformed TOML and changed sandbox settings to ensure the validator rejects them.
- [x] Run Go tests/build/vet and `git diff --check`; inspect the scoped diff and preserve prior work. Append results to `progress.md` without changing unrelated business-feature statuses.

## Verification limits

Tests in the local checkout establish its baseline only; architecture references must be checked against the released Git tree because local main is older. Static validation does not prove that the app session has reloaded agents or that models follow their instructions. No production data or credentials are needed.

## Publishing authorization

After reviewing the local update, the user explicitly requested pushing it to remote main. Prepare an isolated commit on the latest origin/main containing only the five role files, shared domain guide, this plan and the corresponding maintenance progress entry. Re-run validation against that main-based tree, then push without force. Preserve the older local checkout, its unrelated commits and untracked files. This authorization does not include production deployment.
