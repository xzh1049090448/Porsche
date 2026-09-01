# Root Bootstrap Secret Isolation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create the first production Root from a root-only mounted credential file, then guarantee that the long-running application container never receives the one-time Root password.

**Architecture:** A dedicated `bootstrap-root` binary loads ordinary production configuration from a mounted `.env`, parses the separately mounted credential file, validates it with the same Root rules as startup configuration, and reuses `AuthService.BootstrapRoot`. A root-only shell wrapper runs that binary in a disposable container, while candidate deployment fails before writes if `.env` still contains Root bootstrap values.

**Tech Stack:** Go 1.22, GORM/MySQL 8, Bash with `set -Eeuo pipefail`, Docker, and the existing isolated `bash:5.2` shell fixture.

---

## File map

- Modify `internal/config/config.go` and `internal/config/config_test.go`: expose and test one canonical Root credential validator.
- Create `cmd/bootstrap-root/main.go` and `cmd/bootstrap-root/main_test.go`: strict file parsing and one-shot database bootstrap.
- Modify `Dockerfile`: include the dedicated binary in the runtime image.
- Create `deploy/auth-acceptance-bootstrap-root.sh`: root-only disposable bootstrap entry point.
- Modify `deploy/auth-acceptance-deploy.sh`: reject Root secrets before any deployment write.
- Modify `deploy/test-auth-acceptance-deploy.sh`: isolated behavioral and secret-absence assertions.
- Modify `README.md`, `progress.md`, and `feature_list.json`: operator flow and verified evidence.

### Task 1: Canonical Root credential validation

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing validator test**

Add this table to `internal/config/config_test.go`:

```go
func TestValidateRootBootstrapCredentials(t *testing.T) {
	tests := []struct {
		name, username, password string
		wantErr                  bool
	}{
		{name: "valid", username: "root_admin", password: "Aa1@0123456789ab"},
		{name: "invalid username", username: "root admin", password: "Aa1@0123456789ab", wantErr: true},
		{name: "too short", username: "root_admin", password: "Aa1@short", wantErr: true},
		{name: "missing uppercase", username: "root_admin", password: "aa1@0123456789ab", wantErr: true},
		{name: "development default", username: "root_admin", password: "change-me-root-bootstrap-password-for-dev-only", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRootBootstrapCredentials(tt.username, tt.password)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateRootBootstrapCredentials() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
```

- [ ] **Step 2: Verify RED**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/config -run TestValidateRootBootstrapCredentials -count=1`

Expected: compile failure because `ValidateRootBootstrapCredentials` is undefined.

- [ ] **Step 3: Implement and reuse the validator**

Add to `internal/config/config.go`:

```go
func ValidateRootBootstrapCredentials(username, password string) error {
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	if !validRootBootstrapUsername(username) || !strongRootBootstrapPassword(password) {
		return fmt.Errorf("ROOT_BOOTSTRAP credentials are invalid")
	}
	if isDefaultAuthSecret(password, "change-me-root-bootstrap-password-for-dev-only") {
		return fmt.Errorf("ROOT_BOOTSTRAP_PASSWORD must not use the development default in production")
	}
	return nil
}
```

Keep the existing paired-empty check, then replace the duplicate non-empty checks in `validateProductionAuthSettings` with:

```go
if s.RootBootstrapUsername != "" {
	if err := ValidateRootBootstrapCredentials(s.RootBootstrapUsername, s.RootBootstrapPassword); err != nil {
		return err
	}
}
```

- [ ] **Step 4: Verify GREEN**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/config -count=1`

