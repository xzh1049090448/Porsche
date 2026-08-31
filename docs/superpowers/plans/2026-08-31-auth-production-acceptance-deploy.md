# Authentication Acceptance Deployment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Safely deploy the user-registration backend and session-auth frontend branches to `aiportcloud.com` for a reversible browser acceptance test without invoking the normal `main` release workflow.

**Architecture:** Add three root-only shell entry points: a one-time internal Redis bootstrap, an explicitly confirmed MySQL migration runner, and an acceptance deployment/rollback pair. The deployment entry point never switches or resets Git: it requires both server checkouts already be at their exact remote feature branch revisions, starts a candidate application only after frontend build and Nginx validation, and records a root-owned rollback manifest before publishing static assets. Shell behavior tests run each target in a disposable Docker sandbox with no Docker socket or host service mounts, making runtime isolation—not a hand-written shell lexer—the safety boundary.

**Tech Stack:** Bash with `set -Eeuo pipefail`, Docker/Redis 7, MySQL 8 migration command, npm/Vite, rsync, Nginx, shell mock regression tests.

---

## File map

- Create: `deploy/bootstrap-auth-redis.sh` — root-only creation of an internal, persistent, password-protected Redis container.
- Create: `deploy/auth-acceptance-migrate.sh` — separately confirmed migration runner; never starts the application.
- Create: `deploy/auth-acceptance-deploy.sh` — branch-gated backend/frontend candidate deployment and rollback-manifest creation.
- Create: `deploy/auth-acceptance-rollback.sh` — uses the manifest to restore the prior container and static files; never changes MySQL.
- Create: `deploy/test-auth-acceptance-deploy.sh` — isolated behavioral shell fixture for all four entry points.
- Modify: `README.md` — test-only deployment, Redis, migration, browser acceptance, and rollback instructions.
- Modify: `progress.md` and `feature_list.json` — record the new deployment tool as evidence while retaining `go-006` as the unique active feature.

### Task 1: Create the isolated shell regression fixture

**Files:**
- Create: `deploy/test-auth-acceptance-deploy.sh`
- Test: `deploy/test-auth-acceptance-deploy.sh`

- [ ] **Step 1: Write a failing script-existence and safety-contract test**

```bash
for script in bootstrap-auth-redis.sh auth-acceptance-migrate.sh auth-acceptance-deploy.sh auth-acceptance-rollback.sh; do
  test -x "$script_dir/$script" || { echo "missing $script" >&2; exit 1; }
done
! grep -Eq 'compose[[:space:]]+down|volume[[:space:]]+rm|network[[:space:]]+rm|docker[[:space:]]+prune|mysql[[:space:]].*DROP' \
  "$script_dir"/{bootstrap-auth-redis,auth-acceptance-migrate,auth-acceptance-deploy,auth-acceptance-rollback}.sh
```

- [ ] **Step 2: Run the fixture to verify it fails**

Run: `bash deploy/test-auth-acceptance-deploy.sh`

Expected: failure naming the first missing script.

- [ ] **Step 3: Build shared fixture setup with no real credentials or Docker writes**

Create a `mktemp -d` fixture containing backend/frontend Git directories, a root-owned-looking `.env` with fake values, a static root, a manifest directory, and mock `git`, `docker`, `npm`, `rsync`, `nginx`, `systemctl`, `flock`, and `id` executables. Each mock writes NUL-delimited argv records to `$COMMAND_LOG` and returns only controlled `MOCK_*` results. The fixture must set only documented `PORSCHE_AUTH_ACCEPTANCE_TEST_MODE=1` path overrides; production scripts must reject those overrides.

Run every target entry point through a disposable `bash:5.2` Docker container
with `--network none`, no `/var/run/docker.sock`, no database/Redis mount, and
only the `mktemp` fixture mounted beneath `/fixture`. Pass the mock directory
as `PATH` and keep all command logs below `/fixture`. The outer regression
runner may create this disposable test container, but it must never invoke a
target against the host shell. If Docker or the test image is unavailable,
fail with a clear prerequisite message rather than silently executing the
target on the host.

- [ ] **Step 4: Add behavior assertions before implementing commands**

Add these named checks, each expected to fail until its target behavior exists:

