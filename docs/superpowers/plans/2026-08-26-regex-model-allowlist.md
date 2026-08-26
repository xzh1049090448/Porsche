# Regex Model Allowlist Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let `JIEKOU_ALLOWED_MODELS` contain explicit `re:` RE2 patterns such as `re:^.+$`, while retaining exact user and Gateway Token model ACLs.

**Architecture:** Parse global exact IDs and compiled RE2 patterns at startup. The white-label service copies that immutable configuration, verifies exact IDs through the current safe model-ID validator, and authorizes a model when it matches either a configured exact ID or a compiled global pattern; caller ACLs remain an exact second gate. No database schema, migration, endpoint, or frontend change is needed.

**Tech Stack:** Go 1.22, Go `regexp` (RE2), existing config loader, white-label service, Go test.

---

## Files and responsibilities

- Modify: `internal/config/config.go` — parse `re:` configuration entries and expose immutable compiled patterns alongside exact allowed IDs.
- Modify: `internal/config/config_test.go` — configuration startup/fail-closed matcher tests.
- Modify: `internal/whitelabel/service.go` — copy global exact/pattern allowlist and apply it before exact caller ACL checking.
- Modify: `internal/whitelabel/service_test.go` — catalog, detail, Chat authorization, and ACL intersection regressions.
- Modify: `.env.example`, `README.md` — document exact IDs and the explicit `re:` syntax without an actual secret.
- Modify: `feature_list.json`, `progress.md` only after verification — record verified behavior; do not alter unrelated active task evidence.

### Task 1: Add fail-closed global regex configuration

**Files:**
- Modify: `internal/config/config.go:49-86`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write failing configuration tests**

Add tests that define the intended contract:

```go
func TestParseWhiteLabelSettingsSupportsExactAndRegexModels(t *testing.T) {
	settings, err := ParseWhiteLabelSettings("cn", "test-key", "model-a,re:^zai-org/.+$,re:^.+$")
	if err != nil { t.Fatal(err) }
	if !settings.Allows("model-a") || !settings.Allows("zai-org/glm-5.1") || !settings.Allows("other/model") {
		t.Fatal("expected exact and regex matches")
	}
}

func TestParseWhiteLabelSettingsRejectsInvalidRegex(t *testing.T) {
	_, err := ParseWhiteLabelSettings("cn", "test-key", "re:[")
	if err == nil || strings.Contains(err.Error(), "test-key") { t.Fatalf("want sanitized config error, got %v", err) }
}

func TestParseWhiteLabelSettingsRejectsEmptyRegex(t *testing.T) {
	_, err := ParseWhiteLabelSettings("cn", "test-key", "re:")
	if err == nil { t.Fatal("want error") }
}
```

Also assert a list containing only valid regex patterns is nonempty configuration, and normal non-`re:` values remain exact.

- [ ] **Step 2: Run focused tests and confirm RED**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/config -run 'TestParseWhiteLabelSettings' -count=1`

Expected: FAIL because `re:^.+$` is currently stored as an exact literal and invalid regex does not fail startup.

- [ ] **Step 3: Extend settings with compiled patterns and parse them**

Define the immutable configuration shape:

```go
type WhiteLabelSettings struct {
	Region               string
	BaseURL              string
	APIKey               string
	AllowedModels        map[string]struct{}
	AllowedModelPatterns []*regexp.Regexp
}

func (s WhiteLabelSettings) Allows(model string) bool {
	if _, ok := s.AllowedModels[model]; ok { return true }
	for _, pattern := range s.AllowedModelPatterns {
		if pattern.MatchString(model) { return true }
	}
	return false
}
```

In `ParseWhiteLabelSettings`, trim each comma-separated entry. For `strings.HasPrefix(entry, "re:")`, require a nonempty suffix and compile with `regexp.Compile`; on any compile error return a generic error such as `fmt.Errorf("JIEKOU_ALLOWED_MODELS contains an invalid regular expression")`. For other entries, put the exact unchanged entry into `AllowedModels`. Count both exact and pattern entries when enforcing the nonempty fail-closed rule.

- [ ] **Step 4: Run focused config tests**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/config -count=1`

Expected: PASS.

- [ ] **Step 5: Commit configuration parsing**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: parse regex model allowlist entries"
```

### Task 2: Apply patterns only at the global allowlist boundary

**Files:**
- Modify: `internal/whitelabel/service.go:24-76,209-231`
- Modify: `internal/whitelabel/service_test.go`

- [ ] **Step 1: Write failing authorization tests**

Create a white-label service with `AllowedModelPatterns` set to `regexp.MustCompile("^zai-org/.+$")` and an upstream catalog containing `zai-org/glm-5.1` and `other/model`.

```go
func TestPatternAllowlistFiltersCatalogButKeepsCallerACLExact(t *testing.T) {
	// Empty ACL sees zai-org/glm-5.1 but not other/model.
	// ACL []string{"other/model"} sees neither: global pattern blocks other/model and ACL blocks zai-org/glm-5.1.
	// ACL []string{"zai-org/glm-5.1"} sees exactly zai-org/glm-5.1.
}