Expected: PASS, including all existing production configuration cases.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "refactor: centralize root bootstrap validation"
```

### Task 2: Dedicated one-shot Root bootstrap binary

**Files:**
- Create: `cmd/bootstrap-root/main.go`
- Create: `cmd/bootstrap-root/main_test.go`
- Modify: `Dockerfile`

- [ ] **Step 1: Write strict parser and CLI tests**

Create `cmd/bootstrap-root/main_test.go`:

```go
func TestParseCredentialsRequiresExactlyOneUsernameAndPassword(t *testing.T) {
	tests := []struct {
		name, input string
		want        credentials
		wantErr     bool
	}{
		{name: "valid", input: "username=root_admin\npassword=Aa1@0123456789ab\n", want: credentials{Username: "root_admin", Password: "Aa1@0123456789ab"}},
		{name: "duplicate", input: "username=a\nusername=b\npassword=Aa1@0123456789ab\n", wantErr: true},
		{name: "unknown key", input: "username=root_admin\ntoken=secret\npassword=Aa1@0123456789ab\n", wantErr: true},
		{name: "missing password", input: "username=root_admin\n", wantErr: true},
		{name: "malformed", input: "username root_admin\npassword=Aa1@0123456789ab\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCredentials(strings.NewReader(tt.input))
			if (err != nil) != tt.wantErr || (!tt.wantErr && got != tt.want) {
				t.Fatalf("parseCredentials() = %#v, %v", got, err)
			}
			if err != nil && strings.Contains(err.Error(), "Aa1@") {
				t.Fatal("credential parser disclosed input")
			}
		})
	}
}

func TestParseArgsRequiresCredentialsFile(t *testing.T) {
	if _, err := parseArgs([]string{"bootstrap-root"}); err == nil {
		t.Fatal("accepted missing credentials file")
	}
	got, err := parseArgs([]string{"bootstrap-root", "--credentials-file", "/run/secrets/root-bootstrap"})
	if err != nil || got != "/run/secrets/root-bootstrap" {
		t.Fatalf("parseArgs() = %q, %v", got, err)
	}
}
```

- [ ] **Step 2: Verify RED**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./cmd/bootstrap-root -count=1`

Expected: compile failure because the parser types and functions do not exist.

- [ ] **Step 3: Implement the parser**

Create `cmd/bootstrap-root/main.go` with `credentials`, `parseArgs`, and a parser
that first calls `io.ReadAll(io.LimitReader(r, 4097))` and rejects a result
longer than 4096 bytes. Scan the bounded byte slice, split each line once on
`=`, accept only `username` and `password`, and reject duplicates,
unknown/malformed lines, empty values, surrounding whitespace, and scanner
errors. Every parser failure returns only `invalid Root credentials file`.

The signatures are:

```go
type credentials struct { Username, Password string }
func parseArgs(args []string) (string, error)
func parseCredentials(r io.Reader) (credentials, error)
func run(args []string) error
```

- [ ] **Step 4: Implement the one-shot database flow**

`run` must perform these exact operations in order:

```go
path, err := parseArgs(args)
settings, err := config.Load()
file, err := os.Open(path)
credential, err := parseCredentials(file)
err = config.ValidateRootBootstrapCredentials(credential.Username, credential.Password)
gdb, err := db.Open(settings.DatabaseURL, settings.AppEnv)
settings.RootBootstrapUsername = credential.Username
settings.RootBootstrapPassword = credential.Password
created, err := service.NewAuthService(settings, nil, gdb).BootstrapRoot(context.Background())
err = gdb.Model(&models.User{}).Where("role = ?", models.UserRoleRoot).Count(&count).Error
```

Require `count == 1`. Print only `Root bootstrap created` when `created != nil`, otherwise `Root bootstrap already consumed`. `main()` calls `log.Fatal(run(os.Args))`. Returned errors must not contain credentials or `DATABASE_URL`.

- [ ] **Step 5: Build the binary into the runtime image**

Change `Dockerfile` to build both binaries and copy both into `/app`:

```dockerfile
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/server ./cmd/server && \
    CGO_ENABLED=0 GOOS=linux go build -o /out/bootstrap-root ./cmd/bootstrap-root
COPY --from=builder /out/server .
COPY --from=builder /out/bootstrap-root .
```

- [ ] **Step 6: Verify GREEN and commit**

```bash
GOCACHE=/private/tmp/porsche-go-build-cache go test ./cmd/bootstrap-root ./internal/config -count=1
git diff --check
git add cmd/bootstrap-root Dockerfile
git commit -m "feat: add one-shot root bootstrap command"
```

Expected: tests PASS and diff check is silent.

### Task 3: Isolated operator wrapper

**Files:**
- Create: `deploy/auth-acceptance-bootstrap-root.sh`
- Modify: `deploy/test-auth-acceptance-deploy.sh`

