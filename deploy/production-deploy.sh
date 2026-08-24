#!/usr/bin/env bash
# Deploy only the application container. Database services are intentionally out of scope.
set -Eeuo pipefail

if (( $# != 0 )); then
    echo 'production deployment does not accept command-line arguments' >&2
    exit 1
fi

APP_NAME="${APP_NAME:-ai-gateway-go}"
IMAGE_NAME="${IMAGE_NAME:-ai-gateway-go:main}"
HOST_PORT="${HOST_PORT:-8000}"
network_was_set=false
if [[ ${APP_DOCKER_NETWORK+x} ]]; then
    network_was_set=true
fi
APP_DOCKER_NETWORK="${APP_DOCKER_NETWORK:-}"

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="$repo_root/.env"

cd "$repo_root"
command -v git >/dev/null
command -v docker >/dev/null
command -v curl >/dev/null
command -v flock >/dev/null

if [[ ! "$HOST_PORT" =~ ^[1-9][0-9]{0,4}$ ]] || (( HOST_PORT > 65535 )); then
    echo 'HOST_PORT must be an integer from 1 through 65535' >&2
    exit 1
fi
if [[ ! "$APP_NAME" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]]; then
    echo 'APP_NAME must be a valid Docker container name' >&2
    exit 1
fi
if [[ "$network_was_set" == true && -z "$APP_DOCKER_NETWORK" ]]; then
    echo 'APP_DOCKER_NETWORK must not be empty when set' >&2
    exit 1
fi
if [[ ! -f "$ENV_FILE" ]]; then
    echo 'deployment requires the repository .env' >&2
    exit 1
fi
if [[ "$(git rev-parse --is-inside-work-tree)" != true ]]; then
    echo 'deployment must run from a Git worktree' >&2
    exit 1
fi

default_lock_file="/var/lock/${APP_NAME}.deploy.lock"
fixture_lock_file="$repo_root/.deploy-locks/${APP_NAME}.deploy.lock"
LOCK_FILE="${LOCK_FILE:-$default_lock_file}"
if [[ "$LOCK_FILE" != "$default_lock_file" && "$LOCK_FILE" != "$fixture_lock_file" ]]; then
    echo 'LOCK_FILE must be the application lock in /var/lock or this checkout .deploy-locks' >&2
    exit 1
fi
exec 9>"$LOCK_FILE"
if flock -E 75 -n 9; then
    :
else
    lock_status=$?
    if (( lock_status == 75 )); then
        echo "another deployment is already running for ${APP_NAME}" >&2
    fi
    exit "$lock_status"
fi

# Deliberately exclude untracked files so operators can retain local credentials.
git diff --quiet
git diff --cached --quiet
git fetch origin main
git switch main
git reset --hard origin/main

network_args=()
if [[ -n "$APP_DOCKER_NETWORK" ]]; then
    docker network inspect "$APP_DOCKER_NETWORK" >/dev/null
    network_args=(--network "$APP_DOCKER_NETWORK")
fi

rollback_name="${APP_NAME}-rollback-$$"
had_previous=false
new_attempted=false
deployment_succeeded=false

restore_previous() {
    local status=$?
    if [[ "$deployment_succeeded" != true ]]; then
        if [[ "$new_attempted" == true ]]; then
            if ! docker rm -f -- "$APP_NAME" >/dev/null 2>&1; then
                echo 'rollback failed: could not remove candidate container' >&2
            fi
        fi
        if [[ "$had_previous" == true ]]; then
            if ! docker rename -- "$rollback_name" "$APP_NAME" >/dev/null 2>&1; then
                echo 'rollback failed: could not restore previous container name' >&2
            fi
            if ! docker start -- "$APP_NAME" >/dev/null 2>&1; then
                echo 'rollback failed: could not start restored container' >&2
            fi
        fi
    fi
    return "$status"
}
trap restore_previous EXIT

docker build --tag "$IMAGE_NAME" .
if docker container inspect -- "$APP_NAME" >/dev/null 2>&1; then
    had_previous=true
    docker stop -- "$APP_NAME" >/dev/null
    docker rename -- "$APP_NAME" "$rollback_name" >/dev/null
fi

new_attempted=true
if [[ -n "$APP_DOCKER_NETWORK" ]]; then
    container_id="$(docker run -d --name "$APP_NAME" --env-file "$ENV_FILE" \
        --publish "127.0.0.1:${HOST_PORT}:8000" "${network_args[@]}" "$IMAGE_NAME")"
else
    container_id="$(docker run -d --name "$APP_NAME" --env-file "$ENV_FILE" \
        --publish "127.0.0.1:${HOST_PORT}:8000" "$IMAGE_NAME")"
fi

healthy=false
for _ in {1..30}; do
    if curl -fsS --connect-timeout 2 --max-time 3 "http://127.0.0.1:${HOST_PORT}/health" >/dev/null; then
        healthy=true
        break
    fi
    sleep 1
done
if [[ "$healthy" != true ]]; then
    echo 'application health check did not succeed within 30 seconds' >&2
    exit 1
fi

if [[ "$had_previous" == true ]]; then
    docker rm -- "$rollback_name" >/dev/null
fi
deployment_succeeded=true
trap - EXIT
printf 'deployment succeeded: container=%s revision=%s\n' "$container_id" "$(git rev-parse HEAD)"