```bash
assert_bootstrap_creates_internal_redis() {
  run_bootstrap
  require_call 'docker volume create porsche-redis-data'
  require_call 'docker run -d --name porsche-redis --restart unless-stopped --network porsche-app'
  forbid_call 'docker run -p'
}

assert_deploy_refuses_main_or_dirty_checkout_without_writes() {
  if MOCK_BACKEND_BRANCH=main run_deploy; then fail 'accepted main'; fi
  assert_no_docker_or_rsync_writes
}

assert_candidate_failure_restores_old_application() {
  if MOCK_HEALTH_RESULT=failure run_deploy; then fail 'candidate unexpectedly healthy'; fi
  require_call 'docker rm -f -- ai-gateway-go'
  require_call 'docker rename -- ai-gateway-go-acceptance-rollback-'
  require_call 'docker start -- ai-gateway-go'
  forbid_call 'rsync --archive --delete --delay-updates'
}
```

Also create a fake target that runs `/opt/homebrew/bin/docker`, `command
/custom/bin/rsync`, and `bash helper.sh`; run it inside the sandbox and assert
that no host Docker service is reachable and no file outside the fixture can
be altered. Do not maintain a custom shell lexer as a security boundary.

- [ ] **Step 5: Run fixture to verify RED behaviors**

Run: `bash deploy/test-auth-acceptance-deploy.sh`

Expected: failure at missing scripts or missing required mock command ordering.

- [ ] **Step 6: Commit test skeleton**

```bash
git add deploy/test-auth-acceptance-deploy.sh
git commit -m "test: define auth acceptance deployment contract"
```

### Task 2: Add password-protected internal Redis bootstrap

**Files:**
- Create: `deploy/bootstrap-auth-redis.sh`
- Test: `deploy/test-auth-acceptance-deploy.sh`

- [ ] **Step 1: Add the failing password and network checks**

Extend the fixture so empty, shorter-than-32-byte, and already-existing container cases fail before `docker volume create` or `docker run`. The happy path must show one named volume and one Redis container with no `-p` / `--publish` argument.

- [ ] **Step 2: Run the focused bootstrap fixture and verify RED**

Run: `bash deploy/test-auth-acceptance-deploy.sh bootstrap`

Expected: failure because the bootstrap command does not exist.

- [ ] **Step 3: Implement `bootstrap-auth-redis.sh` minimally**

Implement these production rules:

```bash
#!/usr/bin/env bash
set -Eeuo pipefail
(( $# == 0 )) || { echo 'bootstrap-auth-redis.sh accepts no arguments' >&2; exit 64; }
[[ $(id -u) == 0 ]] || { echo 'bootstrap-auth-redis.sh must run as root' >&2; exit 1; }
docker network inspect porsche-app >/dev/null
docker container inspect porsche-redis >/dev/null 2>&1 && {
  echo 'porsche-redis already exists; refusing replacement' >&2; exit 1;
}
read -rsp 'Redis password (32+ bytes): ' redis_password; echo
[[ ${#redis_password} -ge 32 ]] || { echo 'Redis password must contain at least 32 bytes' >&2; exit 1; }
```

Use `umask 077`, create `/var/lib/porsche-auth-redis/redis.conf` without printing the password, write `appendonly yes`, `save 60 1`, and `requirepass <password>` to it, then run:

```bash
docker volume create porsche-redis-data
docker run -d --name porsche-redis --restart unless-stopped --network porsche-app \
  --mount type=volume,src=porsche-redis-data,dst=/data \
  --mount type=bind,src=/var/lib/porsche-auth-redis/redis.conf,dst=/usr/local/etc/redis/redis.conf,readonly \
  redis:7-alpine redis-server /usr/local/etc/redis/redis.conf
```

On any failed start, remove only `porsche-redis`; retain the named volume and config for inspection. Print only the literal `.env` key name `REDIS_URL`, never its value or the password. Accept test-only prompt injection only when `PORSCHE_AUTH_ACCEPTANCE_TEST_MODE=1`.

- [ ] **Step 4: Run focused fixture to verify GREEN**

Run: `bash deploy/test-auth-acceptance-deploy.sh bootstrap`

Expected: `PASS: bootstrap contract`; command log has no port publish, prune, compose, or MySQL command.

- [ ] **Step 5: Commit**

```bash
git add deploy/bootstrap-auth-redis.sh deploy/test-auth-acceptance-deploy.sh
git commit -m "feat: bootstrap internal auth redis"
```

