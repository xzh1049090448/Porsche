# Full Stack Restart Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add one production-safe command that updates and restarts the Go application and publishes the Porsche-Web static frontend without managing MySQL.

**Architecture:** `deploy/restart-all.sh` prepares the frontend before touching production traffic, delegates the backend replacement and rollback to `production-deploy.sh`, then publishes the already-built static files and reloads validated Nginx configuration. `production-deploy.sh` derives a permitted health-check Host from the existing backend `.env`, so host protection does not make a healthy local application look unavailable.

**Tech Stack:** Bash, Docker, Git, npm/Vite, rsync, Nginx, systemd.

---

### Task 1: Make backend health checks compatible with `ALLOWED_HOSTS`

**Files:**
- Modify: `deploy/production-deploy.sh:19-140`
- Modify: `deploy/test-production-deploy.sh:15-109`

- [ ] **Step 1: Add a failing shell assertion for the permitted Host header**

In `deploy/test-production-deploy.sh`, put an allowed host in the fixture `.env` and require the health call to contain it:

```bash
printf 'DATABASE_URL=mysql://test:secret@db/test\nALLOWED_HOSTS=example.com,127.0.0.1\n' >"$repo_dir/.env"
# ... after run_deploy in assert_successful_deploy
require_line health-host 'curl -fsS -H Host: example.com --connect-timeout 2 --max-time 3 http://127.0.0.1:18000/health' >/dev/null
```

- [ ] **Step 2: Run the focused regression and verify it fails**

Run:

```bash
bash deploy/test-production-deploy.sh
```

Expected: FAIL because `production-deploy.sh` does not send a `Host` header.

- [ ] **Step 3: Parse the first allowed host and send it only to local health checks**

Add this immediately after the existing `[[ ! -f "$ENV_FILE" ]]` check in `deploy/production-deploy.sh`:

```bash
allowed_hosts_line="$(sed -n 's/^[[:space:]]*ALLOWED_HOSTS[[:space:]]*=[[:space:]]*//p' "$ENV_FILE" | head -n 1)"
health_check_host="${allowed_hosts_line%%,*}"
health_check_host="${health_check_host//[[:space:]]/}"
if [[ -z "$health_check_host" ]]; then
    echo 'deployment requires a non-empty ALLOWED_HOSTS entry in .env' >&2
    exit 1
fi
```

Replace the loop request with:

```bash
curl -fsS -H "Host: ${health_check_host}" --connect-timeout 2 --max-time 3 \
  "http://127.0.0.1:${HOST_PORT}/health" >/dev/null
```

- [ ] **Step 4: Re-run the regression and existing deploy checks**

Run:

```bash
bash -n deploy/production-deploy.sh deploy/test-production-deploy.sh
bash deploy/test-production-deploy.sh
```

Expected: PASS; test command log proves the header is present and all rollback/lock checks remain green.

- [ ] **Step 5: Commit the focused fix**

```bash
git add deploy/production-deploy.sh deploy/test-production-deploy.sh
git commit -m "fix: send allowed host in deployment health check"
```

### Task 2: Add the all-in-one restart script and behavioral mock tests

**Files:**
- Create: `deploy/restart-all.sh`
- Create: `deploy/test-restart-all.sh`

- [ ] **Step 1: Write failing behavioral tests using a temporary two-repository fixture**

Create `deploy/test-restart-all.sh`. It must symlink `restart-all.sh` and `production-deploy.sh` into a temporary backend fixture, create a separate frontend fixture with a `package.json` and `dist/index.html`, and mock `git`, `npm`, `rsync`, `nginx`, `systemctl`, `docker`, and `flock`. Assert these observable behaviors:

```bash
# happy path ordering
assert_before 'npm run build' 'backend deploy/production-deploy.sh'
assert_before 'backend deploy/production-deploy.sh' 'rsync --archive --delete --delay-updates'
assert_before 'nginx -t' 'systemctl reload nginx'

# dependency/build failure prevents backend deploy and rsync
MOCK_NPM_RESULT=failure assert_failure_without 'backend deploy/production-deploy.sh'
MOCK_NPM_RESULT=failure assert_failure_without 'rsync --archive --delete --delay-updates'

# backend failure prevents frontend publish and Nginx reload
MOCK_BACKEND_DEPLOY_RESULT=failure assert_failure_without 'rsync --archive --delete --delay-updates'
MOCK_BACKEND_DEPLOY_RESULT=failure assert_failure_without 'systemctl reload nginx'

# nginx validation failure prevents reload
MOCK_NGINX_RESULT=failure assert_failure_without 'systemctl reload nginx'
```

Create the fixture backend deployment script as a mock that logs `backend-deploy` and asserts `APP_DOCKER_NETWORK=porsche-app`. Also assert the command log contains `backend-deploy` and contains no `mysql`, `compose down`, `prune`, `volume rm`, `network rm`, or `docker rm` write command.

- [ ] **Step 2: Run the new mock suite and verify it fails**

Run:

```bash
bash deploy/test-restart-all.sh
```

Expected: FAIL because `deploy/restart-all.sh` does not exist.

- [ ] **Step 3: Implement `deploy/restart-all.sh`**

Implement these exact operational boundaries:

```bash
#!/usr/bin/env bash
set -Eeuo pipefail

BACKEND_DIR=/opt/Porsche
FRONTEND_DIR=/opt/Porsche-Web
FRONTEND_ROOT=/var/www/porsche-web
APP_DOCKER_NETWORK=porsche-app
LOCK_FILE=/var/lock/porsche-full-stack.deploy.lock
```

