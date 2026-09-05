# A14 baseline validation

Date: 2026-09-05

## Worktrees

- Backend: `/Users/xuzhihao/code/Porsche/.worktrees/admin-public-260903`
  - HEAD: `6c031edab409367902f3b388a70736b84f7ab37d`
  - Design SHA256: `d0dbd91fe0d85bb7c356f64d06e4d6d92037f0a7e71b70745d737ae5d1c60d92`
- Frontend: `/Users/xuzhihao/code/Porsche-Web/.worktrees/admin-public-260903`
  - checkpoint HEAD: `9d301c1349b6252318798a8e97ea94a6f20b6385`

## Gates

All commands completed with exit code 0 after the frontend checkpoint.

- Backend initializer: `env -u TEST_DATABASE_URL -u TEST_REDIS_URL -u DATABASE_URL -u REDIS_URL -u APP_ENV -u RUN_START_COMMAND ./init.sh` — exit 0. The first sandboxed attempt failed because Go build-cache access was denied; the identical command was rerun with minimum permission and passed. No environment values were recorded.
- Backend tests: `env -u TEST_DATABASE_URL -u TEST_REDIS_URL go test -p 1 ./... -count=1` — exit 0; 18 packages reported, including 15 `ok` packages and 3 packages with no test files.
- Backend build: `go build ./...` — exit 0.
- Backend vet: `go vet ./...` — exit 0.
- Backend diff check: `git diff --check` — exit 0.
- Frontend tests: `npm test` — exit 0; 134 passed, 0 failed, 0 skipped.
- Frontend build: `npm run build` — exit 0; Vite emitted existing chunk-size and dynamic-import warnings.
- Frontend diff check: `git diff --check` — exit 0.

## Final state

Frontend reviewed candidate was checkpointed unchanged as `9d301c1349b6252318798a8e97ea94a6f20b6385`. Backend report creation is the only subsequent change. Both worktrees were verified clean after the report commit.

