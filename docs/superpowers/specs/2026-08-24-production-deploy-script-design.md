# Production Deployment Script Design

## Goal

Provide a repeatable production deployment script for the Go gateway. The script updates the checkout to the latest remote `main`, rebuilds only the application container, and preserves the existing database and its configuration.

## Scope

- Add `deploy/production-deploy.sh`.
- Use the deployment checkout's existing `.env` as the sole source of database and runtime configuration.
- Build a new application image from the checked-out `main` revision.
- Replace only the application container with a bounded interruption and automatic rollback on candidate startup or health-check failure.
- Document the required operator inputs and Nginx boundary.

## Explicit Non-Goals

- Do not create, start, stop, remove, migrate, or inspect the MySQL container.
- Do not parse, print, alter, commit, or synthesize `.env` values.
- Do not delete Docker volumes, networks, images, or unrelated containers.
- Do not edit or reload Nginx automatically.

## Inputs and Defaults

The operator runs the script from a production checkout, normally `/opt/Porsche`.

| Input | Default | Purpose |
| --- | --- | --- |
| `APP_NAME` | `ai-gateway-go` | Application container name. |
| `IMAGE_NAME` | `ai-gateway-go:main` | Image tag built from current `main`. |
| `HOST_PORT` | `8000` | Host loopback port mapped to container port 8000. |
| `APP_DOCKER_NETWORK` | unset | Optional existing network to join. This only provides network reachability; it never supplies database settings. |
| `.env` | required | Existing production configuration, including `DATABASE_URL`. |

If `.env` uses a Docker hostname such as `mysql`, the operator must set `APP_DOCKER_NETWORK` to the network where that hostname resolves. If it uses an external address, `APP_DOCKER_NETWORK` is unnecessary.

## Deployment Flow

1. Require Bash, Git, Docker, a Git worktree, and an existing `.env` without printing its contents.
2. Fetch `origin/main`, require a clean tracked worktree, switch to `main`, and reset it exactly to `origin/main`. This deliberately updates only tracked code; untracked local deployment files remain untouched.
3. Build `IMAGE_NAME` from the fetched source.
4. If an `APP_NAME` container exists, stop it and rename it to a unique rollback name. This releases the fixed loopback port without deleting the known-good container.
5. Start the new version using the final `APP_NAME`, `--env-file .env`, `127.0.0.1:${HOST_PORT}:8000`, and optional `--network ${APP_DOCKER_NETWORK}`.
6. Poll the new container's loopback `/health` endpoint for a bounded time. If startup or health fails, remove the new container, rename the rollback container to `APP_NAME`, start it, and exit non-zero.
7. After health succeeds, remove the stopped rollback container and print the new container ID and deployed Git revision. This gives a brief interruption during port handoff but does not require Nginx changes.

## Safety and Error Handling

- Use `set -Eeuo pipefail` and a cleanup trap that removes only the failed new application container and restores only the renamed previous application container.
- Never use `docker system prune`, `docker volume rm`, `docker compose down -v`, `git clean`, or a force push.
- Reject `HOST_PORT` values other than numeric TCP ports and reject an empty optional network value when explicitly set.
- Require `.env` permissions are left to the operator; the script does not read its content.
- The health endpoint proves process/database startup only. White-label directory, chat, and SSE smoke tests remain separate operator commands because they require secrets and authorized tokens.

## Verification

- A shell syntax check validates the script with `bash -n`.
- A behavior test with mocked Git, Docker and curl verifies the script fetches and resets to `origin/main`, uses `--env-file .env`, binds only `127.0.0.1`, performs stop/rename/new-start/health/old-remove in order, restores the old container after startup or health failure, and contains no database/volume destructive commands.
- Existing Go tests run with an explicit writable `GOCACHE` when the local sandbox blocks the default cache.
