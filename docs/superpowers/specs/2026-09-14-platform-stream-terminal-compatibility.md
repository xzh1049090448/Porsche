# Platform Stream Terminal Compatibility

## Problem

The platform v2 single and compare runners reject a streamed completion after all visible text has arrived. The client consequently receives `model_error` and global `error` and displays “生成失败”.

The upstream documentation states that `stream: true` terminates with `data: [DONE]`, and that `stream_options.include_usage: true` returns usage before that terminal marker. A redacted production-side probe against `baidu/ernie-4.5-300b-a47b-paddle` on 2026-09-14 returned HTTP 200 and this structure:

1. a normal choice chunk;
2. one final chunk containing both a non-null `finish_reason`, one choice, and a usage object;
3. `data: [DONE]`.

The probe emitted no credential, prompt, generated text, identifier, or raw upstream frame. The upstream therefore provides all information required for safe persistence, but combines the final choice and usage instead of emitting the documented standalone `choices: []` usage chunk.

## Root Cause

`platformSingleChunkState.accept` checks `chunk.Usage` first and rejects every usage-bearing chunk whose `choices` array is non-empty. The shared state machine is used by both single-model and compare-model v2 generation, so both paths reject the observed upstream terminal shape.

## Approved Behavior

Accept exactly two usage shapes:

- standalone usage: `usage != nil` and `choices` is empty;
- combined terminal usage: `usage != nil`, exactly one choice at index zero, and that choice has a non-null `finish_reason`.

For the combined shape, process the choice and usage atomically. Preserve the existing content, refusal, tool-call, index, duplicate-usage, token-range, post-finish, UTF-8, size, sequence, persistence, and quota checks.

Continue rejecting:

- usage combined with a non-terminal choice;
- multiple choices or a non-zero choice index;
- duplicate usage;
- choices after a previously observed finish;
- missing usage, missing finish, missing content, malformed chunks, read failures, early EOF, or missing `[DONE]`;
- refusal and tool-call deltas in this text-only platform path.

## Scope

Backend only. No public SSE event names or payloads change. No database schema, migration, frontend, deployment, or contract-version change is required.

## Verification

- A regression test must reproduce the observed combined terminal chunk and fail before implementation.
- Standalone usage must remain accepted.
- Combined non-terminal usage and duplicate usage must be rejected.
- Single and compare runner tests must pass because they share the state machine.
- Run `go test ./internal/service ./internal/whitelabel -count=1`, then `go test ./... -count=1`, `go vet ./...`, and `go build ./...` in an environment that permits loopback listeners.