### Task 3: Add the explicit migration gate

**Files:**
- Create: `deploy/auth-acceptance-migrate.sh`
- Test: `deploy/test-auth-acceptance-deploy.sh`

- [ ] **Step 1: Add failing migration-contract tests**

Require `auth-acceptance-migrate.sh` to reject no argument, reject anything except `--confirm-auth-schema-migration`, reject missing `.env`, and make zero Docker writes on rejection. On the happy path assert exactly one command resembling:

```text
docker run --rm --env-file <backend>/.env --network porsche-app -v <backend>:/src:ro -w /src golang:1.22-alpine sh -ec ... go run ./cmd/migrate up
```

Also assert it has no `docker stop`, `docker rm`, `docker volume`, `docker network rm`, application start, static publish, or Git branch operation.

- [ ] **Step 2: Run migration fixture to verify RED**

Run: `bash deploy/test-auth-acceptance-deploy.sh migration`

Expected: failure naming the absent command.

- [ ] **Step 3: Implement `auth-acceptance-migrate.sh`**

Require root, exactly one literal confirmation argument, `/opt/Porsche/.env`, Docker, and `porsche-app`. Refuse unless the current backend branch is exactly `feature/user-registration-management` and its tracked remote is the same SHA after `git fetch origin feature/user-registration-management`. Run the pinned Go image command from the approved design. Do not inspect, print, rewrite, or parse credentials.

- [ ] **Step 4: Run focused fixture to verify GREEN**

Run: `bash deploy/test-auth-acceptance-deploy.sh migration`

Expected: `PASS: migration contract`.

- [ ] **Step 5: Commit**

```bash
git add deploy/auth-acceptance-migrate.sh deploy/test-auth-acceptance-deploy.sh
git commit -m "feat: gate auth schema migration explicitly"
```

### Task 4: Implement candidate deployment and manifest-based rollback

**Files:**
- Create: `deploy/auth-acceptance-deploy.sh`
- Create: `deploy/auth-acceptance-rollback.sh`
- Test: `deploy/test-auth-acceptance-deploy.sh`

- [ ] **Step 1: Add failing deployment-order tests**

Extend the fixture with these assertions:

```bash
assert_before 'npm run build' 'docker build --tag ai-gateway-go:auth-acceptance'
assert_before 'nginx -t' 'docker stop -- ai-gateway-go'
assert_before 'docker run -d --name ai-gateway-go' 'rsync --archive --delete --delay-updates'
assert_before 'rsync --archive --delete --delay-updates' 'systemctl reload nginx'
assert_manifest_contains_no_secret_values
```

Add negative cases for wrong branch, remote SHA mismatch, tracked dirty tree, missing Redis container, missing `.env`, invalid Nginx, candidate failure, rsync failure, and reload failure. Each must preserve/restart the old application and prevent or restore static publication. Assert production mode ignores all path/port/name override variables.

- [ ] **Step 2: Run deployment fixture to verify RED**

Run: `bash deploy/test-auth-acceptance-deploy.sh deploy`

Expected: failure because the deployment and rollback commands do not exist.

- [ ] **Step 3: Implement `auth-acceptance-deploy.sh`**

Use fixed production constants:

```bash
BACKEND_DIR=/opt/Porsche
FRONTEND_DIR=/opt/Porsche-Web
FRONTEND_ROOT=/var/www/porsche-web
NETWORK=porsche-app
APP_NAME=ai-gateway-go
IMAGE_NAME=ai-gateway-go:auth-acceptance
BACKEND_BRANCH=feature/user-registration-management
FRONTEND_BRANCH=feature/session-auth-frontend
LOCK_FILE=/var/lock/porsche-auth-acceptance.deploy.lock
MANIFEST_DIR=/var/lib/porsche-auth-acceptance
```

Before build or Docker writes, require root; no arguments; both current branches and `origin/<branch>` SHA matches; clean tracked worktrees; backend `.env`; `APP_ENV=production`; `ALLOWED_HOSTS` containing exactly the normal hostname syntax; `AUTH_TRUSTED_ORIGINS` containing `https://aiportcloud.com`; `REDIS_URL` nonempty; existing `porsche-redis`; existing `porsche-app`; and `nginx -t` success. The script must not source `.env`.

