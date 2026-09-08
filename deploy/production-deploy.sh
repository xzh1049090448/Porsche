#!/usr/bin/env bash
# Deploy only the application container. Database services are intentionally out of scope.
set -Eeuo pipefail

(( $# == 0 )) || { echo 'production deployment does not accept command-line arguments' >&2; exit 1; }
APP_NAME="${APP_NAME:-ai-gateway-go}"; IMAGE_NAME="${IMAGE_NAME:-ai-gateway-go:main}"; HOST_PORT="${HOST_PORT:-8000}"
network_was_set=false; [[ ${APP_DOCKER_NETWORK+x} ]] && network_was_set=true; APP_DOCKER_NETWORK="${APP_DOCKER_NETWORK:-}"
prebuilt_was_set=false; [[ ${PREBUILT_IMAGE_ID+x} ]] && prebuilt_was_set=true
PREBUILT_IMAGE_ID="${PREBUILT_IMAGE_ID:-}"; PREBUILT_SOURCE_REVISION="${PREBUILT_SOURCE_REVISION:-}"; ENV_SNAPSHOT="${ENV_SNAPSHOT:-}"
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"; ENV_FILE="$repo_root/.env"; cd "$repo_root"
for command_name in git docker curl flock mktemp cp chmod rm stat; do command -v "$command_name" >/dev/null || { echo "required command is unavailable: $command_name" >&2; exit 1; }; done
[[ "$HOST_PORT" =~ ^[1-9][0-9]{0,4}$ ]] && (( HOST_PORT <= 65535 )) || { echo 'HOST_PORT must be an integer from 1 through 65535' >&2; exit 1; }
[[ "$APP_NAME" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || { echo 'APP_NAME must be a valid Docker container name' >&2; exit 1; }
[[ "$network_was_set" != true || -n "$APP_DOCKER_NETWORK" ]] || { echo 'APP_DOCKER_NETWORK must not be empty when set' >&2; exit 1; }
[[ -f "$ENV_FILE" && ! -L "$ENV_FILE" ]] || { echo 'deployment requires the repository .env' >&2; exit 1; }
[[ "$(git rev-parse --is-inside-work-tree)" == true ]] || { echo 'deployment must run from a Git worktree' >&2; exit 1; }

release_lock_default=/var/lock/porsche-full-stack.deploy.lock
release_lock_fixture="$repo_root/.deploy-locks/porsche-full-stack.deploy.lock"
LOCK_FILE="${LOCK_FILE:-$release_lock_default}"
[[ "$LOCK_FILE" == "$release_lock_default" || "$LOCK_FILE" == "$release_lock_fixture" ]] || { echo 'LOCK_FILE must be the shared production release lock' >&2; exit 1; }
if [[ -n "${RELEASE_LOCK_FD:-}" ]]; then
    [[ "$RELEASE_LOCK_FD" =~ ^[0-9]+$ && -e "/dev/fd/$RELEASE_LOCK_FD" ]] || { echo 'invalid inherited release lock descriptor' >&2; exit 1; }
    lock_identity="$(stat -L -f '%i' "$LOCK_FILE" 2>/dev/null || stat -L -c '%i' "$LOCK_FILE")"
    fd_identity="$(stat -L -f '%i' "/dev/fd/$RELEASE_LOCK_FD" 2>/dev/null || stat -L -c '%i' "/dev/fd/$RELEASE_LOCK_FD")"
    [[ "$lock_identity" == "$fd_identity" ]] || { echo 'inherited release lock descriptor does not match lock file' >&2; exit 1; }
    flock -E 75 -n "$RELEASE_LOCK_FD" || { echo 'another production release is already running' >&2; exit 75; }
else
    exec 9>"$LOCK_FILE"
    flock -E 75 -n 9 || { echo 'another production release is already running' >&2; exit 75; }
fi

snapshot_path=''; owns_snapshot=false
cleanup_snapshot(){ [[ "$owns_snapshot" != true || -z "$snapshot_path" ]] || rm -f -- "$snapshot_path"; }
if [[ "$prebuilt_was_set" == true ]]; then
    [[ "$PREBUILT_IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo 'PREBUILT_IMAGE_ID must be an immutable sha256 image ID' >&2; exit 1; }
    [[ "$PREBUILT_SOURCE_REVISION" =~ ^[0-9a-f]{40}$ ]] || { echo 'PREBUILT_SOURCE_REVISION must be a full Git commit' >&2; exit 1; }
    [[ "$(git rev-parse HEAD)" == "$PREBUILT_SOURCE_REVISION" ]] || { echo 'current checkout does not match PREBUILT_SOURCE_REVISION' >&2; exit 1; }
    [[ "$ENV_SNAPSHOT" == /* && -f "$ENV_SNAPSHOT" && ! -L "$ENV_SNAPSHOT" ]] || { echo 'prebuilt deployment requires an absolute regular ENV_SNAPSHOT' >&2; exit 1; }
    snapshot_mode="$(stat -f '%Lp' "$ENV_SNAPSHOT" 2>/dev/null || stat -c '%a' "$ENV_SNAPSHOT")"
    [[ "$snapshot_mode" == 600 ]] || { echo 'ENV_SNAPSHOT must have mode 0600' >&2; exit 1; }
    snapshot_path="$ENV_SNAPSHOT"
else
    git diff --quiet; git diff --cached --quiet; git fetch origin main; git switch main; git reset --hard origin/main
    PREBUILT_SOURCE_REVISION="$(git rev-parse HEAD)"
    [[ -f "$repo_root/.env.example" && -x "$repo_root/deploy/merge-env-example.sh" ]] || { echo 'standalone deployment requires the environment template and merge command' >&2; exit 1; }
    "$repo_root/deploy/merge-env-example.sh" "$repo_root/.env.example" "$ENV_FILE"
    exec 8>"$repo_root/.env.merge.lock"; flock -E 75 -n 8 || { echo 'environment file is being updated' >&2; exit 75; }
    snapshot_path="$(mktemp "$repo_root/.env.release.XXXXXX")"; chmod 0600 "$snapshot_path"; cp "$ENV_FILE" "$snapshot_path"
    owns_snapshot=true
    trap cleanup_snapshot EXIT
fi

allowed_hosts_value=''; allowed_hosts_found=false; allowed_hosts_invalid=false
while IFS= read -r env_line || [[ -n "$env_line" ]]; do
    if [[ "$env_line" =~ ^[[:space:]]*(export[[:space:]]+)?ALLOWED_HOSTS[[:space:]]*=[[:space:]]*(.*)$ ]]; then allowed_hosts_found=true; allowed_hosts_value="${BASH_REMATCH[2]}"; break; fi
done <"$snapshot_path"
[[ "$allowed_hosts_found" == true ]] || { echo 'deployment requires a non-empty ALLOWED_HOSTS entry in environment snapshot' >&2; exit 1; }
[[ "$allowed_hosts_value" != *$'\r'* && "$allowed_hosts_value" != *$'\n'* ]] || allowed_hosts_invalid=true
allowed_hosts_value="${allowed_hosts_value#"${allowed_hosts_value%%[!$' \t']*}"}"; allowed_hosts_value="${allowed_hosts_value%"${allowed_hosts_value##*[!$' \t']}"}"
if [[ "$allowed_hosts_value" == \"* ]]; then [[ "$allowed_hosts_value" =~ ^\"([^\"]*)\"$ ]] && allowed_hosts_value="${BASH_REMATCH[1]}" || allowed_hosts_invalid=true
elif [[ "$allowed_hosts_value" == \'* ]]; then [[ "$allowed_hosts_value" =~ ^\'([^\']*)\'$ ]] && allowed_hosts_value="${BASH_REMATCH[1]}" || allowed_hosts_invalid=true
elif [[ "$allowed_hosts_value" == *\"* || "$allowed_hosts_value" == *\'* ]]; then allowed_hosts_invalid=true; fi
health_check_host="${allowed_hosts_value%%,*}"
[[ -n "$health_check_host" ]] || { echo 'deployment requires a non-empty ALLOWED_HOSTS entry in environment snapshot' >&2; exit 1; }
[[ "$allowed_hosts_invalid" != true && "$health_check_host" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ ]] || { echo 'deployment requires a valid ALLOWED_HOSTS entry in environment snapshot' >&2; exit 1; }

[[ -z "$APP_DOCKER_NETWORK" ]] || docker network inspect "$APP_DOCKER_NETWORK" >/dev/null
if [[ "$prebuilt_was_set" == true ]]; then
    inspected_image_id="$(docker image inspect --format '{{.Id}}' "$PREBUILT_IMAGE_ID")"; [[ "$inspected_image_id" == "$PREBUILT_IMAGE_ID" ]] || { echo 'PREBUILT_IMAGE_ID does not match the inspected image' >&2; exit 1; }; candidate_image_id="$PREBUILT_IMAGE_ID"
else
    docker build --tag "$IMAGE_NAME" .; candidate_image_id="$(docker image inspect --format '{{.Id}}' "$IMAGE_NAME")"; [[ "$candidate_image_id" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo 'built image did not resolve to an immutable sha256 image ID' >&2; exit 1; }
fi
if [[ -n "$APP_DOCKER_NETWORK" ]]; then docker run --rm --env-file "$snapshot_path" --network "$APP_DOCKER_NETWORK" --entrypoint /app/check-config "$candidate_image_id"
else docker run --rm --env-file "$snapshot_path" --entrypoint /app/check-config "$candidate_image_id"; fi

rollback_name="${APP_NAME}-rollback-$$"; had_previous=false; new_attempted=false; deployment_succeeded=false
restore_previous(){ local rc=$?; if [[ "$deployment_succeeded" != true ]]; then [[ "$new_attempted" != true ]] || docker rm -f -- "$APP_NAME" >/dev/null 2>&1 || true; if [[ "$had_previous" == true ]]; then docker rename -- "$rollback_name" "$APP_NAME" >/dev/null 2>&1 || true; docker start -- "$APP_NAME" >/dev/null 2>&1 || true; fi; fi; cleanup_snapshot; return "$rc"; }
trap restore_previous EXIT
if docker container inspect -- "$APP_NAME" >/dev/null 2>&1; then had_previous=true; docker stop -- "$APP_NAME" >/dev/null; docker rename -- "$APP_NAME" "$rollback_name" >/dev/null; fi
new_attempted=true
if [[ -n "$APP_DOCKER_NETWORK" ]]; then container_id="$(docker run -d --name "$APP_NAME" --env-file "$snapshot_path" --publish "127.0.0.1:${HOST_PORT}:8000" --network "$APP_DOCKER_NETWORK" "$candidate_image_id")"
else container_id="$(docker run -d --name "$APP_NAME" --env-file "$snapshot_path" --publish "127.0.0.1:${HOST_PORT}:8000" "$candidate_image_id")"; fi
healthy=false; for _ in {1..30}; do if curl -fsS -H "Host: ${health_check_host}" --connect-timeout 2 --max-time 3 "http://127.0.0.1:${HOST_PORT}/health" >/dev/null; then healthy=true; break; fi; sleep 1; done
[[ "$healthy" == true ]] || { echo 'application health check did not succeed within 30 seconds' >&2; exit 1; }
[[ "$had_previous" != true ]] || docker rm -- "$rollback_name" >/dev/null
deployment_succeeded=true; cleanup_snapshot; trap - EXIT
printf 'deployment succeeded: container=%s revision=%s\n' "$container_id" "$PREBUILT_SOURCE_REVISION"
