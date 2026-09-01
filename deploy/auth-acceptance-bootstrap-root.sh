#!/usr/bin/env bash
set -Eeuo pipefail

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

snapshot_dir="$(mktemp -d "${TMPDIR:-/tmp}/porsche-auth-root-bootstrap.XXXXXX")"
cleanup() {
    local status=$?
    trap - EXIT
    rm -rf -- "$snapshot_dir"
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

validate_credentials_metadata() {
    local path="$1" owner mode
    [[ -f "$path" && ! -L "$path" ]] || {
        echo 'root bootstrap credentials must be a regular non-symlink file' >&2
        return 1
    }
    owner="$(stat -c '%u' -- "$path")"
    mode="$(stat -c '%a' -- "$path")"
    [[ "$owner" =~ ^[0-9]+$ && "$owner" == 0 ]] || {
        echo 'root bootstrap credentials must be owned by numeric uid 0' >&2
        return 1
    }
    [[ "$mode" == 600 ]] || {
        echo 'root bootstrap credentials must have mode 600' >&2
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
                [[ "$line" == ROOT_BOOTSTRAP_USERNAME= ]] || {
                    echo 'backend .env contains a forbidden ROOT_BOOTSTRAP_USERNAME declaration' >&2
                    return 1
                }
                ;;
            *ROOT_BOOTSTRAP_PASSWORD*)
                [[ "$line" == ROOT_BOOTSTRAP_PASSWORD= ]] || {
                    echo 'backend .env contains a forbidden ROOT_BOOTSTRAP_PASSWORD declaration' >&2
                    return 1
                }
                ;;
        esac
    done <"$snapshot_env"
}

[[ -f "$BACKEND_DIR/.env" && ! -L "$BACKEND_DIR/.env" ]] || {
    echo 'backend .env must be a regular non-symlink file' >&2
    exit 1
}
check_checkout
validate_credentials_metadata "$CREDENTIALS_FILE"
umask 077
cp --no-dereference -- "$BACKEND_DIR/.env" "$snapshot_env"
cp --no-dereference -- "$CREDENTIALS_FILE" "$snapshot_credentials"
[[ -f "$snapshot_env" && ! -L "$snapshot_env" ]] || { echo 'snapshot .env is not a regular file' >&2; exit 1; }
[[ -f "$snapshot_credentials" && ! -L "$snapshot_credentials" ]] || { echo 'snapshot credentials are not a regular file' >&2; exit 1; }
chmod 600 "$snapshot_credentials"
validate_credentials_metadata "$snapshot_credentials"
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