- [ ] **Step 1: Add the RED fixture contract**

Add `root-bootstrap` to the selected checks, map it to `auth-acceptance-bootstrap-root.sh`, create a mode-`0600` fixture credential file, and add:

```bash
run_root_bootstrap() {
    run_entrypoint auth-acceptance-bootstrap-root.sh --confirm-auth-root-bootstrap
}

assert_root_bootstrap_uses_mounted_secret_only() {
    run_root_bootstrap
    require_call docker build --tag ai-gateway-go:auth-acceptance /fixture/Porsche
    require_call docker container inspect porsche-mysql
    require_call docker run --rm --network porsche-app
    forbid_logged_text 'Aa1@fixture-secret'
    forbid_logged_text 'ROOT_BOOTSTRAP_PASSWORD'
    assert_no_dangerous_calls
}
```

Also reject missing/wrong confirmation, non-`0600` credentials, symlinks, malformed files, dirty/wrong branches, remote mismatch, and non-empty Root keys in `.env`, with `assert_no_docker_or_rsync_writes` after each rejection. `forbid_logged_text` must decode the existing NUL argv records and compare fields without printing the sought secret.

- [ ] **Step 2: Verify RED**

Run: `bash deploy/test-auth-acceptance-deploy.sh root-bootstrap`

Expected: failure naming the missing fixture entry point.

- [ ] **Step 3: Implement the wrapper**

Create `deploy/auth-acceptance-bootstrap-root.sh` with fixed production constants:

```bash
BACKEND_DIR=/opt/Porsche
CREDENTIALS_FILE=/var/lib/porsche-auth-acceptance/root-acceptance-credentials
NETWORK=porsche-app
MYSQL_CONTAINER=porsche-mysql
IMAGE_NAME=ai-gateway-go:auth-acceptance
BACKEND_BRANCH=feature/user-registration-management
```

Require root and exactly `--confirm-auth-root-bootstrap`. Reuse the existing clean checkout, fetched remote SHA, and `.env` readers. Before building, require a regular non-symlink credentials file with numeric owner `0`, mode `600`, and exactly one `username=` plus one `password=` line and no other lines. Require both `.env` Root values empty, and inspect `porsche-app` and `porsche-mysql`.

Run only:

```bash
docker build --tag "$IMAGE_NAME" "$BACKEND_DIR"
docker run --rm --network "$NETWORK" \
  --mount "type=bind,src=$BACKEND_DIR/.env,dst=/app/.env,readonly" \
  --mount "type=bind,src=$CREDENTIALS_FILE,dst=/run/secrets/root-bootstrap,readonly" \
  "$IMAGE_NAME" /app/bootstrap-root \
  --credentials-file /run/secrets/root-bootstrap
```

Do not use `--env`, `--env-file`, `source`, `eval`, or expand credential values in shell.

- [ ] **Step 4: Verify GREEN and commit**

```bash
bash -n deploy/auth-acceptance-bootstrap-root.sh deploy/test-auth-acceptance-deploy.sh
bash deploy/test-auth-acceptance-deploy.sh root-bootstrap
git add deploy/auth-acceptance-bootstrap-root.sh deploy/test-auth-acceptance-deploy.sh
git commit -m "feat: bootstrap root from mounted secret"
```

Expected: `PASS: auth acceptance deployment regression checks (root-bootstrap)`.

### Task 4: Keep Root credentials out of candidate containers

**Files:**
- Modify: `deploy/auth-acceptance-deploy.sh`
- Modify: `deploy/test-auth-acceptance-deploy.sh`

- [ ] **Step 1: Add the RED preflight test**

For each Root key, append a non-empty value to the fixture `.env`, run deployment, and assert zero Docker build/run/stop and zero rsync calls:

```bash
assert_deploy_rejects_root_bootstrap_environment_without_writes() {
    local clean_env="$backend_dir/.env.clean"
    cp "$backend_dir/.env" "$clean_env"
    printf 'ROOT_BOOTSTRAP_USERNAME=root_admin\n' >>"$backend_dir/.env"
    if run_deploy; then fail 'deployment accepted Root bootstrap username'; fi
    assert_no_docker_or_rsync_writes
    cp "$clean_env" "$backend_dir/.env"
    printf 'ROOT_BOOTSTRAP_PASSWORD=Aa1@fixture-secret\n' >>"$backend_dir/.env"
    if run_deploy; then fail 'deployment accepted Root bootstrap password'; fi
    assert_no_docker_or_rsync_writes
    mv "$clean_env" "$backend_dir/.env"
}
```

