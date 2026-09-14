# Platform Stream Terminal Compatibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Accept an upstream final SSE chunk that contains both the terminal choice and usage without weakening the existing completion, persistence, or `[DONE]` requirements.

**Architecture:** Keep the compatibility rule inside the shared `platformSingleChunkState` used by single and compare generation. Validate the complete chunk before mutating state, then record usage and terminal choice together. Public platform SSE and durable persistence remain unchanged.

**Tech Stack:** Go 1.22, `testing`, existing Porsche platform v2 runners and white-label SSE projection.

---

### Task 1: Lock the observed upstream terminal shape with tests

**Files:**
- Modify: `internal/service/platform_single_generation_test.go`
- Test: `internal/service/platform_single_generation_test.go`

- [ ] **Step 1: Write the failing combined-terminal regression test**

Add a focused test that calls the real state machine:

```go
func TestPlatformSingleChunkStateAcceptsCombinedTerminalUsage(t *testing.T) {
	state := platformSingleChunkState{}
	content := "answer"
	finish := "stop"
	delta, err := state.accept(whitelabel.ChatCompletionChunk{
		Choices: []whitelabel.ChatCompletionChunkChoice{{
			Index: 0,
			Delta: whitelabel.ChatCompletionChunkDelta{Content: &content},
			FinishReason: &finish,
		}},
		Usage: &whitelabel.ChatCompletionUsage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3},
	})
	if err != nil || delta != content || !state.modelEnded || !state.usageSeen || state.totalTokens != 3 {
		t.Fatalf("delta=%q state=%+v err=%v", delta, state, err)
	}
}
```

- [ ] **Step 2: Add narrow rejection tests**

Add table cases proving that usage combined with a nil `finish_reason`, a duplicate usage object, multiple choices, or non-zero index remains rejected. Assert `ErrPlatformSingleGenerationUpstream` and that invalid combined input does not partially set `usageSeen` or `modelEnded`.

- [ ] **Step 3: Run tests and verify RED**

Run:

```bash
GOCACHE=/private/tmp/porsche-terminal-tdd-cache go test ./internal/service -run '^TestPlatformSingleChunkState' -count=1
```

Expected: the combined-terminal acceptance test fails with `platform single generation upstream failure`; rejection cases that describe existing behavior pass.

- [ ] **Step 4: Commit the regression tests**

```bash
git add internal/service/platform_single_generation_test.go docs/superpowers/specs/2026-09-14-platform-stream-terminal-compatibility.md docs/superpowers/plans/2026-09-14-platform-stream-terminal-compatibility.md
git commit -m "test: reproduce combined terminal usage chunk"
```

### Task 2: Accept combined terminal usage atomically

**Files:**
- Modify: `internal/service/platform_single_generation.go:848-883`
- Test: `internal/service/platform_single_generation_test.go`

- [ ] **Step 1: Restructure validation without broadening completion**

Update `platformSingleChunkState.accept` so usage validity is checked first, but usage is committed only after the choice shape is validated. Preserve standalone usage:

```go
func (s *platformSingleChunkState) accept(chunk whitelabel.ChatCompletionChunk) (string, error) {
	hasUsage := chunk.Usage != nil
	if hasUsage && (s.usageSeen || chunk.Usage.TotalTokens < 0 || chunk.Usage.TotalTokens > math.MaxInt32) {
		return "", ErrPlatformSingleGenerationUpstream
	}
	if len(chunk.Choices) == 0 {
		if !hasUsage {
			return "", ErrPlatformSingleGenerationUpstream
		}
		s.usageSeen = true
		s.totalTokens = int64(chunk.Usage.TotalTokens)
		return "", nil
	}
	if len(chunk.Choices) != 1 || chunk.Choices[0].Index != 0 || s.modelEnded {
		return "", ErrPlatformSingleGenerationUpstream
	}
	choice := chunk.Choices[0]
	if choice.Delta.Refusal != nil || len(choice.Delta.ToolCalls) != 0 || (hasUsage && choice.FinishReason == nil) {
		return "", ErrPlatformSingleGenerationUpstream
	}
	delta := ""
	if choice.Delta.Content != nil {
		delta = *choice.Delta.Content
	}
	if choice.FinishReason != nil {
		s.modelEnded = true
	}
	if hasUsage {
		s.usageSeen = true
		s.totalTokens = int64(chunk.Usage.TotalTokens)
	}
	return delta, nil
}
```

- [ ] **Step 2: Run focused tests and verify GREEN**

Run:

```bash
GOCACHE=/private/tmp/porsche-terminal-tdd-cache go test ./internal/service -run '^TestPlatformSingleChunkState' -count=1
```

Expected: PASS.

- [ ] **Step 3: Run single and compare runner regressions**

Run:

```bash
GOCACHE=/private/tmp/porsche-terminal-tdd-cache go test ./internal/service -run 'Platform(Single|Compare)Generation' -count=1
```

Expected: PASS with no changed public failure codes or event ordering.

- [ ] **Step 4: Commit the minimal fix**

```bash
git add internal/service/platform_single_generation.go internal/service/platform_single_generation_test.go
git commit -m "fix: accept combined terminal usage chunks"
```

### Task 3: Verify repository-wide compatibility

**Files:**
- Verify: `internal/service/platform_single_generation.go`
- Verify: `internal/service/platform_single_generation_test.go`

- [ ] **Step 1: Format and inspect the diff**

Run:

```bash
gofmt -w internal/service/platform_single_generation.go internal/service/platform_single_generation_test.go
git diff --check
```

Expected: no output from `git diff --check`.

- [ ] **Step 2: Run focused packages**

Run:

```bash
GOCACHE=/private/tmp/porsche-terminal-verify-cache go test ./internal/service ./internal/whitelabel -count=1
```

Expected: both packages PASS.

- [ ] **Step 3: Run full repository verification**

Run in an environment that permits loopback test listeners:

```bash
GOCACHE=/private/tmp/porsche-terminal-verify-cache go test ./... -count=1
go vet ./...
go build ./...
```

Expected: all commands exit zero. Database-backed tests may only be reported as accepted if their fixtures are configured and they actually run.

- [ ] **Step 4: Review final scope**

Confirm the diff contains only the design/plan, focused tests, and the shared state-machine change. Confirm no frontend, migration, deployment, public contract, credential, prompt, generated content, or raw upstream frame is included.
