# Production Release Preflight Hotfix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the full-stack production release preserve existing `.env` values, add newly documented keys safely, reject incomplete backend or Mock frontend configuration before service replacement, and install frontend dependencies reproducibly.

**Architecture:** The backend owns the environment template, additive merge command, and canonical configuration validator. A small `check-config` binary is built into the same candidate image as the server. The full-stack script resets both repositories to `origin/main`, merges missing keys under its release lock, builds and validates one backend image, builds the frontend through a fail-closed production gate using `npm ci`, then deploys the already validated image by immutable image ID.

**Tech Stack:** Bash with strict mode, Go 1.22, Docker multi-stage build, Node.js 18+, Vite 6, npm lockfile, Go `testing`, Node `node:test`, shell behavioral fixtures.

---

## File map

Backend repository `/Users/xuzhihao/code/Porsche/.worktrees/production-release-preflight`:

- Modify `.env.example`: complete runtime configuration catalog without committing secrets.
- Modify `internal/config/config.go`: reject declared development-only fixed credentials outside development without requiring them when absent.
- Modify `internal/config/config_test.go`: configuration behavior and template parity tests.
- Create `cmd/check-config/main.go`: canonical configuration-only process entry point.
- Create `cmd/check-config/main_test.go`: success and sanitized-failure command tests.
- Modify `Dockerfile`: include `check-config` in the production image.
- Create `deploy/merge-env-example.sh`: atomic additive environment merge.
- Create `deploy/test-merge-env-example.sh`: behavioral merge regression suite.
- Modify `deploy/production-deploy.sh`: validate or consume one immutable candidate image before stopping the current container.
- Modify `deploy/test-production-deploy.sh`: candidate preflight and immutable prebuilt-image tests.
- Modify `deploy/restart-all.sh`: reset both repositories, merge environment, build/preflight backend, use `npm ci`, then deploy the checked image.
- Modify `deploy/test-restart-all.sh`: release ordering, lockfile, merge, preflight, and no-overwrite tests.
- Modify `README.md`: production configuration, migration, preflight, and release behavior.

Frontend repository `/Users/xuzhihao/code/Porsche-Web/.worktrees/production-release-preflight`:

- Create `scripts/check-production-env.mjs`: Vite-compatible production Mock gate.
- Create `scripts/check-production-env.test.js`: exact false/missing/truthy environment tests.
- Modify `package.json`: run the gate before `vite build`.
- Modify `README.md`: production build requirements and lockfile workflow.

### Task 1: Correct backend production configuration semantics and template

**Files:**
- Modify: `internal/config/config_test.go`
- Modify: `internal/config/config.go`
- Modify: `.env.example`

- [ ] **Step 1: Write failing production fixed-credential tests**

Add tests that call `setSafeProductionAuthEnvironment`, unset `FIXED_LOGIN_PHONE` and `FIXED_LOGIN_PASSWORD`, and expect `Load()` to succeed. Add table cases that explicitly declare either key, including an empty value, and expect the exact sanitized error `FIXED_LOGIN credentials are not allowed outside development`.

```go
func TestLoadAllowsOmittedFixedLoginCredentialsOutsideDevelopment(t *testing.T) {
    setSafeProductionAuthEnvironment(t)
    unsetEnvironment(t, "FIXED_LOGIN_PHONE")
    unsetEnvironment(t, "FIXED_LOGIN_PASSWORD")
    if _, err := Load(); err != nil {
        t.Fatalf("Load() error = %v", err)
    }
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
GOCACHE=/private/tmp/porsche-release-preflight-cache go test ./internal/config -run 'TestLoad(AllowsOmitted|RejectsDeclared)FixedLogin' -count=1
```

Expected: the omitted case fails on the current default credential check and declared empty variables are accepted.

- [ ] **Step 3: Implement the minimal fixed-credential rule**

