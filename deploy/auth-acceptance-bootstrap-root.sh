#!/usr/bin/env bash
set -Eeuo pipefail

reject_untrusted_test_mode() {
    echo 'test mode is restricted to isolated fixture container' >&2
    exit 1
}

validate_test_mode_binding() {
    local resolved_entrypoint override_name override_value
    resolved_entrypoint="$(readlink -f -- "${BASH_SOURCE[0]}" 2>/dev/null || true)"
    [[ "${PORSCHE_AUTH_ACCEPTANCE_TEST_CONTAINER:-}" == 1 &&
        "$resolved_entrypoint" == /fixture/Porsche/deploy/auth-acceptance-bootstrap-root.sh &&
        -f /.dockerenv && ! -S /var/run/docker.sock ]] || reject_untrusted_test_mode
    for override_name in "$@"; do
        override_value="${!override_name:-}"
        [[ "$override_value" == /fixture/* ]] || reject_untrusted_test_mode
    done
}

if [[ "${PORSCHE_AUTH_ACCEPTANCE_TEST_MODE:-0}" == 1 ]]; then
    validate_test_mode_binding \
        PORSCHE_AUTH_ACCEPTANCE_BACKEND_DIR \
        PORSCHE_AUTH_ACCEPTANCE_ROOT_CREDENTIALS_FILE
fi

[[ $# == 1 && "$1" == --confirm-auth-root-bootstrap ]] || {
    echo 'usage: auth-acceptance-bootstrap-root.sh --confirm-auth-root-bootstrap' >&2
    exit 64
}
[[ "$(id -u)" == 0 ]] || { echo 'auth-acceptance-bootstrap-root.sh must run as root' >&2; exit 1; }

BACKEND_DIR=/opt/Porsche
CREDENTIALS_FILE=/var/lib/porsche-auth-acceptance/root-acceptance-credentials
NETWORK=porsche-app
MYSQL_CONTAINER=porsche-mysql
IMAGE_NAME=ai-gateway-go:auth-acceptance
BACKEND_BRANCH=feature/user-registration-management
if [[ "${PORSCHE_AUTH_ACCEPTANCE_TEST_MODE:-0}" == 1 ]]; then
    BACKEND_DIR="${PORSCHE_AUTH_ACCEPTANCE_BACKEND_DIR:?test backend directory is required}"
    CREDENTIALS_FILE="${PORSCHE_AUTH_ACCEPTANCE_ROOT_CREDENTIALS_FILE:?test credentials file is required}"
fi

snapshot_dir="$(mktemp -d /tmp/porsche-root-bootstrap.XXXXXX)"
cleanup() {
    local status=$?
    trap - EXIT
    if [[ "$snapshot_dir" =~ ^/tmp/porsche-root-bootstrap\.[A-Za-z0-9]+$ && -d "$snapshot_dir" && ! -L "$snapshot_dir" ]]; then
        rm -rf -- "$snapshot_dir"
    fi
    exit "$status"
}
trap cleanup EXIT
chmod 700 "$snapshot_dir"
snapshot_env="$snapshot_dir/.env"
snapshot_credentials="$snapshot_dir/root-bootstrap"

check_checkout() {
    local current local_sha remote_sha status_output
    cd "$BACKEND_DIR"
    current="$(git branch --show-current)"
    [[ "$current" == "$BACKEND_BRANCH" ]] || { echo "unexpected backend branch: $current" >&2; return 1; }
    status_output="$(git status --porcelain --untracked-files=no)" || { echo 'cannot inspect tracked backend changes' >&2; return 1; }
    [[ -z "$status_output" ]] || { echo 'tracked backend changes present' >&2; return 1; }
    git fetch origin "$BACKEND_BRANCH"
    local_sha="$(git rev-parse HEAD)"
    remote_sha="$(git rev-parse "origin/$BACKEND_BRANCH")"
    [[ "$local_sha" == "$remote_sha" ]] || { echo 'backend checkout does not match its remote feature branch' >&2; return 1; }
    backend_sha="$local_sha"
}

validate_root_file_metadata() {
    local path="$1" label="$2" owner mode
    [[ -f "$path" && ! -L "$path" ]] || {
        echo "$label must be a regular non-symlink file" >&2
        return 1
    }
    owner="$(stat -c '%u' -- "$path")"
    mode="$(stat -c '%a' -- "$path")"
    [[ "$owner" =~ ^[0-9]+$ && "$owner" == 0 ]] || {
        echo "$label must be owned by numeric uid 0" >&2
        return 1
    }
    [[ "$mode" == 600 ]] || {
        echo "$label must have mode 600" >&2
        return 1
    }
}

validate_root_controlled_directory() {
    local path="$1" label="$2" owner mode group_digit other_digit
    [[ -d "$path" && ! -L "$path" ]] || {
        echo "$label must be a regular non-symlink directory" >&2
        return 1
    }
    owner="$(stat -c '%u' -- "$path")"
    mode="$(stat -c '%a' -- "$path")"
    [[ "$owner" =~ ^[0-9]+$ && "$owner" == 0 ]] || {
        echo "$label must be owned by numeric uid 0" >&2
        return 1
    }
    [[ "$mode" =~ ^[0-7]{3,4}$ ]] || {
        echo "$label has an invalid mode" >&2
        return 1
    }
    group_digit="${mode: -2:1}"
    other_digit="${mode: -1}"
    (( (8#$group_digit & 2) == 0 && (8#$other_digit & 2) == 0 )) || {
        echo "$label must not be group- or world-writable" >&2
        return 1
    }
}

validate_credentials_content() {
    local path="$1" line value line_count=0 username_count=0 password_count=0
    while IFS= read -r line || [[ -n "$line" ]]; do
        ((line_count += 1))
        [[ -n "$line" ]] || { echo 'invalid root bootstrap credentials format' >&2; return 1; }
        case "$line" in
            username=*)
                ((username_count += 1))
                value="${line#username=}"
                ;;
            password=*)
                ((password_count += 1))
                value="${line#password=}"
                ;;
            *)
                echo 'invalid root bootstrap credentials format' >&2
                return 1
                ;;
        esac
        [[ -n "$value" && "$value" != [[:space:]]* && "$value" != *[[:space:]] ]] || {
            echo 'invalid root bootstrap credentials value' >&2
            return 1
        }
    done <"$path"
    (( line_count == 2 && username_count == 1 && password_count == 1 )) || {
        echo 'root bootstrap credentials must contain username and password exactly once' >&2
        return 1
    }
}

validate_snapshot_env() {
    local line
    while IFS= read -r line || [[ -n "$line" ]]; do
        case "$line" in
            *ROOT_BOOTSTRAP_USERNAME*)
                echo 'backend .env contains a forbidden ROOT_BOOTSTRAP_USERNAME declaration' >&2
                return 1
                ;;
            *ROOT_BOOTSTRAP_PASSWORD*)
                echo 'backend .env contains a forbidden ROOT_BOOTSTRAP_PASSWORD declaration' >&2
                return 1
                ;;
        esac
    done <"$snapshot_env"
}

validate_root_controlled_directory "$BACKEND_DIR" 'backend directory'
validate_root_file_metadata "$BACKEND_DIR/.env" 'backend .env'
check_checkout
credentials_parent="$(dirname -- "$CREDENTIALS_FILE")"
validate_root_controlled_directory "$credentials_parent" 'root bootstrap credentials parent'
validate_root_file_metadata "$CREDENTIALS_FILE" 'root bootstrap credentials'
umask 077
cp --preserve=mode,ownership --no-dereference -- "$BACKEND_DIR/.env" "$snapshot_env"
cp --preserve=mode,ownership --no-dereference -- "$CREDENTIALS_FILE" "$snapshot_credentials"
validate_root_file_metadata "$snapshot_env" 'snapshot .env'
validate_root_file_metadata "$snapshot_credentials" 'snapshot credentials'
validate_credentials_content "$snapshot_credentials"
validate_snapshot_env
docker network inspect "$NETWORK" >/dev/null
docker container inspect "$MYSQL_CONTAINER" >/dev/null

image_id="$(git archive --format=tar "$backend_sha" | docker build --quiet --tag "$IMAGE_NAME" -)"
[[ "$image_id" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo 'Docker build did not return an immutable image id' >&2; exit 1; }
docker run --rm --network "$NETWORK" \
    --mount "type=bind,src=$snapshot_env,dst=/app/.env,readonly" \
    --mount "type=bind,src=$snapshot_credentials,dst=/run/secrets/root-bootstrap,readonly" \
    "$image_id" /app/bootstrap-root --credentials-file /run/secrets/root-bootstrap