Build frontend with `npm install --package-lock=false` and `npm run build`, stage it in a private `mktemp` directory, then build the backend image. Rename the old `ai-gateway-go` only after all prerequisite checks. Start candidate on `127.0.0.1:8000:8000` with `--env-file /opt/Porsche/.env --network porsche-app`; health check `http://127.0.0.1:8000/health` with `Host: aiportcloud.com`, 2-second connect, 3-second maximum, and 30 attempts.

On success, copy the old frontend root to a `mktemp` rollback directory beneath `/var/lib/porsche-auth-acceptance`, write a `0600` manifest containing only container name, static rollback directory, and Git SHA values, publish staged assets with `rsync --archive --delete --delay-updates`, and reload Nginx. Keep the renamed old container inactive for later explicit rollback. Every failure after rename must remove only the candidate, restore/start the old container, and restore static files if they were changed.

- [ ] **Step 4: Implement `auth-acceptance-rollback.sh`**

Require root and exactly `--confirm-auth-acceptance-rollback`. Require a `0600` manifest owned by root, validate that its container name matches `^ai-gateway-go-acceptance-rollback-[0-9]+$` and its static directory is directly beneath `/var/lib/porsche-auth-acceptance/`. Run `nginx -t` before replacing static files. Stop/remove only the known current `ai-gateway-go`, rename/start the manifest rollback container, restore static files from the manifest with rsync, reload Nginx, then delete the manifest. Do not change Git, execute migrations, remove Redis, or remove a Docker volume/network.

- [ ] **Step 5: Run deployment and rollback fixture to verify GREEN**

Run: `bash deploy/test-auth-acceptance-deploy.sh deploy rollback`

Expected: `PASS: auth acceptance deployment regression checks`.

- [ ] **Step 6: Commit**

```bash
git add deploy/auth-acceptance-deploy.sh deploy/auth-acceptance-rollback.sh deploy/test-auth-acceptance-deploy.sh
git commit -m "feat: deploy auth acceptance candidate safely"
```

### Task 5: Document operator flow, then verify the implementation

**Files:**
- Modify: `README.md`
- Modify: `progress.md`
- Modify: `feature_list.json`
- Test: `deploy/test-auth-acceptance-deploy.sh`

- [ ] **Step 1: Add failing documentation assertions**

Make the shell fixture require the README to contain all literal commands below and forbid a claim that database rollback is automatic:

```text
sudo bash deploy/bootstrap-auth-redis.sh
sudo bash deploy/auth-acceptance-migrate.sh --confirm-auth-schema-migration
sudo bash deploy/auth-acceptance-deploy.sh
sudo bash deploy/auth-acceptance-rollback.sh --confirm-auth-acceptance-rollback
```

- [ ] **Step 2: Run docs check to verify RED**

Run: `bash deploy/test-auth-acceptance-deploy.sh docs`

Expected: failure for absent acceptance-deployment documentation.

- [ ] **Step 3: Update documentation and trackers**

Document the exact configuration requirements, how to make the Redis password and set `REDIS_URL` without committing it, the explicit migration confirmation, health/rollback behavior, and browser acceptance checklist. State direct production-domain test impact and that the migration remains after an application rollback. Update `progress.md` and `feature_list.json` with only verified shell/test evidence; keep `go-006` as the sole `in_progress` feature until browser E2E and final authentication delivery are actually complete.

- [ ] **Step 4: Run all verification commands**

Run:

```bash
bash -n deploy/bootstrap-auth-redis.sh deploy/auth-acceptance-migrate.sh \
  deploy/auth-acceptance-deploy.sh deploy/auth-acceptance-rollback.sh \
  deploy/test-auth-acceptance-deploy.sh
bash deploy/test-auth-acceptance-deploy.sh
bash deploy/test-production-deploy.sh
bash deploy/test-restart-all.sh
bash deploy/nginx/test-aiportcloud-conf.sh
GOCACHE=/private/tmp/porsche-go-build-cache go test ./... -count=1
GOCACHE=/private/tmp/porsche-go-build-cache go vet ./...
git diff --check
```

Expected: all commands pass. If the local sandbox cannot bind `httptest` loopback listeners, repeat Go tests in the approved loopback environment and record the limitation without weakening test coverage.

- [ ] **Step 5: Commit**

```bash
git add README.md progress.md feature_list.json deploy/test-auth-acceptance-deploy.sh
git commit -m "docs: describe auth acceptance deployment"
```