Add a helper mirroring `rejectRootBootstrapEnvironment` that scans `os.Environ()` for the exact keys `FIXED_LOGIN_PHONE` and `FIXED_LOGIN_PASSWORD` outside development. Call it during `Load()` and remove the comparison that requires replacement values while `FIXED_LOGIN_ENABLED=false`.

- [ ] **Step 4: Add a failing `.env.example` parity test**

Parse the exact environment names used by `config.go` from a maintained expected list, parse active and exact commented empty assignments from `.env.example`, and assert that every runtime key is documented exactly once. Assert `ACTION_SECURITY_HMAC_KEY` is an exact commented empty assignment and no valid 43-character secret appears in the template.

- [ ] **Step 5: Run the parity test and verify RED**

Run:

```bash
GOCACHE=/private/tmp/porsche-release-preflight-cache go test ./internal/config -run TestEnvironmentExampleDocumentsRuntimeSettings -count=1
```

Expected: failure listing the currently omitted runtime settings, led by `ACTION_SECURITY_HMAC_KEY`.

- [ ] **Step 6: Complete `.env.example` and verify GREEN**

Add the missing optional settings with current code defaults. Add `TRUST_PROXY_HEADERS=false` as an assignment. Add the action-security key as:

```dotenv
# staging/production 必填；填入32字节随机值的43字符无填充Base64URL编码。
# 不得与 JWT_SECRET_KEY、AUTH_HMAC_KEY 或 JIEKOU_API_KEY 复用。
# ACTION_SECURITY_HMAC_KEY=
```

Run all `internal/config` tests and `git diff --check`.

- [ ] **Step 7: Commit Task 1**

```bash
git add .env.example internal/config/config.go internal/config/config_test.go
git commit -m "fix: align production environment contract"
```

### Task 2: Add the atomic additive environment merge

**Files:**
- Create: `deploy/test-merge-env-example.sh`
- Create: `deploy/merge-env-example.sh`

- [ ] **Step 1: Write the failing shell regression suite**

Create isolated fixture files and require these behaviors:

```text
existing key/value bytes remain unchanged
existing empty assignment remains unchanged
missing active example assignment is appended once
missing exact commented empty assignment is appended as KEY=
values never appear in stdout or stderr
owner and mode are preserved
duplicate or malformed example keys fail without changing .env
symlink inputs are rejected
second execution is byte-for-byte idempotent
```

The test must compare the original prefix with `cmp`, hash the destination before every rejected case, and inspect only key names in output.

- [ ] **Step 2: Run the suite and verify RED**

Run:

```bash
bash deploy/test-merge-env-example.sh
```

Expected: failure because `deploy/merge-env-example.sh` does not exist.

- [ ] **Step 3: Implement the minimal merge command**

Use `set -Eeuo pipefail`; require exactly two absolute regular non-symlink paths. Parse only `KEY=value`, `export KEY=value`, and exact `# KEY=` template placeholders where `KEY` matches `[A-Z][A-Z0-9_]*`. Reject duplicate example keys and malformed candidate assignment lines. Copy `.env` to a same-directory `mktemp`, append a newline only when necessary, append absent assignments in example order, apply the original numeric mode and owner, then `mv` the completed file over `.env`. Trap cleanup of the temporary file. Print `added environment key: KEY` only.

- [ ] **Step 4: Run RED cases to GREEN**

Run the merge suite twice and run `bash -n` on both scripts. Expected: PASS with no values printed.

- [ ] **Step 5: Commit Task 2**

```bash
git add deploy/merge-env-example.sh deploy/test-merge-env-example.sh
git commit -m "feat: merge new environment keys additively"
```

### Task 3: Build a canonical configuration-check binary

**Files:**
- Create: `cmd/check-config/main_test.go`
- Create: `cmd/check-config/main.go`
- Modify: `Dockerfile`
- Modify: `deploy/test-dockerfile.sh`

- [ ] **Step 1: Write failing command tests**

Define `run(stdout, stderr io.Writer) int`. With a safe production fixture expect exit `0` and only `configuration valid`. With a missing action key expect exit `1`, the sanitized `ACTION_SECURITY_HMAC_KEY: missing`, and no environment values. Assert the function does not open a listener or construct application state by keeping its only dependency `config.Load()`.

