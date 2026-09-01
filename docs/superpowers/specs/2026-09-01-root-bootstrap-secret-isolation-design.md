# Root Bootstrap Secret Isolation Design

## Goal

Create the first production Root account without placing its one-time password
in the environment or persisted configuration of the long-running application
container. Preserve the existing one-Root-only database invariant, explicit
operator confirmation, and acceptance-deployment rollback guarantees.

## Security boundary

- Root credentials live in a root-owned `0600` file outside the repository.
- Docker receives only a read-only bind mount path for that credential file;
  the username and password do not appear in Docker argv, `--env`,
  `--env-file`, image layers, deployment manifests, or command output.
- A dedicated `--rm` container reads the credentials, creates the Root, and
  exits before the long-running candidate is started.
- `/opt/Porsche/.env` must not contain non-empty
  `ROOT_BOOTSTRAP_USERNAME` or `ROOT_BOOTSTRAP_PASSWORD` when the candidate is
  started. The deployment entry point rejects either value fail-closed.
- The credential file remains after bootstrap only so the operator can perform
  the acceptance login. Its removal is a separate, explicit post-acceptance
  action.

## Components

### `cmd/bootstrap-root`

Add a narrow Go command that accepts exactly one credentials-file argument.
It loads normal production settings and the configured MySQL database, reads
exactly one `username=<value>` and one `password=<value>` entry from the mounted
file, assigns those values only to the in-process settings object, and calls
the existing `AuthService.BootstrapRoot` implementation.

The command reuses existing username/password validation, MySQL named locking,
transactional Root creation, permanent Root tombstone handling, password
hashing, and audit-event creation. It prints no credential value. A successful
new creation and an already-consumed bootstrap are distinguishable: the
operator command succeeds only when the database contains exactly one Root
after the operation, while repeated execution reports that bootstrap has
already been consumed without creating another account.

The command does not start HTTP, initialize Redis-backed sessions, run schema
migrations, or alter application containers.

### `deploy/auth-acceptance-bootstrap-root.sh`

Add a root-only entry point requiring exactly
`--confirm-auth-root-bootstrap`. It verifies:

- the backend checkout is clean, on
  `feature/user-registration-management`, and equal to its fetched remote SHA;
- `/opt/Porsche/.env` exists and contains no non-empty Root bootstrap values;
- the credentials file is a regular, non-symlink file owned by root with mode
  `0600`, and its syntax contains exactly the two permitted keys;
- `porsche-app` and `porsche-mysql` exist; and
- migration `0002` is already applied by the command's successful database
  startup and schema-dependent bootstrap operation.

It builds the acceptance image from the verified checkout, then runs the
dedicated command in a disposable container attached to `porsche-app`. The
already-built image contains the verified source; `.env` and the credentials
file are mounted read-only. The `.env` mount is used as a normal
non-secret-bootstrap configuration source; only the credentials file path is
supplied on the command line. The container is removed automatically.

Before exiting, the Go command performs a non-secret database count check and
requires exactly one Root. The wrapper never prints the credentials file,
exports its values, rewrites `.env`, or starts/stops/removes the live
application.

### Candidate deployment guard

Extend `auth-acceptance-deploy.sh` preflight to reject a non-empty
`ROOT_BOOTSTRAP_USERNAME` or `ROOT_BOOTSTRAP_PASSWORD` before frontend build,
Docker writes, or static publication. This makes the long-running container's
existing `--env-file` use safe for this one-time secret and prevents regression
to the original startup-bootstrap workflow.

## Operator flow

1. Store the acceptance Root credentials in
   `/var/lib/porsche-auth-acceptance/root-acceptance-credentials` with root
   ownership and mode `0600`.
2. Remove the two Root bootstrap entries from `/opt/Porsche/.env`, or set both
   to empty values.
3. Run the explicit Root bootstrap wrapper.
4. Verify exactly one Root exists and verify that no running or stopped
   application container contains `ROOT_BOOTSTRAP_` values.
5. Run the normal authentication acceptance candidate deployment.
6. Use the root-only credential file for the controlled browser acceptance
   login. Delete it after credentials are transferred or rotated and acceptance
   evidence is complete.

The database migration remains forward-only and is not changed by this flow.
Application rollback does not delete or recreate the Root account.

## Failure handling

- Invalid confirmation, checkout, file metadata, file syntax, configuration,
  Docker prerequisites, or database connectivity fails before any Root write.
- Invalid or weak credentials are rejected by the existing domain validation.
- Concurrent attempts serialize through the existing MySQL named lock.
- If a Root or Root tombstone already exists, no new privileged account is
  created.
- A successful database commit followed by a wrapper interruption is safe:
  rerunning observes the existing Root and cannot create a duplicate.
- No failure path mutates the live application container, frontend files,
  Redis, MySQL container, Docker volumes, or Docker network.

## Verification

Tests follow RED-GREEN development and cover:

- credential-file parsing rejects symlinks, wrong ownership/mode, unknown or
  duplicate keys, missing values, and malformed lines without disclosing data;
- the Go command reuses password validation and creates at most one Root;
- the isolated shell fixture records Docker argv and proves no credential value
  appears in argv, environment options, logs, or rollback manifests;
- bootstrap rejection causes no live-container or static-file writes;
- the deployment fixture rejects non-empty Root bootstrap values before Docker
  writes;
- existing shell regression tests, Go tests, `go vet`, syntax checks, and
  `git diff --check` remain green.

Production acceptance additionally verifies the transient bootstrap container
has been removed, the database Root count is one, and `docker inspect` of the
long-running application contains no `ROOT_BOOTSTRAP_` entries.