func TestPatternAllowedSlashModelUsesEscapedDetailPath(t *testing.T) {
	// GetModel with ACL []string{"zai-org/glm-5.1"} succeeds and upstream sees /models/zai-org%2Fglm-5.1.
}

func TestCallerACLRegexTextIsNotAnAuthorizationPattern(t *testing.T) {
	// ACL []string{"re:^.+$"} must not authorize zai-org/glm-5.1 and must make zero upstream calls.
}
```

Use separate `httptest` services/counters so the denied ACL assertion proves zero total upstream requests.

- [ ] **Step 2: Run focused tests and confirm RED**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/whitelabel -run 'Test(PatternAllowlist|PatternAllowed|CallerACLRegex)' -count=1`

Expected: FAIL because `NewWhiteLabelService` currently validates every configured global entry as a literal model ID and only keeps an exact map.

- [ ] **Step 3: Copy configured patterns and preserve exact ACL semantics**

Change the service state to keep exact IDs and compiled patterns separately:

```go
type WhiteLabelService struct {
	baseURL         string
	apiKey          string
	allowedExact    map[string]struct{}
	allowedPatterns []*regexp.Regexp
	// existing client/cache fields remain unchanged
}

func (s *WhiteLabelService) globallyAllows(id string) bool {
	if _, ok := s.allowedExact[id]; ok { return true }
	for _, pattern := range s.allowedPatterns {
		if pattern.MatchString(id) { return true }
	}
	return false
}

func (s *WhiteLabelService) permitted(id string, acl []string) bool {
	if !s.globallyAllows(id) { return false }
	if len(acl) == 0 { return true }
	for _, candidate := range acl {
		if candidate == id { return true }
	}
	return false
}
```

In `NewWhiteLabelService`, validate only `settings.AllowedModels` with `validModelID`; copy `settings.AllowedModelPatterns` into a new slice after rejecting nil patterns. Do not compile regexes in the service, and do not interpret any caller ACL entry as a pattern.

- [ ] **Step 4: Verify service, race, and full backend suites**

Run:

```bash
GOCACHE=/private/tmp/porsche-go-build-cache go test -race ./internal/whitelabel -count=1
GOCACHE=/private/tmp/porsche-go-build-cache go test ./... -count=1
GOCACHE=/private/tmp/porsche-go-build-cache go vet ./...
git diff --check
```

Expected: all commands pass. If local sandbox blocks `httptest` loopback listeners, rerun the same Go commands in an approved local-loopback environment and record the actual result.

- [ ] **Step 5: Commit service authorization**

```bash
git add internal/whitelabel/service.go internal/whitelabel/service_test.go
git commit -m "feat: apply regex global model allowlist"
```

### Task 3: Document configuration and record validation

**Files:**
- Modify: `.env.example:19-22`
- Modify: `README.md:30-39`
- Modify: `feature_list.json`
- Modify: `progress.md`

- [ ] **Step 1: Add documentation assertions or review checklist**

Add a config test or documentation review checklist confirming `.env.example` presents both exact and global-all forms without a real API key:

```dotenv
# Exact IDs, comma separated:
JIEKOU_ALLOWED_MODELS=zai-org/glm-5.1,deepseek/deepseek-v4-pro
# Or one or more explicit RE2 patterns; this allows every safe upstream model:
# JIEKOU_ALLOWED_MODELS=re:^.+$
```

- [ ] **Step 2: Update docs**

State that `re:` is supported only for the global `.env` allowlist; user and Gateway Token ACL values remain exact IDs. State that invalid regex causes startup failure and that `re:^.+$` automatically exposes any safe future upstream catalog model to principals without restrictive ACLs.

- [ ] **Step 3: Run final verification**

Run backend checks from the backend worktree and ensure neither source nor docs contain a real key:

```bash
GOCACHE=/private/tmp/porsche-go-build-cache go test ./... -count=1
GOCACHE=/private/tmp/porsche-go-build-cache go vet ./...
git diff --check
rg -n 'JIEKOU_API_KEY=.*(sk-|key-[A-Za-z0-9])' .env.example README.md docs || true
```

Expected: tests/vet/diff pass; the scan finds no real secret.

- [ ] **Step 4: Record only verified tracker evidence**

Add a concise `go-004` evidence entry for regex allowlist unit/full verification. Do not claim real upstream smoke unless executed. Preserve `go-004` as `in_progress` while Issue #2 still lacks deployed MySQL and upstream smoke evidence.

- [ ] **Step 5: Commit docs and tracker evidence**

```bash
git add .env.example README.md feature_list.json progress.md
git commit -m "docs: document regex model allowlists"
```

## Plan self-review

- **Spec coverage:** Task 1 provides explicit `re:` parsing and fail-closed startup; Task 2 keeps caller ACLs exact while applying patterns consistently to catalog/detail/Chat gates; Task 3 documents safe deployment syntax and records only actual verification.
- **Security:** patterns are compiled once at startup by Go RE2; user/Token ACL never parse regex; unsafe IDs are filtered before matching or upstream requests; errors avoid API-key interpolation.
- **Scope:** no database migration, model mapping, route, frontend, or changes to existing exact ACL formats.