- [ ] **Step 2: Run and verify RED**

```bash
GOCACHE=/private/tmp/porsche-release-preflight-cache go test ./cmd/check-config -count=1
```

Expected: package or `run` is missing.

- [ ] **Step 3: Implement the command and Docker inclusion**

`run` calls `config.Load()`, writes the returned sanitized error to stderr on failure, and writes `configuration valid` on success. `main` calls `os.Exit(run(os.Stdout, os.Stderr))`. Build `/out/check-config` in the existing builder stage and copy it beside `server` and `bootstrap-root`.

- [ ] **Step 4: Extend Dockerfile regression and verify GREEN**

Require the exact `go build -o /out/check-config ./cmd/check-config` and final-stage copy. Run command tests, `deploy/test-dockerfile.sh`, and `docker build` only in the final verification batch.

- [ ] **Step 5: Commit Task 3**

```bash
git add cmd/check-config Dockerfile deploy/test-dockerfile.sh
git commit -m "feat: add production configuration preflight"
```

### Task 4: Add the frontend production Mock gate

**Files:**
- Create: `scripts/check-production-env.test.js`
- Create: `scripts/check-production-env.mjs`
- Modify: `package.json`
- Modify: `README.md`

- [ ] **Step 1: Write failing Node tests**

Import `validateProductionEnvironment` and assert:

```javascript
assert.doesNotThrow(() => validateProductionEnvironment({ VITE_USE_MOCK: 'false' }))
for (const value of [undefined, '', 'true', 'TRUE', '0']) {
  assert.throws(() => validateProductionEnvironment({ VITE_USE_MOCK: value }), /VITE_USE_MOCK must be exactly false/)
}
```

Use temporary `.env.production` files to prove the CLI reads Vite production environment resolution and that a process-level `VITE_USE_MOCK` override wins.

- [ ] **Step 2: Run and verify RED**

```bash
node --test scripts/check-production-env.test.js
```

Expected: module is missing.

- [ ] **Step 3: Implement the gate and build chain**

Use Vite `loadEnv('production', cwd, '')`, overlay explicitly declared `process.env.VITE_USE_MOCK`, validate exact string `false`, and print no environment values. Guard CLI execution with an `import.meta.url` comparison. Change the package script to:

```json
"build": "node ./scripts/check-production-env.mjs && vite build"
```

- [ ] **Step 4: Verify focused and full frontend behavior**

Run:

```bash
node --test scripts/check-production-env.test.js
VITE_USE_MOCK=false npm test
VITE_USE_MOCK=false npm run build
VITE_USE_MOCK=true npm run build
```

Expected: focused and full tests pass, false build succeeds, true build exits before Vite transforms modules.

- [ ] **Step 5: Commit Task 4**

```bash
git add scripts/check-production-env.mjs scripts/check-production-env.test.js package.json README.md
git commit -m "fix: block production mock builds"
```

### Task 5: Integrate immutable preflight into full-stack deployment

**Files:**
- Modify: `deploy/test-production-deploy.sh`
- Modify: `deploy/production-deploy.sh`
- Modify: `deploy/test-restart-all.sh`
- Modify: `deploy/restart-all.sh`

- [ ] **Step 1: Write failing production-deploy tests**

Extend the Docker mock to model image build/inspect and check-config execution. Require standalone deployment to build once, run `check-config` before `docker stop`, and deploy the inspected immutable image ID. Require `PREBUILT_IMAGE_ID=sha256:<64 lowercase hex>` to skip building, verify `docker image inspect`, run config check against that same ID, and use it in `docker run`. Invalid or missing image IDs must fail before Docker writes.

- [ ] **Step 2: Run and verify RED**

```bash
bash deploy/test-production-deploy.sh
```

Expected: no check-config invocation and no immutable prebuilt-image behavior.

- [ ] **Step 3: Implement production-deploy image preflight**