- [ ] **Step 2: Verify RED**

Run: `bash deploy/test-auth-acceptance-deploy.sh deploy`

Expected: failure stating deployment accepted a Root bootstrap value.

- [ ] **Step 3: Add the deployment guard**

Before builds or Docker writes in `deploy/auth-acceptance-deploy.sh`, add:

```bash
[[ -z "$(read_env_value ROOT_BOOTSTRAP_USERNAME)" ]] || {
    echo 'ROOT_BOOTSTRAP_USERNAME must be empty before deployment' >&2
    exit 1
}
[[ -z "$(read_env_value ROOT_BOOTSTRAP_PASSWORD)" ]] || {
    echo 'ROOT_BOOTSTRAP_PASSWORD must be empty before deployment' >&2
    exit 1
}
```

- [ ] **Step 4: Verify GREEN and commit**

```bash
bash deploy/test-auth-acceptance-deploy.sh deploy
bash deploy/test-auth-acceptance-deploy.sh
git add deploy/auth-acceptance-deploy.sh deploy/test-auth-acceptance-deploy.sh
git commit -m "fix: reject root secrets in application deployment"
```

Expected: both fixtures PASS.

### Task 5: Documentation, evidence, and full verification

**Files:**
- Modify: `README.md`
- Modify: `progress.md`
- Modify: `feature_list.json`
- Modify: `deploy/test-auth-acceptance-deploy.sh`

- [ ] **Step 1: Add RED documentation assertions**

Require these literal strings:

```text
sudo bash deploy/auth-acceptance-bootstrap-root.sh --confirm-auth-root-bootstrap
ROOT_BOOTSTRAP_USERNAME and ROOT_BOOTSTRAP_PASSWORD must be empty before deployment
docker inspect of the long-running application must not contain ROOT_BOOTSTRAP_
```

Run: `bash deploy/test-auth-acceptance-deploy.sh docs`

Expected: failure naming the first missing instruction.

- [ ] **Step 2: Document the safe flow**

Update `README.md` with this order: migration `0002`; root-owned `0600` credential file; remove/empty both Root keys in `.env`; explicit one-shot bootstrap; verify Root count one and transient container gone; candidate deploy; browser acceptance; secure credential transfer or password rotation followed by credential-file deletion. Include no real credential and no command that prints the file.

- [ ] **Step 3: Record honest project evidence**

Update `progress.md` and only the `go-006` evidence array in `feature_list.json`. Keep `go-006` as the sole `in_progress` feature and state that real one-shot bootstrap, candidate deployment, and browser acceptance remain pending.

- [ ] **Step 4: Run complete verification**

```bash
bash -n deploy/bootstrap-auth-redis.sh deploy/auth-acceptance-migrate.sh \
  deploy/auth-acceptance-bootstrap-root.sh deploy/auth-acceptance-deploy.sh \
  deploy/auth-acceptance-rollback.sh deploy/test-auth-acceptance-deploy.sh
bash deploy/test-auth-acceptance-deploy.sh
bash deploy/test-production-deploy.sh
bash deploy/test-restart-all.sh
bash deploy/nginx/test-aiportcloud-conf.sh
GOCACHE=/private/tmp/porsche-go-build-cache go test ./... -count=1
GOCACHE=/private/tmp/porsche-go-build-cache go vet ./...
jq empty feature_list.json
git diff --check
```

Expected: all shell fixtures and Go packages PASS; vet, JSON parsing, and diff checks are silent.

- [ ] **Step 5: Commit and inspect the branch**

```bash
git add README.md progress.md feature_list.json deploy/test-auth-acceptance-deploy.sh
git commit -m "docs: describe isolated root bootstrap"
git status --short --branch
git log --oneline origin/feature/user-registration-management..HEAD
```

Expected: clean tracked state and only the reviewed design, plan, implementation, tests, and documentation commits ahead of the remote target branch.
