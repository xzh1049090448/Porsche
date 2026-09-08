# Production Release Preflight Hotfix Design

## Context

PRD-260903 merged to backend `main` at `7e98becc2a468b3a11f19f370b47c45ba4fed997` and frontend `main` at `db163c9c860ba737cae22562c44ed5f5dc7e4cae`. Production still serves the older frontend asset and does not expose the A03 create route. Before the next deployment, the release path must reject incomplete security configuration, production Mock builds, and non-reproducible dependency installation.

## Scope

This hotfix changes only configuration documentation, production configuration validation, frontend build gating, and full-stack deployment orchestration. It does not run a production migration, deploy either application, create a production account, alter business behavior, or close R02 production acceptance.

## Backend configuration contract

`.env.example` will document every runtime setting read by `internal/config/config.go`. `ACTION_SECURITY_HMAC_KEY` remains commented because development may omit it; its comment will state that staging and production require a freshly generated 32-byte value encoded as exactly 43 unpadded Base64URL characters. Real secrets will never be committed.

Outside development, `FIXED_LOGIN_ENABLED` remains required to be false. `FIXED_LOGIN_PHONE` and `FIXED_LOGIN_PASSWORD` are development-only settings: an explicit declaration outside development is rejected, while their absence no longer forces operators to supply meaningless replacement values. This resolves the existing contradiction between `.env.example` and `validateProductionAuthSettings` without weakening the prohibition on fixed login.

The backend will expose a configuration-check command that loads the same `config.Load()` path used by the server and exits before database, Redis, HTTP listener, or application construction. The command prints only a generic success message or the existing sanitized configuration error. `deploy/restart-all.sh` will run this check from the backend image with the production `.env` and `porsche-app` network before building the frontend or stopping the current application container.

## Frontend production gate

The frontend will add a small Node preflight command that reads Vite's production environment using Vite's own `loadEnv` behavior. It accepts only the literal value `false` for `VITE_USE_MOCK`; missing, blank, mixed-case, or truthy values fail before `vite build`. The build script will run the preflight first, so both direct production builds and `restart-all.sh` share the same rule.

Development and test commands retain their existing Mock behavior. `VITE_API_BASE` may remain blank because production uses same-origin Nginx routing.

## Reproducible dependency installation

Because `package-lock.json` is committed, `restart-all.sh` will replace `npm install --package-lock=false` with `npm ci`. The release script will require both `package.json` and `package-lock.json` before modifying either live service. It will not delete or rewrite the lockfile.

## Failure ordering and rollback

All new checks run before the current backend container is stopped and before frontend files are published. A configuration, Mock gate, lockfile, dependency, or build failure therefore leaves the existing production application and static site unchanged. Existing backend candidate health-check rollback remains responsible for failures after container replacement.

The script continues to exclude database migration from deployment. Operators must back up the target database and run the forward migration separately before invoking the full-stack release.

## Tests

Backend tests will first demonstrate these failures:

- production accepts absent development-only fixed credentials but rejects either explicit credential variable;
- the environment template omits no supported runtime setting and does not contain a real action-security secret;
- the configuration-check command uses `config.Load()` and performs no service construction;
- `restart-all.sh` refuses a missing frontend lockfile, runs the backend preflight before frontend installation, uses `npm ci`, and never invokes the old non-locking install.

Frontend tests will first demonstrate these failures:

- production Mock values other than exact `false` are rejected;
- exact `false` with blank or configured `VITE_API_BASE` passes;
- the package build command invokes the preflight before Vite.

After each focused red/green cycle, run backend shell tests and Go tests, frontend Node tests and production build, and `git diff --check`. Final verification must record that no `.env`, secret value, build output, or production state was changed.

## Delivery

Backend and frontend changes use paired `hotfix/production-release-preflight` branches and separate pull requests. Both must merge before production migration and deployment. The deployed backend and frontend revisions, migration ledger, HTTPS browser checks, and rollback evidence remain separate R02 acceptance artifacts.
