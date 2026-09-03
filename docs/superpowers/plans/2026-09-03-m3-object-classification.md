# M3 object Classification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox syntax.

**Goal:** Add closed object diagnostic categories without changing the SSE contract.

**Architecture:** Existing struct decoder and rejection order remain authoritative. Attach fixed decoded kind at object rejection; trace-aware SSE failure handling scans top-level object keys for shape and ambiguity. Copy/sanitize at diagnostic sink.

**Tech Stack:** Go 1.22 encoding/json, existing diagnostics Trace and whitelabel tests.

## Task 1: TDD implementation in existing isolated worktree

Files: internal/diagnostics/chunk.go and new object.go/object_test.go; internal/whitelabel/sse.go and new sse_object_diagnostics.go/sse_object_diagnostics_test.go. Do not modify persistence, handler, service, frontend or existing assertion contracts.

- [x] Run baseline `GOCACHE=/private/tmp/porsche-go-build-cache bash ./init.sh`, with no TEST_* or RUN_START_COMMAND set; record integration skips rather than claiming database verification.
- [x] Add real SSE+Trace RED test using `{"id":"safe","object":null,"choices":[{"delta":{}}]}` and assert `malformed_chunk_detail.object_detail` has decoded_kind=empty, field_shape=null, key_match=canonical; existing public result remains503/zero frames. Record actual missing-field failure before implementing.
- [x] Introduce `ObjectDetail` with fixed JSON fields decoded_kind/field_shape/key_match, and optional `Object *ObjectDetail` in ChunkFailure tagged object_detail,omitempty. Keep old MalformedChunk(reason,field) calls supported; permit optional copied ObjectDetail only for invalid_value/object, normalize each vocabulary independently. Existing nil trace and mutex semantics remain.
- [x] At the original object rejection classify only decoded string: `"" => empty`, `"chat.completion" => known_chat_completion`, everything else=>other. Do not store the string in failure. Other rejection branches leave Object nil.
- [x] In trace-aware SSE failure handling, enrich only invalid_value/object using a json.Decoder over payload. Iterate top-level keys, decode each value as RawMessage, compare JSON-decoded key with strings.EqualFold(key,"object"), count matches. Return missing/none for0, shape/canonical-or-case_variant for1, ambiguous/multiple for>1. On scan failure return unknown/unknown; preserve decoded_kind. Call sink with copied categories, never raw payload/error.
- [x] Add RED/GREEN cases for vocab sanitizing, pointer isolation/nil trace, successful and unrelated-failure omission, repeated keys/null, escaped/case keys, nested keys, decoder type error and ID priority. Use SENSITIVE sentinel in values/names/nesting; logs must not contain sentinel or arbitrary upstream data.
- [x] Run `GOPROXY=off GOSUMDB=off GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/diagnostics ./internal/whitelabel -count=1`; freeze changed code for review. No generated traffic or deployment.

## Task 2: Independent verification and release preparation

- [x] PM/spec review of final diff against design, fix findings before quality review.
- [x] Quality review plus `go test -race ./internal/diagnostics ./internal/whitelabel -count=1`, default `go test ./... -count=1`, `go vet ./...`, `go build ./...`, `git diff --check`; record precise fixture skips.
- [x] Compare actual public outcomes/bytes with6e70784 using the25 archived investigation cases and existing accepted chunk compatibility cases; copied synthetic fixture is evidence only, no parser reimplementation.
- [x] Commit exact code after reviews. Build binaries from independent exact clone with CGO_ENABLED=0 GOOS=linux GOARCH=amd64 and -trimpath -buildvcs=true; record revision/modified=false and binary SHA256.
- [x] Build offline linux/amd64 runtime image with approved cached base, verify binary hashes/CA and expected missing-key isolated startup, export canonical image and archive SHA256. Do not upload or deploy yet.
- [x] Prepare exact release sheet/script bound to current13ada4aa/cb42 and new candidate, preserving three locks, source/public200, frontend158a00e hash and old containers; run existing7 behavior tests and binding assertions. Prepare fourth-attempt harness bounded to ledger maximum4 and exactly3 existing attempts.
- [x] Update report, feature/progress/handoff and both-repo manifests; obtain PM/quality written scope.
- [ ] Obtain final user exact-candidate deployment approval. Remaining1 request is not spent before readiness.