The script must reject arguments and non-root execution. It must obtain a non-blocking `flock -E 75 -n` before Git or Docker writes. It must check `git docker npm rsync nginx systemctl flock` with `command -v`, check both repositories plus `$BACKEND_DIR/.env`, `$FRONTEND_DIR/package.json`, `$BACKEND_DIR/deploy/production-deploy.sh`, and `docker network inspect "$APP_DOCKER_NETWORK"`.

Implement the execution sequence as:

```bash
# frontend: tracked-tree check, fetch main, switch main, reset to origin/main
(cd "$FRONTEND_DIR" && git diff --quiet && git diff --cached --quiet && \
  git fetch origin main && git switch main && git reset --hard origin/main && \
  npm install --package-lock=false && npm run build)

# backend: reuse only its existing rollback-capable deployment script
(cd "$BACKEND_DIR" && APP_DOCKER_NETWORK="$APP_DOCKER_NETWORK" \
  ./deploy/production-deploy.sh)

# frontend: stage first, then synchronize static output
stage_dir="$(mktemp -d /var/www/.porsche-web-stage.XXXXXX)"
trap 'rm -rf -- "$stage_dir"' EXIT
rsync --archive --delete --delay-updates "$FRONTEND_DIR/dist/" "$stage_dir/"
rsync --archive --delete --delay-updates "$stage_dir/" "$FRONTEND_ROOT/"

nginx -t
systemctl reload nginx
```

The cleanup trap may delete only its `mktemp -d /var/www/.porsche-web-stage.*` directory. It must never remove production assets, Docker containers, networks, volumes, or databases. Print the two `git rev-parse HEAD` values only after every prior command succeeds.

- [ ] **Step 4: Run the restart regression suite and syntax checks**

Run:

```bash
bash -n deploy/restart-all.sh deploy/test-restart-all.sh
bash deploy/test-restart-all.sh
```

Expected: PASS; mocked failures prove no later deployment action occurs.

- [ ] **Step 5: Commit the script and tests**

```bash
git add deploy/restart-all.sh deploy/test-restart-all.sh
git commit -m "feat: add full stack restart script"
```

### Task 3: Serve the frontend while retaining backend gateway routes

**Files:**
- Modify: `deploy/nginx/aiportcloud.conf:1-48`
- Modify: `deploy/nginx/test-aiportcloud-conf.sh`

- [ ] **Step 1: Add failing static Nginx assertions**

In `deploy/nginx/test-aiportcloud-conf.sh`, require the frontend root and SPA fallback plus all backend proxy prefixes:

```bash
grep -Fq 'root /var/www/porsche-web;' "$config"
grep -Fq 'try_files $uri $uri/ /index.html;' "$config"
for location in 'location /api/' 'location /v1/' 'location /admin/' 'location = /health'; do
  grep -Fq "$location" "$config"
done
```

- [ ] **Step 2: Run the Nginx static test and verify it fails**

Run:

```bash
bash deploy/nginx/test-aiportcloud-conf.sh
```

Expected: FAIL because the existing configuration proxies every `/` request to the backend.

- [ ] **Step 3: Split static frontend and backend proxy locations**

In the TLS server block, set:

```nginx
root /var/www/porsche-web;
index index.html;
```

Move the current secure proxy headers, HTTP/1.1, disabled buffering, and 300-second timeouts into four explicit locations: `/api/`, `/v1/`, `/admin/`, and exact `/health`. Then replace the catch-all proxy location with:

```nginx
location / {
    try_files $uri $uri/ /index.html;
}
```

Do not change `X-Forwarded-For $remote_addr`, certificate paths, server names, or the default-server deny block.

- [ ] **Step 4: Run static configuration checks**

Run:

```bash
bash deploy/nginx/test-aiportcloud-conf.sh
git diff --check
```

Expected: PASS. If Nginx is installed locally, additionally run `nginx -t -c "$(pwd)/deploy/nginx/aiportcloud.conf"`; otherwise record that only static checks were possible.

- [ ] **Step 5: Commit Nginx routing support**

```bash
git add deploy/nginx/aiportcloud.conf deploy/nginx/test-aiportcloud-conf.sh
git commit -m "feat: serve frontend from nginx"
```

### Task 4: Document operation and run final verification

**Files:**
- Modify: `README.md:42-90`
- Modify: `progress.md`
- Modify: `feature_list.json`

- [ ] **Step 1: Add operator documentation**

Document the exact production command:

```bash
sudo /opt/Porsche/deploy/restart-all.sh
```

State the required fixed paths and network, that the frontend command uses `npm install --package-lock=false` because no lockfile is committed, that MySQL is not managed by this command, and that Nginx must proxy `/api/`, `/v1/`, `/admin/`, and `/health` while serving the SPA from `/var/www/porsche-web`.

- [ ] **Step 2: Record evidence without altering unrelated feature status**

Append the restart-script verification commands/results to `progress.md`. Do not change `go-004` status, because live JieKou smoke remains independent of this script.

- [ ] **Step 3: Run all relevant verification commands**

Run:

```bash
bash -n deploy/production-deploy.sh deploy/test-production-deploy.sh deploy/restart-all.sh deploy/test-restart-all.sh
bash deploy/test-production-deploy.sh
bash deploy/test-restart-all.sh
bash deploy/nginx/test-aiportcloud-conf.sh
GOCACHE=/private/tmp/porsche-go-build-cache go test ./... -count=1
GOCACHE=/private/tmp/porsche-go-build-cache go vet ./...
git diff --check
```

Expected: all commands PASS. The Go test command may require an environment that permits temporary localhost listeners; if it does, record the successful elevated run rather than interpreting sandbox listener denial as a code failure.

- [ ] **Step 4: Commit documentation and evidence**

```bash
git add README.md progress.md feature_list.json
git commit -m "docs: document full stack restart"
```
