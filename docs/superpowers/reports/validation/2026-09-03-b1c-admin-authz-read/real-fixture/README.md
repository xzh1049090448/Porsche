# Authorized B1-C real fixture evidence

The user authorized this batch lifecycle with “就行” and “继续”: disposable task
containers/database, existing 0001–0003, isolated test-owned child schemas and
final exact task cleanup. No production target or real upstream is used.

JSON event identity, action and counts are preserved; arbitrary diagnostic output
is omitted so SQL, session IDs and credential-bearing diagnostics cannot enter
public evidence. Migration ledger is the actual post-migration database status.

`full-initial-harness-failure.jsonl` is historical: archived overlay `.go` files
were mistakenly compiled as a new package and migration-only APP_ENV=test leaked
into two config tests. Evidence source files were renamed `.go.txt` without
changing bytes, and migration env was scoped to the migration process. Production
code and test assertions were unchanged. `full.jsonl` is the corrected fresh run.

Archived independent overlay sources under `../independent-static/independent-probe`
have `.go.txt` suffixes to exclude them from Go package scanning. For reproduction,
copy them to a private temporary directory as `.go`, generate a new overlay.json
mapping the current handler test path to those temporary source files, and run
the documented `go test -overlay` command. Original overlay.json and security
report retain historical temporary paths and bytes as provenance, not as a ready
workspace overlay.
