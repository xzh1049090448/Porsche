# Public Renderer and Joint Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Materialize snapshot-consistent public HTML and sitemap within 60 seconds, preserve the last-good site on failure, integrate the renderer with `restart-all.sh`, and produce P01/P03-P08 joint evidence.

**Architecture:** Porsche-Web supplies a Node renderer that consumes only public APIs and writes a complete private stage tree. Porsche deployment scripts install a locked-down systemd service/timer or equivalent foreground worker, validate the staged generation, and atomically switch a symlink under `/var/www`. The backend render-job protocol provides generations, leases, completion, and sanitized failures.

**Tech Stack:** Node 18+, Vue/Vite build manifest, filesystem staging, systemd, Bash deployment fixtures, Nginx, Go render-job CLI/API.

---

### Task 1: Implement deterministic HTML generation in Porsche-Web

**Files (Porsche-Web worktree):**
- Create: `scripts/public-renderer/render.js`
- Create: `scripts/public-renderer/render.test.js`
- Create: `scripts/public-renderer/templates.js`
- Create: `scripts/public-renderer/templates.test.js`
- Create: `scripts/public-renderer/sitemap.js`
- Create: `scripts/public-renderer/sitemap.test.js`
- Modify: `package.json`
- Modify: `vite.config.js`

- [ ] **Step 1: Write RED renderer tests**

Use an in-memory filesystem/fetch adapter and require title, description, canonical URL, visible release content, escaped user text, exact generation metadata, active model detail files, 404/410 metadata, and public-only sitemap membership.

- [ ] **Step 2: Run RED**

Run: `node --test scripts/public-renderer/*.test.js`  
Expected: module-not-found failure.

- [ ] **Step 3: Implement pure render functions**

Accept `{site, home, pages, models, generation}` and return a map of relative path to UTF-8 content. Enable the Vite build manifest in `vite.config.js` and reference its hashed assets through a supplied manifest. Never call admin APIs.

- [ ] **Step 4: Add authenticated-price redaction tests**

Require no numeric prices or pricing model URLs in anonymous HTML/sitemap when publication visibility is authenticated. Require normal public output when visibility is public.

- [ ] **Step 5: Run GREEN and commit in Porsche-Web**

```bash
node --test scripts/public-renderer/*.test.js
git add scripts/public-renderer package.json vite.config.js
git commit -m "feat: render public snapshot html"
```

### Task 2: Implement private staging validation and atomic switch

**Files (Porsche-Web worktree):**
- Create: `scripts/public-renderer/filesystem.js`
- Create: `scripts/public-renderer/filesystem.test.js`
- Create: `scripts/public-renderer/worker.js`
- Create: `scripts/public-renderer/worker.test.js`

- [ ] **Step 1: Write RED filesystem tests**

Require `0700` private stage, regular-file-only output, traversal rejection, generation marker, complete path manifest, same-filesystem rename/symlink switch, last-good preservation, bounded old-generation retention, and cleanup limited to renderer-owned names.

- [ ] **Step 2: Run RED**

Run: `node --test scripts/public-renderer/filesystem.test.js scripts/public-renderer/worker.test.js`  
Expected: module-not-found failure.

- [ ] **Step 3: Implement stage/validate/switch worker**

Fetch all public endpoints with bounded timeouts, reject mixed ETag/generation values, write and fsync the tree, validate required HTML/sitemap/manifest files, then atomically replace only the `public-current` symlink.

- [ ] **Step 4: Run failure matrix**

Inject fetch timeout, malformed JSON, mixed generation, missing Vite asset, write failure, validation failure, and switch failure.  
Expected: every case leaves the prior symlink target unchanged and reports a sanitized failure.

- [ ] **Step 5: Commit in Porsche-Web**

```bash
git add scripts/public-renderer
git commit -m "feat: atomically publish rendered pages"
```

### Task 3: Add Nginx public-file routing

**Files (Porsche backend worktree):**
- Modify: `deploy/nginx/aiportcloud.conf`
- Modify: `deploy/nginx/test-aiportcloud-conf.sh`

- [ ] **Step 1: Add failing static Nginx assertions**

Require `/`, `/pricing`, `/pricing/<safe-modelKey>`, `/about`, `/terms`, `/privacy`, and `/sitemap.xml` to use the renderer symlink tree while `/chat` and authenticated routes retain SPA fallback. Reject arbitrary path capture and traversal.

- [ ] **Step 2: Run RED**

Run: `bash deploy/nginx/test-aiportcloud-conf.sh`  
Expected: FAIL because public-current routing is absent.

- [ ] **Step 3: Add exact locations and cache policy**

HTML uses short/no-cache validation; hashed assets remain immutable. API paths continue proxying to the Go service. Unknown public paths reach the Vue public 404 rather than redirecting to `/`.

- [ ] **Step 4: Run GREEN and Nginx syntax**

Run: `bash deploy/nginx/test-aiportcloud-conf.sh`  
Run in deployment fixture: `nginx -t`  
Expected: PASS.

- [ ] **Step 5: Commit in Porsche**

```bash
git add deploy/nginx
git commit -m "feat: serve rendered public pages"
```

### Task 4: Install and supervise the renderer

**Files (Porsche backend worktree):**
- Create: `deploy/public-renderer.service`
- Create: `deploy/public-renderer.env.example`
- Create: `deploy/install-public-renderer.sh`
- Create: `deploy/test-install-public-renderer.sh`
- Modify: `.env.example`

- [ ] **Step 1: Write RED shell behavior tests**

Use command shims in a disposable fixture. Require dedicated unprivileged user, read-only application sources, write access only to renderer-owned static directories, loopback API URL, no upstream API key, service restart policy, private environment metadata, and no mutation outside exact paths.

