#!/usr/bin/env bash
# REVIEW DRAFT ONLY. No container, database, migration or test is launched.
# The coordinator must obtain authorization for this batch's complete lifecycle
# before turning this draft into an executable runner.
set -euo pipefail

readonly task_label='b1c-admin-authz-read-260903'
readonly mysql_name='b1c-admin-authz-read-260903-mysql'
readonly redis_name='b1c-admin-authz-read-260903-redis'
readonly database_name='porsche_b1c_admin_authz_260903_test'

# Reviewed operations for a future authorized runner:
# 1. Resolve locally available mysql:8.0 and redis:7-alpine to immutable image IDs
#    using docker image inspect --format '{{.Id}}' (never pull implicitly).
# 2. Create a private mktemp directory under /private/tmp, chmod 0700. Generate
#    unique 32-byte hex passwords with Python secrets.token_hex(32); write only
#    0600 mysql-root-password, mysql-client.cnf, redis.conf and fixture.env.
#    Do not print credentials, Redis config, .env, or SQL connection strings.
# 3. Check the two exact container names do not exist; fail on collision.
#    Run the following commands with private paths and pinned image IDs:
#
# docker run --pull=never --detach --rm --name "$mysql_name" \
#   --label "codex.task=$task_label" --publish 127.0.0.1::3306 \
#   --tmpfs /var/lib/mysql:rw,nosuid,nodev --tmpfs /tmp:rw,nosuid,nodev \
#   --mount "type=bind,src=$private_dir/mysql-root-password,dst=/run/secrets/mysql-root-password,readonly" \
#   --mount "type=bind,src=$private_dir/mysql-client.cnf,dst=/run/secrets/mysql-client.cnf,readonly" \
#   --env MYSQL_ROOT_PASSWORD_FILE=/run/secrets/mysql-root-password \
#   --env MYSQL_ROOT_HOST=% --env "MYSQL_DATABASE=$database_name" "$mysql_image_id"
#
# docker run --pull=never --detach --rm --name "$redis_name" \
#   --label "codex.task=$task_label" --publish 127.0.0.1::6379 \
#   --tmpfs /data:rw,nosuid,nodev \
#   --mount "type=bind,src=$private_dir/redis.conf,dst=/usr/local/etc/redis/redis.conf,readonly" \
#   "$redis_image_id" redis-server /usr/local/etc/redis/redis.conf
#
# redis.conf contains bind 0.0.0.0, port 6379, appendonly no, save "", and a fresh
# requirepass. It is private and never included in evidence.
# 4. Record exact returned container IDs, image IDs, labels, database name and
#    docker port output; reject any mapping outside 127.0.0.1. Use exact-ID
#    inspect with requested fields only, never whole environment/container JSON.
# 5. Wait boundedly for readiness. Verify SELECT DATABASE() equals the approved
#    database, and server versions are MySQL8/Redis7. Write fixture.env privately
#    with TEST_DATABASE_URL and TEST_REDIS_URL, not production variable values.
# 6. Within a subprocess, map only this explicit TEST_DATABASE_URL to DATABASE_URL
#    and run `go run ./cmd/migrate up` for existing 0001-0003. No new migration.
# 7. Run focused B1-C tests/race, then serial full JSON, build/vet/diff. Existing
#    isolated tests may CREATE/DROP their uniquely owned *_test child schemas;
#    authorization must explicitly include that lifecycle in this new container.
#    New B1-C helpers only connect/create unique fixtures, never migrate/clear.
# 8. Capture redacted results. Cleanup only the two recorded exact container IDs
#    after checking exact codex.task labels and names against this manifest:
#    `docker stop "$recorded_mysql_id" "$recorded_redis_id"` (AutoRemove).
#    Unlink only the private files created by this run, then rmdir private_dir.
#    Never docker compose down -v, docker volume rm, wildcard prune, old fixtures,
#    root bootstrap, production .env, or persistent-volume operations.

printf '%s\n' 'DRAFT_ONLY: no operations performed.' \
  "task_label=$task_label" "mysql_name=$mysql_name" \
  "redis_name=$redis_name" "database_name=$database_name" \
  'Required approval: create task containers/database, existing 0001-0003 migrations, isolated test child schemas, and exact task cleanup.'
