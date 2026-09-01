#!/usr/bin/env bash
set -Eeuo pipefail

reject_untrusted_test_mode() {
    echo 'test mode is restricted to isolated fixture container' >&2
    exit 1
}

canonicalize_fixture_override() {
    local override_name="$1" original parent leaf canonical_parent canonical
    original="${!override_name:-}"
    [[ -n "$original" && "$original" == /* ]] || reject_untrusted_test_mode
    if [[ -e "$original" || -L "$original" ]]; then
        canonical="$(readlink -f -- "$original" 2>/dev/null)" || reject_untrusted_test_mode
    else
        parent="$(dirname -- "$original" 2>/dev/null)" || reject_untrusted_test_mode
        leaf="$(basename -- "$original" 2>/dev/null)" || reject_untrusted_test_mode
        [[ -n "$leaf" && "$leaf" != . && "$leaf" != .. ]] || reject_untrusted_test_mode
        canonical_parent="$(readlink -f -- "$parent" 2>/dev/null)" || reject_untrusted_test_mode
        canonical="$canonical_parent/$leaf"
    fi
    [[ "$canonical" == /fixture || "$canonical" == /fixture/* ]] || reject_untrusted_test_mode
    printf -v "$override_name" '%s' "$canonical"
}

validate_test_mode_binding() {
    local resolved_entrypoint override_name
    resolved_entrypoint="$(readlink -f -- "${BASH_SOURCE[0]}" 2>/dev/null || true)"
    [[ "${PORSCHE_AUTH_ACCEPTANCE_TEST_CONTAINER:-}" == 1 &&
        "$resolved_entrypoint" == /fixture/Porsche/deploy/auth-acceptance-deploy.sh &&
        -f /.dockerenv && ! -e /var/run/docker.sock && ! -L /var/run/docker.sock ]] || reject_untrusted_test_mode
    for override_name in "$@"; do
        canonicalize_fixture_override "$override_name"
    done
}

if [[ "${PORSCHE_AUTH_ACCEPTANCE_TEST_MODE:-0}" == 1 ]]; then
    validate_test_mode_binding \
        PORSCHE_AUTH_ACCEPTANCE_BACKEND_DIR \
        PORSCHE_AUTH_ACCEPTANCE_FRONTEND_DIR \
        PORSCHE_AUTH_ACCEPTANCE_FRONTEND_ROOT \
        PORSCHE_AUTH_ACCEPTANCE_LOCK_FILE \
        PORSCHE_AUTH_ACCEPTANCE_MANIFEST_DIR
fi

(( $# == 0 )) || { echo 'auth-acceptance-deploy.sh accepts no arguments' >&2; exit 64; }
[[ "$(id -u)" == 0 ]] || { echo 'auth-acceptance-deploy.sh must run as root' >&2; exit 1; }

BACKEND_DIR=/opt/Porsche
FRONTEND_DIR=/opt/Porsche-Web
FRONTEND_ROOT=/var/www/porsche-web
NETWORK=porsche-app
APP_NAME=ai-gateway-go
IMAGE_NAME=ai-gateway-go:auth-acceptance
BACKEND_BRANCH=feature/user-registration-management
FRONTEND_BRANCH=feature/session-auth-frontend
LOCK_FILE=/var/lock/porsche-auth-acceptance.deploy.lock
MANIFEST_DIR=/var/lib/porsche-auth-acceptance
if [[ "${PORSCHE_AUTH_ACCEPTANCE_TEST_MODE:-0}" == 1 ]]; then
    BACKEND_DIR="${PORSCHE_AUTH_ACCEPTANCE_BACKEND_DIR:?}"
    FRONTEND_DIR="${PORSCHE_AUTH_ACCEPTANCE_FRONTEND_DIR:?}"
    FRONTEND_ROOT="${PORSCHE_AUTH_ACCEPTANCE_FRONTEND_ROOT:?}"
    LOCK_FILE="${PORSCHE_AUTH_ACCEPTANCE_LOCK_FILE:?}"
    MANIFEST_DIR="${PORSCHE_AUTH_ACCEPTANCE_MANIFEST_DIR:?}"
fi

reject_root_bootstrap_env_keys() {
    local env_file="$1" key
    # This is deliberately a literal deny guard, not an .env parser. Every
    # line mentioning a Root bootstrap key is rejected without exposing it.
    for key in ROOT_BOOTSTRAP_USERNAME ROOT_BOOTSTRAP_PASSWORD; do
        if ! awk -v key="$key" 'index($0, key) { exit 1 }' "$env_file" >/dev/null 2>&1; then
            echo "$key is not allowed in the application .env" >&2
            return 1
        fi
    done
}

scan_container_root_bootstrap_env() {
    local container_name="$1" pipeline_status
    if docker container inspect "$container_name" --format '{{range .Config.Env}}{{println .}}{{end}}' | grep -Eq '^ROOT_BOOTSTRAP_'; then
        pipeline_status=("${PIPESTATUS[@]}")
    else
        pipeline_status=("${PIPESTATUS[@]}")
    fi
    if [[ "${pipeline_status[0]:-1}" == 0 && "${pipeline_status[1]:-1}" == 1 ]]; then
        return 0
    fi
    if [[ "${pipeline_status[0]:-1}" == 0 && "${pipeline_status[1]:-1}" == 0 ]]; then
        echo "container $container_name has a forbidden ROOT_BOOTSTRAP_ environment key" >&2
    else
        echo "cannot inspect ROOT_BOOTSTRAP_ environment keys for container $container_name" >&2
    fi
    return 1
}

resolve_container_id() {
    local container_name="$1" container_id
    container_id="$(docker container inspect "$container_name" --format '{{.Id}}')" || {
        echo "cannot resolve immutable container ID for $container_name" >&2
        return 1
    }
    [[ "$container_id" =~ ^[0-9a-f]{64}$ ]] || {
        echo "invalid immutable container ID for $container_name" >&2
        return 1
    }
    printf '%s\n' "$container_id"
}

scan_relevant_container_root_bootstrap_envs() {
    local container_names container_name
    if ! container_names="$(docker ps -a --format '{{.Names}}')"; then
        echo 'cannot list application containers for ROOT_BOOTSTRAP_ environment scan' >&2
        return 1
    fi
    while IFS= read -r container_name; do
        if [[ "$container_name" == "$APP_NAME" || "$container_name" =~ ^ai-gateway-go-acceptance-rollback-[0-9]+$ ]]; then
            scan_container_root_bootstrap_env "$container_name" || return 1
        fi
    done <<<"$container_names"
}

read_env_value() {
    local key="$1"
    sed -n "s/^${key}=//p" "$ENV_FILE" | tail -n 1
}

[[ -f "$BACKEND_DIR/.env" && ! -L "$BACKEND_DIR/.env" ]] || { echo "backend .env must be a regular non-symlink file" >&2; exit 1; }
reject_root_bootstrap_env_keys "$BACKEND_DIR/.env"

exec 9>"$LOCK_FILE"
flock -n 9 || { echo 'another auth acceptance deployment is running' >&2; exit 1; }
scan_relevant_container_root_bootstrap_envs

ENV_FILE=''
env_snapshot_dir=''
stage_dir=''
build_context=''
rollback_name=''
rollback_static=''
old_container_id=''
manifest=''
old_renamed=0
candidate_started=0
candidate_container_id=''
static_changed=0
cleanup() {
    local status=$?
    if (( status != 0 )); then
        (( candidate_started == 0 )) || docker rm -f -- "$candidate_container_id" >/dev/null 2>&1 || true
        if (( old_renamed )); then
            docker rename -- "$old_container_id" "$APP_NAME" >/dev/null 2>&1 || true
            docker start -- "$old_container_id" >/dev/null 2>&1 || true
        fi
        if (( static_changed )) && [[ -n "$rollback_static" ]]; then
            rsync --archive --delete --delay-updates "$rollback_static/" "$FRONTEND_ROOT/" || true
            systemctl reload nginx || true
        fi
        [[ -z "$manifest" ]] || rm -f -- "$manifest"
    fi
    [[ -z "$stage_dir" ]] || rm -rf -- "$stage_dir"
    if [[ "$build_context" =~ ^/tmp/porsche-auth-build\.[A-Za-z0-9]+$ && -d "$build_context" && ! -L "$build_context" ]]; then
        rm -rf -- "$build_context"
    fi
    if [[ "$env_snapshot_dir" =~ ^/tmp/porsche-auth-env\.[A-Za-z0-9]+$ && -d "$env_snapshot_dir" && ! -L "$env_snapshot_dir" ]]; then
        rm -rf -- "$env_snapshot_dir"
    fi
    exit "$status"
}
trap cleanup EXIT

snapshot_candidate="$(mktemp -d /tmp/porsche-auth-env.XXXXXX)"
[[ "$snapshot_candidate" =~ ^/tmp/porsche-auth-env\.[A-Za-z0-9]+$ && -d "$snapshot_candidate" && ! -L "$snapshot_candidate" ]] || {
    echo 'environment snapshot directory is invalid' >&2
    exit 1
}
env_snapshot_dir="$snapshot_candidate"
chmod 700 "$env_snapshot_dir"
ENV_FILE="$env_snapshot_dir/.env"
cp --no-dereference -- "$BACKEND_DIR/.env" "$ENV_FILE"
[[ -f "$ENV_FILE" && ! -L "$ENV_FILE" ]] || { echo 'environment snapshot must be a regular non-symlink file' >&2; exit 1; }
reject_root_bootstrap_env_keys "$ENV_FILE"

check_checkout() {
    local dir="$1" branch="$2" current local_sha remote_sha status_output
    cd "$dir"
    current="$(git branch --show-current)"
    [[ "$current" == "$branch" ]] || { echo "unexpected branch in $dir: $current" >&2; return 1; }
    status_output="$(git status --porcelain --untracked-files=no)" || { echo "cannot inspect tracked changes in $dir" >&2; return 1; }
    [[ -z "$status_output" ]] || { echo "tracked changes in $dir" >&2; return 1; }
    git fetch origin "$branch"
    local_sha="$(git rev-parse HEAD)"
    remote_sha="$(git rev-parse "origin/$branch")"
    [[ "$local_sha" == "$remote_sha" ]] || { echo "remote SHA mismatch in $dir" >&2; return 1; }
}

check_checkout "$BACKEND_DIR" "$BACKEND_BRANCH"
backend_sha="$(cd "$BACKEND_DIR" && git rev-parse HEAD)"
check_checkout "$FRONTEND_DIR" "$FRONTEND_BRANCH"
frontend_sha="$(cd "$FRONTEND_DIR" && git rev-parse HEAD)"
[[ "$(read_env_value APP_ENV)" == production ]] || { echo 'APP_ENV must be production' >&2; exit 1; }
[[ "$(read_env_value ALLOWED_HOSTS)" =~ (^|,)aiportcloud\.com(,|$) ]] || { echo 'ALLOWED_HOSTS must contain aiportcloud.com' >&2; exit 1; }
[[ "$(read_env_value AUTH_TRUSTED_ORIGINS)" =~ (^|,)https://aiportcloud\.com(,|$) ]] || { echo 'AUTH_TRUSTED_ORIGINS must contain https://aiportcloud.com' >&2; exit 1; }
[[ -n "$(read_env_value REDIS_URL)" ]] || { echo 'REDIS_URL is required' >&2; exit 1; }
docker network inspect "$NETWORK" >/dev/null
docker container inspect porsche-redis >/dev/null
nginx -t

cd "$FRONTEND_DIR"
npm install --package-lock=false
npm run build
stage_dir="$(mktemp -d "${TMPDIR:-/tmp}/porsche-auth-stage.XXXXXX")"
chmod 700 "$stage_dir"
cp -a "$FRONTEND_DIR/dist/." "$stage_dir/"
build_context="$(mktemp -d /tmp/porsche-auth-build.XXXXXX)"
[[ "$build_context" =~ ^/tmp/porsche-auth-build\.[A-Za-z0-9]+$ && -d "$build_context" && ! -L "$build_context" ]] || {
    echo 'build context directory is invalid' >&2
    exit 1
}
chmod 700 "$build_context"
# The context comes only from the commit verified above.  This excludes live
# worktree additions (including ignored files and .env); .dockerignore is part
# of that commit and therefore retains its normal Docker semantics.
if ! (cd "$BACKEND_DIR" && git archive --format=tar "$backend_sha") | tar -xf - -C "$build_context" --no-same-owner; then
    echo 'cannot create verified backend build context' >&2
    exit 1
fi
# Runtime configuration is supplied only through the private snapshot below;
# never permit a tracked historical .env to become a Docker build input.
rm -f -- "$build_context/.env"
[[ -f "$build_context/Dockerfile" && ! -L "$build_context/Dockerfile" ]] || {
    echo 'verified backend build context is missing Dockerfile' >&2
    exit 1
}
docker build --tag "$IMAGE_NAME" "$build_context"

# Re-scan immediately before the destructive swap, then bind the current
# application to its immutable ID so a name replacement cannot redirect it.
scan_relevant_container_root_bootstrap_envs
if docker container inspect "$APP_NAME" >/dev/null 2>&1; then
    old_container_id="$(resolve_container_id "$APP_NAME")"
    scan_container_root_bootstrap_env "$old_container_id"
fi

mkdir -p "$MANIFEST_DIR"
chmod 700 "$MANIFEST_DIR"
rollback_name="${APP_NAME}-acceptance-rollback-$(date +%s)"
manifest="$MANIFEST_DIR/rollback.env"

if [[ -n "$old_container_id" ]]; then
    docker stop -- "$old_container_id"
    docker rename -- "$old_container_id" "$rollback_name"
    old_renamed=1
fi
candidate_run_output="$(docker run -d --name "$APP_NAME" --restart unless-stopped --network "$NETWORK" \
    --env-file "$ENV_FILE" -p 127.0.0.1:8000:8000 "$IMAGE_NAME")"
if [[ "$candidate_run_output" =~ ^[0-9a-f]{64}$ ]]; then
    candidate_container_id="$candidate_run_output"
    candidate_started=1
else
    # docker run may have created the named container before returning a
    # malformed response. Resolve the trusted name rather than ever using the
    # untrusted output as a mutation target, then let EXIT recover by ID.
    candidate_container_id="$(resolve_container_id "$APP_NAME")" || {
        echo 'Docker run returned an invalid immutable candidate container ID' >&2
        exit 1
    }
    scan_container_root_bootstrap_env "$candidate_container_id"
    candidate_started=1
    echo 'Docker run returned an invalid immutable candidate container ID' >&2
    exit 1
fi
healthy=0
for _ in $(seq 1 30); do
    if curl --fail --silent --show-error --connect-timeout 2 --max-time 3 \
        -H 'Host: aiportcloud.com' http://127.0.0.1:8000/health >/dev/null; then healthy=1; break; fi
    sleep 1
done
(( healthy == 1 )) || { echo 'candidate health check failed' >&2; exit 1; }

rollback_static="$(mktemp -d "$MANIFEST_DIR/static.XXXXXX")"
cp -a "$FRONTEND_ROOT/." "$rollback_static/"
umask 077
printf 'ROLLBACK_CONTAINER=%s\nROLLBACK_STATIC=%s\nBACKEND_SHA=%s\nFRONTEND_SHA=%s\n' \
    "$rollback_name" "$rollback_static" "$backend_sha" "$frontend_sha" >"$manifest"
chmod 600 "$manifest"
static_changed=1
rsync --archive --delete --delay-updates "$stage_dir/" "$FRONTEND_ROOT/"
systemctl reload nginx
trap - EXIT
rm -rf -- "$stage_dir"
rm -rf -- "$build_context"
if [[ "$env_snapshot_dir" =~ ^/tmp/porsche-auth-env\.[A-Za-z0-9]+$ && -d "$env_snapshot_dir" && ! -L "$env_snapshot_dir" ]]; then
    rm -rf -- "$env_snapshot_dir"
fi
echo 'auth acceptance candidate deployed; database migration is not automatically rolled back'
