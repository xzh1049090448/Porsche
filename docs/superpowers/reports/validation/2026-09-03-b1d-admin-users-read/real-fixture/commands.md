# Executed command shapes

Secrets and URLs are represented by private environment variables and were
never printed or written here. Every Go command also set
`GOCACHE=/private/tmp/porsche-go-build-cache`.

```text
./init.sh

go build -o <private-dir>/migrate ./cmd/migrate
env -i DATABASE_URL="$TEST_DATABASE_URL" APP_ENV=test SNOWFLAKE_NODE_ID=904 <private-dir>/migrate up
env -i DATABASE_URL="$TEST_DATABASE_URL" APP_ENV=test SNOWFLAKE_NODE_ID=904 <private-dir>/migrate status

go test ./internal/service -run 'AdminUsersReadDB|AdminUsersReadQuery$|AdminUsersReadProjection$|AdminUsersReadLegacy|AuthProjection' -count=1 -json
go test ./internal/handler -run 'AdminUsersReadHTTP|AuthProjection' -count=1 -json
go test -race ./internal/service -run 'AdminUsersReadDB|AdminUsersReadQuery$|AdminUsersReadProjection$|AdminUsersReadLegacy|AuthProjection' -count=1 -json
go test -race ./internal/handler -run 'AdminUsersReadHTTP|AuthProjection' -count=1 -json

go test -p 1 ./... -count=1 -json
go build ./...
go vet ./...
git diff --check

go test ./internal/handler -run '^(TestAdminUserBehaviorRequiresStrictlyLowerTargetRole|TestAdminUsersHTTPHierarchy)$' -count=1 -json
B1D_RUN_PERFORMANCE=1 go test ./internal/handler -run '^TestAdminUsersReadPerformance$' -count=1 -v
```

Before every test command, `APP_ENV`, `SNOWFLAKE_NODE_ID`, `DATABASE_URL`,
`REDIS_URL`, and `RUN_START_COMMAND` were unset. Only the private task
`TEST_DATABASE_URL` and `TEST_REDIS_URL` were present. The full suite and the
two mutable package suites were serialized; performance ran alone.
