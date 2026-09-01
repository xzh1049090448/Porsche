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
}

validate_credentials_file() {
    local owner mode line value line_count=0 username_count=0 password_count=0
    [[ -f "$CREDENTIALS_FILE" && ! -L "$CREDENTIALS_FILE" ]] || {
        echo 'root bootstrap credentials must be a regular non-symlink file' >&2
        return 1
    }
    owner="$(stat -c '%u' -- "$CREDENTIALS_FILE")"
    mode="$(stat -c '%a' -- "$CREDENTIALS_FILE")"
    [[ "$owner" =~ ^[0-9]+$ && "$owner" == 0 ]] || {
        echo 'root bootstrap credentials must be owned by numeric uid 0' >&2
        return 1
    }
    [[ "$mode" == 600 ]] || {
        echo 'root bootstrap credentials must have mode 600' >&2
        return 1
    }

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
    done <"$CREDENTIALS_FILE"
    (( line_count == 2 && username_count == 1 && password_count == 1 )) || {
        echo 'root bootstrap credentials must contain username and password exactly once' >&2
        return 1
    }
}

env_has_nonempty_value() {
    local key="$1" line
    while IFS= read -r line || [[ -n "$line" ]]; do
        case "$line" in
            "${key}="*) [[ -z "${line#*=}" ]] || return 0 ;;
        esac
    done <"$BACKEND_DIR/.env"
    return 1
}

[[ -f "$BACKEND_DIR/.env" && ! -L "$BACKEND_DIR/.env" ]] || {
    echo 'backend .env must be a regular non-symlink file' >&2
    exit 1
}
check_checkout
validate_credentials_file
if env_has_nonempty_value ROOT_BOOTSTRAP_USERNAME; then
    echo 'ROOT_BOOTSTRAP_USERNAME must be empty or missing in backend .env' >&2
    exit 1
fi
if env_has_nonempty_value ROOT_BOOTSTRAP_PASSWORD; then
    echo 'ROOT_BOOTSTRAP_PASSWORD must be empty or missing in backend .env' >&2
    exit 1
fi
docker network inspect "$NETWORK" >/dev/null
docker container inspect "$MYSQL_CONTAINER" >/dev/null

docker build --tag "$IMAGE_NAME" "$BACKEND_DIR"
docker run --rm --network "$NETWORK" \
    --mount "type=bind,src=$BACKEND_DIR/.env,dst=/app/.env,readonly" \
    --mount "type=bind,src=$CREDENTIALS_FILE,dst=/run/secrets/root-bootstrap,readonly" \
    "$IMAGE_NAME" /app/bootstrap-root --credentials-file /run/secrets/root-bootstrap
