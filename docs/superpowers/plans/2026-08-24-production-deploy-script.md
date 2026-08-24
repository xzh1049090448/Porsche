# Production Deployment Script Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a safe, repeatable production deployment script that synchronizes remote `main` and replaces only the Go application container using the existing `.env`.

**Architecture:** A Bash script validates prerequisites and tracked-worktree cleanliness, resets only tracked source to `origin/main`, builds a candidate image, stops and renames the old application to release the fixed loopback port, then health-checks the replacement container. If startup or health fails, it restores the renamed old container. A standalone mocked shell test locks in the exact safety behavior without requiring Docker or credentials.

**Tech Stack:** Bash, Git, Docker CLI, existing Go Dockerfile.

---

## File Structure

- Create `deploy/production-deploy.sh`: production operator entrypoint; never reads `.env` content or manages MySQL.
- Create `deploy/test-production-deploy.sh`: no-Docker static contract test for the deployment script.
- Modify `README.md`: operator-facing invocation, optional network behavior, and Nginx/real-smoke boundary.
- Modify `feature_list.json` and `progress.md`: record script status and verification evidence.

### Task 1: Lock in deployment safety invariants

**Files:**
- Create: `deploy/test-production-deploy.sh`
- Test: `deploy/test-production-deploy.sh`

- [ ] **Step 1: Write a failing static deployment-contract test**

```bash
#!/usr/bin/env bash
set -Eeuo pipefail
script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/production-deploy.sh"
test -f "$script"
grep -Fq 'git fetch origin main' "$script"
grep -Fq 'git reset --hard origin/main' "$script"
grep -Fq -- '--env-file .env' "$script"
grep -Fq '127.0.0.1:${HOST_PORT}:8000' "$script"
! grep -Eq 'docker (volume rm|system prune|compose down -v)' "$script"
```

- [ ] **Step 2: Run the test and verify RED**

Run: `bash deploy/test-production-deploy.sh`

Expected: FAIL because `production-deploy.sh` does not yet exist.

- [ ] **Step 3: Extend the test with bounded-interruption and rollback assertions**

```bash
assert_call_order stop-old rename-old run-new health remove-rollback
run_with_failed_health
assert_call_order stop-old rename-old run-new health remove-new restore-name start-old
```

- [ ] **Step 4: Commit the red test**

```bash
git add deploy/test-production-deploy.sh
git commit -m "test: define production deploy safety contract"
```

### Task 2: Implement the production deployment script

**Files:**
- Create: `deploy/production-deploy.sh`
- Test: `deploy/test-production-deploy.sh`

- [ ] **Step 1: Add strict prerequisites and source synchronization**

```bash
#!/usr/bin/env bash
set -Eeuo pipefail

APP_NAME="${APP_NAME:-ai-gateway-go}"
IMAGE_NAME="${IMAGE_NAME:-ai-gateway-go:main}"
HOST_PORT="${HOST_PORT:-8000}"
APP_DOCKER_NETWORK="${APP_DOCKER_NETWORK:-}"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
command -v git >/dev/null
command -v docker >/dev/null
test -f .env
[[ "$HOST_PORT" =~ ^[1-9][0-9]{0,4}$ ]] && (( HOST_PORT <= 65535 ))
test -z "$(git status --porcelain --untracked-files=no)"
git fetch origin main
git switch main
git reset --hard origin/main
```

- [ ] **Step 2: Add bounded-interruption lifecycle, rollback, and health check**

```bash
rollback_name="${APP_NAME}-rollback-$$"
had_previous=false
restore_previous() {
  docker rm -f "$APP_NAME" >/dev/null 2>&1 || true
  if [[ "$had_previous" == true ]]; then
    docker rename "$rollback_name" "$APP_NAME"
    docker start "$APP_NAME"
  fi
}
trap restore_previous ERR

network_args=()
if [[ -n "$APP_DOCKER_NETWORK" ]]; then
  docker network inspect "$APP_DOCKER_NETWORK" >/dev/null
  network_args=(--network "$APP_DOCKER_NETWORK")
fi

docker build --tag "$IMAGE_NAME" .
if docker container inspect "$APP_NAME" >/dev/null 2>&1; then
  had_previous=true
  docker stop "$APP_NAME"
  docker rename "$APP_NAME" "$rollback_name"
fi
docker run -d --name "$APP_NAME" --env-file "$ENV_FILE" \
  --publish "127.0.0.1:${HOST_PORT}:8000" "${network_args[@]}" "$IMAGE_NAME"
for _ in $(seq 1 30); do
  curl -fsS "http://127.0.0.1:${HOST_PORT}/health" >/dev/null && break
  sleep 1
done
curl -fsS "http://127.0.0.1:${HOST_PORT}/health" >/dev/null
if [[ "$had_previous" == true ]]; then
  docker rm "$rollback_name"
fi
trap - EXIT
```

- [ ] **Step 3: Run syntax and static tests to verify GREEN**

Run: `bash -n deploy/production-deploy.sh && bash deploy/test-production-deploy.sh`

Expected: PASS.

- [ ] **Step 4: Commit the implementation**

```bash
git add deploy/production-deploy.sh deploy/test-production-deploy.sh
git commit -m "feat: add production deployment script"
```

### Task 3: Document and verify operation boundaries

**Files:**
- Modify: `README.md`
- Modify: `feature_list.json`
- Modify: `progress.md`

- [ ] **Step 1: Document the exact invocation and optional network**

```markdown
APP_DOCKER_NETWORK=ai-gateway_default sudo -E bash deploy/production-deploy.sh
```

Document that `.env` is the production default configuration source (with `ENV_FILE` reserved for isolated test fixtures) and that the script never starts, replaces, or deletes MySQL. Document the brief interruption and automatic rollback behavior.

- [ ] **Step 2: Document the Nginx and white-label smoke boundary**

```markdown
Before deployment, set `TRUST_PROXY_HEADERS=true` and a deployment-specific
`TRUSTED_PROXY_CIDRS`; validate Nginx with `sudo nginx -t` after installing
the supplied proxy configuration. Run directory, non-stream, SSE and IP
allowlist smoke tests separately with non-logged shell secrets.
```

- [ ] **Step 3: Record feature evidence**

Set `go-004` to `passing` only for script/static verification; retain real-white-label smoke as an explicit external pending item rather than claiming it ran.

- [ ] **Step 4: Run full verification**

Run: `bash -n deploy/production-deploy.sh && bash deploy/test-production-deploy.sh && GOCACHE=/private/tmp/porsche-go-build-cache go test ./... && GOCACHE=/private/tmp/porsche-go-build-cache go vet ./... && git diff --check`

Expected: PASS. If `httptest` cannot bind in the sandbox, repeat the Go commands with local-listener permission and record that environmental limitation.

- [ ] **Step 5: Commit documentation and evidence**

```bash
git add README.md feature_list.json progress.md
git commit -m "docs: document production deployment workflow"
```
