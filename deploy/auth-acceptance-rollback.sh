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
        "$resolved_entrypoint" == /fixture/Porsche/deploy/auth-acceptance-rollback.sh &&
        -f /.dockerenv && ! -e /var/run/docker.sock && ! -L /var/run/docker.sock ]] || reject_untrusted_test_mode
    for override_name in "$@"; do
        canonicalize_fixture_override "$override_name"
    done
}

if [[ "${PORSCHE_AUTH_ACCEPTANCE_TEST_MODE:-0}" == 1 ]]; then
    validate_test_mode_binding \
        PORSCHE_AUTH_ACCEPTANCE_FRONTEND_ROOT \
        PORSCHE_AUTH_ACCEPTANCE_MANIFEST_DIR \
        PORSCHE_AUTH_ACCEPTANCE_LOCK_FILE
fi

[[ $# == 1 && "$1" == --confirm-auth-acceptance-rollback ]] || {
    echo 'usage: auth-acceptance-rollback.sh --confirm-auth-acceptance-rollback' >&2; exit 64;
}
[[ "$(id -u)" == 0 ]] || { echo 'auth-acceptance-rollback.sh must run as root' >&2; exit 1; }
FRONTEND_ROOT=/var/www/porsche-web
MANIFEST_DIR=/var/lib/porsche-auth-acceptance
LOCK_FILE=/var/lock/porsche-auth-acceptance.deploy.lock
if [[ "${PORSCHE_AUTH_ACCEPTANCE_TEST_MODE:-0}" == 1 ]]; then
    FRONTEND_ROOT="${PORSCHE_AUTH_ACCEPTANCE_FRONTEND_ROOT:?}"
    MANIFEST_DIR="${PORSCHE_AUTH_ACCEPTANCE_MANIFEST_DIR:?}"
    LOCK_FILE="${PORSCHE_AUTH_ACCEPTANCE_LOCK_FILE:?}"
fi
exec 9>"$LOCK_FILE"
flock -n 9 || { echo 'another auth acceptance deployment is running' >&2; exit 1; }
manifest="$MANIFEST_DIR/rollback.env"
[[ -f "$manifest" ]] || { echo 'rollback manifest is missing' >&2; exit 1; }
[[ "$(stat -c %a "$manifest")" == 600 ]] || { echo 'rollback manifest mode must be 0600' >&2; exit 1; }
[[ "$(stat -c %u "$manifest")" == 0 ]] || { echo 'rollback manifest must be owned by root' >&2; exit 1; }
rollback_container="$(sed -n 's/^ROLLBACK_CONTAINER=//p' "$manifest")"
rollback_static="$(sed -n 's/^ROLLBACK_STATIC=//p' "$manifest")"
[[ "$rollback_container" =~ ^ai-gateway-go-acceptance-rollback-[0-9]+$ ]] || { echo 'invalid rollback container' >&2; exit 1; }
[[ "$(dirname -- "$rollback_static")" == "$MANIFEST_DIR" && "$(basename -- "$rollback_static")" =~ ^static\.[A-Za-z0-9]+$ ]] || { echo 'invalid rollback static path' >&2; exit 1; }

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
    local container_names container_name rollback_seen=0
    if ! container_names="$(docker ps -a --format '{{.Names}}')"; then
        echo 'cannot list application containers for ROOT_BOOTSTRAP_ environment scan' >&2
        return 1
    fi
    while IFS= read -r container_name; do
        if [[ "$container_name" == ai-gateway-go || "$container_name" =~ ^ai-gateway-go-acceptance-rollback-[0-9]+$ ]]; then
            [[ "$container_name" != "$rollback_container" ]] || rollback_seen=1
            scan_container_root_bootstrap_env "$container_name" || return 1
        fi
    done <<<"$container_names"
    if (( rollback_seen == 0 )); then
        scan_container_root_bootstrap_env "$rollback_container" || return 1
    fi
}

scan_relevant_container_root_bootstrap_envs
current_container_id="$(resolve_container_id ai-gateway-go)"
rollback_container_id="$(resolve_container_id "$rollback_container")"
scan_container_root_bootstrap_env "$current_container_id"
scan_container_root_bootstrap_env "$rollback_container_id"

# Keep the pre-rollback application and static tree until the replacement is
# both running and published.  Every recovery mutation uses the ID resolved
# above, so a concurrent name rebind cannot redirect it.
previous_current_name="${rollback_container}-failed-$(date +%s)"
previous_static=''
current_stopped=0
current_renamed=0
rollback_renamed=0
rollback_started=0
static_publish_started=0
rollback_committed=0
recover_rollback_failure() {
    local status=$?
    if (( status != 0 && rollback_committed == 0 )); then
        if (( rollback_renamed )); then
            (( rollback_started == 0 )) || docker stop -- "$rollback_container_id" >/dev/null 2>&1 || true
            docker rename -- "$rollback_container_id" "$rollback_container" >/dev/null 2>&1 || true
        fi
        if (( current_renamed )); then
            docker rename -- "$current_container_id" ai-gateway-go >/dev/null 2>&1 || true
            docker start -- "$current_container_id" >/dev/null 2>&1 || true
        elif (( current_stopped )); then
            docker start -- "$current_container_id" >/dev/null 2>&1 || true
        fi
        if (( static_publish_started )) && [[ -n "$previous_static" ]]; then
            rsync --archive --delete --delay-updates "$previous_static/" "$FRONTEND_ROOT/" || true
            systemctl reload nginx || true
        fi
    fi
    [[ -z "$previous_static" ]] || rm -rf -- "$previous_static"
    exit "$status"
}
trap recover_rollback_failure EXIT

nginx -t
previous_static="$(mktemp -d "$MANIFEST_DIR/rollback-current-static.XXXXXX")"
[[ -d "$previous_static" && ! -L "$previous_static" ]] || { echo 'cannot create rollback static recovery snapshot' >&2; exit 1; }
chmod 700 "$previous_static"
cp -a "$FRONTEND_ROOT/." "$previous_static/"
docker stop -- "$current_container_id"
current_stopped=1
docker rename -- "$current_container_id" "$previous_current_name"
current_renamed=1
docker rename -- "$rollback_container_id" ai-gateway-go
rollback_renamed=1
docker start -- "$rollback_container_id"
rollback_started=1
static_publish_started=1
rsync --archive --delete --delay-updates "$rollback_static/" "$FRONTEND_ROOT/"
systemctl reload nginx
rollback_committed=1
docker rm -f -- "$current_container_id" || echo 'warning: previous application container could not be removed' >&2
rm -f -- "$manifest" || echo 'warning: rollback manifest could not be removed' >&2
rm -rf -- "$previous_static"
previous_static=''
trap - EXIT
echo 'application and frontend rolled back; database migration remains applied'