After checkout reset, either build and inspect the candidate or validate `PREBUILT_IMAGE_ID`. Run:

```bash
docker run --rm --env-file "$ENV_FILE" "${network_args[@]}" --entrypoint ./check-config "$candidate_image_id"
```

Only after success may the script rename or stop the current application. Use the immutable ID for the application `docker run`.

- [ ] **Step 4: Write failing restart-all tests**

Require this exact order under the full-stack lock:

```text
reset backend and frontend to origin/main
merge-env-example.sh .env.example .env
docker build backend candidate
docker image inspect candidate
docker run check-config using candidate ID
npm ci
npm run build
production-deploy.sh with the same PREBUILT_IMAGE_ID
nginx -t
publish static files
reload nginx
```

Add negative cases for missing `package-lock.json`, merge failure, preflight failure, and `npm ci` failure. Every case must prove there was no backend stop, frontend publish, or Nginx reload. Assert the command log contains no `npm install`.

- [ ] **Step 5: Run restart test and verify RED**

```bash
bash deploy/test-restart-all.sh
```

Expected: old `npm install --package-lock=false` and missing merge/preflight ordering fail assertions.

- [ ] **Step 6: Implement restart orchestration and verify GREEN**

Require `package-lock.json`, `merge-env-example.sh`, and `.env.example`. Reset both repositories before merging. Invoke the merge with absolute paths, build/inspect/preflight the backend candidate, use `npm ci`, and pass only the validated immutable ID to `production-deploy.sh`. Keep migration excluded and preserve existing static staging and Nginx behavior.

Run all three deployment shell suites and `bash -n` on modified scripts.

- [ ] **Step 7: Commit Task 5**

```bash
git add deploy/production-deploy.sh deploy/test-production-deploy.sh deploy/restart-all.sh deploy/test-restart-all.sh
git commit -m "fix: fail closed before full-stack release"
```

### Task 6: Documentation, full verification, and delivery

**Files:**
- Modify: `README.md`
- Modify: `progress.md`

- [ ] **Step 1: Update operator documentation**

Document additive `.env` merge, empty required-key halt, fixed-login declaration prohibition, immutable candidate preflight, committed lockfile and `npm ci`, explicit migration prerequisite, and the fact that this hotfix does not deploy production.

- [ ] **Step 2: Run backend verification**

```bash
bash deploy/test-merge-env-example.sh
bash deploy/test-dockerfile.sh
bash deploy/test-production-deploy.sh
bash deploy/test-restart-all.sh
GOCACHE=/private/tmp/porsche-release-preflight-cache go test ./...
GOCACHE=/private/tmp/porsche-release-preflight-cache go vet ./...
git diff --check
```

Expected: all commands pass. If Docker is available, also run `docker build -t porsche-release-preflight:test .` and record the exact image ID without deploying it.

- [ ] **Step 3: Run frontend verification**

```bash
A14_BACKEND_CONTRACT=/Users/xuzhihao/code/Porsche/.worktrees/production-release-preflight/docs/agents/contracts/admin-action-future-contract.json A03_BACKEND_CONTRACT=/Users/xuzhihao/code/Porsche/.worktrees/production-release-preflight/docs/agents/contracts/admin-user-create-v1.json VITE_USE_MOCK=false npm test
VITE_USE_MOCK=false npm run build
VITE_USE_MOCK=true npm run build
git diff --check
```

Expected: 275 existing tests plus new preflight tests pass; production false build succeeds; production true build fails before Vite compilation.

- [ ] **Step 4: Record status without overstating production**

Append exact command results and remaining migration/deployment/R02 blockers to each repository's `progress.md`. Keep the PRD and R02 statuses unchanged.

- [ ] **Step 5: Commit documentation and status**

```bash
git add README.md progress.md
git commit -m "docs: record release preflight verification"
```

- [ ] **Step 6: Final secret and scope checks**

Verify neither diff contains `.env`, a 43-character action key, credentials, build output, or production state changes. Verify both worktrees are clean and list the exact commits intended for review.