- [ ] **Step 2: Run RED**

Run: `bash deploy/test-install-public-renderer.sh`  
Expected: FAIL because installer/service are absent.

- [ ] **Step 3: Implement service and installer**

Run the Node worker continuously with a bounded poll interval and graceful shutdown. Keep secrets out of argv/logs. The backend render-job credential is a dedicated local-purpose secret merged through the existing example-key workflow.

- [ ] **Step 4: Run GREEN and syntax gates**

Run: `bash deploy/test-install-public-renderer.sh`  
Run: `bash -n deploy/install-public-renderer.sh deploy/test-install-public-renderer.sh`  
Expected: PASS.

- [ ] **Step 5: Commit in Porsche**

```bash
git add deploy/public-renderer* deploy/install-public-renderer.sh deploy/test-install-public-renderer.sh .env.example
git commit -m "feat: supervise public renderer"
```

### Task 5: Integrate renderer into full-stack restart and rollback

**Files (Porsche backend worktree):**
- Modify: `deploy/restart-all.sh`
- Modify: `deploy/test-restart-all.sh`
- Modify: `deploy/production-deploy.sh`
- Modify: `README.md`

- [ ] **Step 1: Extend failing deployment fixtures**

Require frontend build before traffic changes, renderer stage generation before symlink switch, Nginx validation before reload, service installation/restart, and restoration of backend container, frontend assets, public symlink, and renderer service state when any later step fails.

- [ ] **Step 2: Run RED**

Run: `bash deploy/test-restart-all.sh`  
Expected: FAIL on missing renderer lifecycle calls.

- [ ] **Step 3: Implement bounded orchestration**

Reuse existing lock and environment key merge. Snapshot the current public symlink target before changes. Do not roll back database migrations automatically. Print backend revision, frontend revision, public generation, and renderer service state on success.

- [ ] **Step 4: Run complete shell matrix**

```bash
bash deploy/test-restart-all.sh
bash deploy/test-production-deploy.sh
bash deploy/test-install-public-renderer.sh
bash deploy/nginx/test-aiportcloud-conf.sh
bash -n deploy/restart-all.sh deploy/production-deploy.sh deploy/install-public-renderer.sh
```

Expected: PASS, including injected renderer/render/symlink/Nginx failures.

- [ ] **Step 5: Commit in Porsche**

```bash
git add deploy README.md
git commit -m "feat: deploy public renderer safely"
```

### Task 6: Local cross-repository joint acceptance

**Files:**
- Create in Porsche: `docs/superpowers/reports/validation/2026-09-09-public-content-pricing/manifest.json`
- Create in Porsche-Web: `docs/agents/validation/2026-09-09-public-content-pricing/joint-acceptance.md`
- Modify in both repositories: `feature_list.json`, `progress.md`

- [ ] **Step 1: Start isolated dependencies and apply 0001-0012**

Use task-labelled MySQL 8 and Redis 7 with new credentials and no production data. Record versions, container IDs, database name, Redis DB, and migration ledger in private raw evidence; publish only sanitized hashes/summaries.

- [ ] **Step 2: Run backend and frontend full gates**

Run all commands from both implementation plans with explicit contract and fixture paths.  
Expected: zero fail and zero unexpected skip.

- [ ] **Step 3: Run browser and API scenarios P01/P03-P08**

Verify anonymous routes, slash upstream ID/stable key, exact prices/missing prices, authenticated-only redaction, draft/preview/concurrent publish/failure preservation/restore, hostile content, safe-draft truth gate, Root CRUD/missing detection/alerts, three-strike inactivation, and 375/768/1440 accessibility.

- [ ] **Step 4: Verify renderer agreement**

For each published test generation compare API headers, HTML generation markers, visible content, and sitemap.  
Expected: exact agreement within 60 seconds; failure injection preserves last-good content.

- [ ] **Step 5: Exact cleanup**

Remove only task-labelled containers, databases, Redis keys, temporary credentials, stage trees, test model configs, alerts, and drafts. Confirm unrelated resources are unchanged.

- [ ] **Step 6: Record bounded results**

Promote P01/P03-P08 only for demonstrated implementation scope. Keep real production content blocked until reviewed prices, terms, privacy, and brand text exist. Keep production deployment and rollback separately unclaimed.

- [ ] **Step 7: Commit reports in each repository**

```bash
git add feature_list.json progress.md docs/superpowers/reports/validation/2026-09-09-public-content-pricing
git commit -m "docs: record public pricing joint acceptance"
```

```bash
git add feature_list.json progress.md docs/agents/validation/2026-09-09-public-content-pricing
git commit -m "docs: record public pricing joint acceptance"
```

### Task 7: Production readiness package

**Files:**
- Create in Porsche: `docs/superpowers/reports/2026-09-09-public-content-pricing-release.md`
- Modify in Porsche: `deploy/restart-all.sh` only if acceptance found a reproducible deployment defect

- [ ] **Step 1: Prepare release checklist**

Record exact backend/frontend commits, migration 0012 checksum, required new environment keys, renderer user/directories/service, Nginx diff, safe draft state, rollback steps, and content-approval blockers.

- [ ] **Step 2: Run release preflight without deployment**

Build immutable backend/frontend candidates, validate configuration, migrations against an isolated database, renderer stage, Nginx syntax, and rollback fixture.  
Expected: PASS; no production traffic or data changes.

- [ ] **Step 3: Stop for explicit deployment authorization**

Deployment, production migration, first reviewed-content publication, and production rollback exercise require a separate concrete approval after the candidate revisions and checklist are reviewable.
