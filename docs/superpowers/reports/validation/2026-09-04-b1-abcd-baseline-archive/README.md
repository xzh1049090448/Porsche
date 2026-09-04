# B1-A through B1-D candidate baseline archive

This archive freezes the existing B1-A, B1-B1, B1-B2, B1-B3, B1-C and
B1-D candidate as the parent for the separately authorized B1-E work. It does
not add a feature, change an API contract, run a production operation, or
claim that fixture-skipped tests passed.

## Scope inventory

- Pre-existing candidate inventory: 353 paths, comprising 18 tracked
  modifications and 335 untracked paths.
- Candidate size before these archive metadata files: 16,180,422 bytes.
- All paths are assigned to a B1 slice, shared B1 integration, B1 tracking, or
  the B1-D joint-acceptance archive. No package/cache directory, `.env`,
  `fixture.env`, credential file, private key, JWT, credential-bearing DSN,
  bearer authorization header, or Set-Cookie value was found.
- `manifest.json` records every pre-existing path, its original Git state,
  size, SHA-256 digest, and scope assignment. `stage-inputs.sha256` freezes the
  complete staged input except itself and documents that self-exclusion.

## Fresh verification

- `./init.sh`: the first sandboxed attempt stopped during `go mod tidy`
  because the default Go cache was not writable. The rerun used a private
  `GOCACHE`, cleared application/database/Redis/test/start variables, did not
  start the service, and passed.
- `go test ./... -count=1 -json`: 389 tests passed, 258 fixture-dependent tests
  skipped, 0 failed; 15 test packages passed and 4 no-test-file packages
  skipped.
- B1 package race run (`authz`, `migration`, `models`, `service`, `handler`,
  `middleware`): 130 tests passed, 248 fixture-dependent tests skipped, 0
  failed; all 6 packages passed.
- Focused migration run: 6 tests passed, 14 fixture-dependent tests skipped,
  0 failed. No new MySQL/Redis fixture was created, so a fresh real execution
  of migrations 0001-0004 is not claimed by this archive.
- `go build ./...`, `go vet ./...`, and `git diff --check`: exit 0.
- Evidence JSON: 18 complete JSON documents, 4 JSON event streams stored with
  a `.json` suffix, and 48 `.jsonl` streams parsed with 0 invalid records.

### Immutable raw-evidence whitespace exceptions

The staged all-file `git diff --cached --check` reports six historical raw
evidence files. They are non-executable captures, and their exact bytes already
match the earlier B1-D or joint-acceptance manifest. They are retained without
formatting so the historical hash chain remains valid:

- `real-fixture/count-index/analyze-current-after.txt`
- `real-fixture/count-index/analyze-current-before.txt`
- `real-fixture/count-index/h1-use-index-output-and-explain.txt`
- `real-fixture/count-index/h3-ignore-active-updated-overlay.log`
- `real-fixture/counts.json`
- `joint-acceptance-20260904/bootstrap.log`

After excluding exactly those six immutable captures, `git diff --cached
--check` reports no errors across production source, migrations, tests,
specifications, plans, reports, tracking files, and all remaining evidence.

## Historical hash interpretation

Historical hash files are immutable point-in-time evidence. Four expected
cross-slice differences were retained rather than rewritten:

- the B1-C static router hash predates B1-C/B1-D route registration;
- the pre-H3 B1-D `admin_users_read.go` hashes predate the reviewed H3 query;
- the H3 QA manifest hash predates the exact-cleanup manifest update;
- the joint-acceptance `manifest.sha256` resolves relative to its own directory
  and matches.

The final B1-B3 hash set, current joint-acceptance backend hash set, and all
other entries that describe the current candidate match. Historical mismatches
are evidence chronology, not replacements for the current baseline digest.

## Explicit limitations

- Production migration, deployment, push, model/SSE/upstream calls and a new
  Docker fixture were not performed.
- Fixture-dependent skips remain skips. The existing real-fixture reports are
  preserved as historical evidence and were checked for secret hygiene, but
  were not recreated in this archive step.
- This commit establishes a reproducible B1-E parent only; it does not mark the
  overall PRD or the remaining joint-acceptance cases complete.
