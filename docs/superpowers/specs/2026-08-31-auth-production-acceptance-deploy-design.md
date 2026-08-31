# Production-Domain Authentication Acceptance Deployment Design

## Goal

Deploy the already-pushed user-registration backend and session-auth frontend
branches to `https://aiportcloud.com` for a controlled browser acceptance test,
without using the normal `main`-only release scripts and without automatically
changing MySQL.

## Scope and boundary

- Backend source branch: `feature/user-registration-management`.
- Frontend source branch: `feature/session-auth-frontend`.
- The existing application container and the live frontend directory are
  replaced only after the candidate backend passes its loopback health check.
- The existing MySQL container, its volumes, Docker network, and unrelated
  containers are never stopped, removed, or recreated.
- The deployment script does not run a database migration. Migration is a
  separately confirmed operator action because schema changes are not an
  automatic rollback target.

## Redis prerequisite

Production authentication is fail-closed without Redis. Before deployment the
operator creates one internal Docker Redis 7 service named `porsche-redis`:

- attached only to `porsche-app`;
- no host `-p` mapping;
- AOF enabled and backed by a named volume;
- protected by a distinct random password;
- addressed by the application as
  `REDIS_URL=redis://:<password>@porsche-redis:6379/0`.

The deployment script only checks that this dependency is reachable from the
candidate application. It does not parse, print, create, rotate, or store the
Redis password.

## Configuration prerequisite

The repository `.env` remains operator-owned and untracked. It must set:

```dotenv
APP_ENV=production
ALLOWED_HOSTS=aiportcloud.com
AUTH_TRUSTED_ORIGINS=https://aiportcloud.com
REDIS_URL=redis://:<password>@porsche-redis:6379/0
REGISTER_ENABLED=true
PASSWORD_REGISTER_ENABLED=true
PASSWORD_LOGIN_ENABLED=true
FIXED_LOGIN_ENABLED=false
SMS_DEV_MODE=false
```

`JWT_SECRET_KEY`, `AUTH_HMAC_KEY`, `ADMIN_TOKEN`, and `METRICS_TOKEN` must be
four different production-strength secrets. `ROOT_BOOTSTRAP_USERNAME` and
`ROOT_BOOTSTRAP_PASSWORD` are used together only when intentionally creating
the first Root user, then removed from `.env` after that start succeeds.

## Deployment flow

1. Refuse tracked-dirty repositories, wrong backend/frontend branches, missing
   `.env`, missing `porsche-app`, missing Redis container, invalid Nginx config,
   or missing required tools before any Docker or static-file write.
2. Build the frontend from the checked-out session-auth branch. Stage `dist/`
   in a `mktemp` directory; do not yet alter `/var/www/porsche-web`.
3. Build a candidate backend image from the checked-out registration branch.
   Stop and rename the existing application container to a unique rollback
   name only long enough to release loopback port 8000. Start the candidate as
   the normal application container on `127.0.0.1:8000` and `porsche-app`.
4. Probe `/health` with bounded timeouts and `Host: aiportcloud.com`. If the
   candidate fails, remove it and restore/start the renamed old container.
5. After candidate health succeeds, copy the current frontend root to a
   rollback directory, publish the staged static files, then reload Nginx.
   If the Nginx reload fails, restore the saved static files and restore the
   previous application container.
6. Print the backend and frontend revisions plus the rollback directory. The
   operator completes browser acceptance before treating this as a release.

## Explicit migration step

After configuration and Redis are confirmed, the operator runs the embedded
migration command against the already configured MySQL database. The command
is intentionally separate from deployment:

```bash
sudo docker run --rm --env-file /opt/Porsche/.env --network porsche-app \
  -v /opt/Porsche:/src:ro -w /src golang:1.22-alpine \
  sh -ec 'apk add --no-cache git ca-certificates && go run ./cmd/migrate up'
```

This is allowed only after an operator confirms the target database and has a
backup or accepts the additive schema change. The implementation must document
the exact migration IDs and verify whether they are backward compatible before
the command is presented as a rollback-safe operation.

## Rollback

The deployment script rolls back candidate start, health, publish, and Nginx
reload failures automatically. A later manual rollback uses the recorded
frontend backup plus the existing `main` release workflow after the operator
explicitly switches both repositories back to `main`. Database migrations are
not deleted or reversed by either rollback path.

## Verification

- Shell regression tests execute future deployment entry points inside a
  disposable Docker test container with no Docker socket, no network, and no
  production checkout, `.env`, database, Redis, or host service mount. The
  fixture provides mocked Docker, Git, Nginx, Redis preflight, and rsync
  commands only inside that container. Therefore an absolute command path or
  shell wrapper cannot mutate the host even if a future script accidentally
  bypasses `PATH`.
- The fixture still uses exact, structured argv assertions to prove no `main`
  reset/switch, no MySQL/volume/network deletion, no static publish before
  Nginx validation, and candidate rollback on failures. Static checks remain
  limited to clear policy violations; they are not relied on as the execution
  sandbox.
- `bash -n`, shell regression tests, Nginx static checks, Go tests and vet run
  before the script is offered for deployment.
- Browser acceptance at `https://aiportcloud.com` verifies username register,
  login, refresh-page recovery, session revocation, logout, and that only the
  `HttpOnly; Secure` refresh cookie exists outside JavaScript storage.
