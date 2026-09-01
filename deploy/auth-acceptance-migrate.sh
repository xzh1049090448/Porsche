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
        "$resolved_entrypoint" == /fixture/Porsche/deploy/auth-acceptance-migrate.sh &&
        -f /.dockerenv && ! -e /var/run/docker.sock && ! -L /var/run/docker.sock ]] || reject_untrusted_test_mode
    for override_name in "$@"; do
        canonicalize_fixture_override "$override_name"
    done
}

if [[ "${PORSCHE_AUTH_ACCEPTANCE_TEST_MODE:-0}" == 1 ]]; then
    validate_test_mode_binding PORSCHE_AUTH_ACCEPTANCE_BACKEND_DIR
fi

[[ $# == 1 && "$1" == --confirm-auth-schema-migration ]] || {
    echo 'usage: auth-acceptance-migrate.sh --confirm-auth-schema-migration' >&2
    exit 64
}
[[ "$(id -u)" == 0 ]] || { echo 'auth-acceptance-migrate.sh must run as root' >&2; exit 1; }

backend_dir=/opt/Porsche
if [[ "${PORSCHE_AUTH_ACCEPTANCE_TEST_MODE:-0}" == 1 ]]; then
    backend_dir="${PORSCHE_AUTH_ACCEPTANCE_BACKEND_DIR:?test backend directory is required}"
fi
[[ -f "$backend_dir/.env" ]] || { echo "missing $backend_dir/.env" >&2; exit 1; }
reject_root_bootstrap_env_keys() {
    local env_file="$1"
    # Treat this as text, rather than parsing assignments: empty declarations,
    # comments, exports, and future ROOT_BOOTSTRAP_* keys are all forbidden.
    if ! awk 'index($0, "ROOT_BOOTSTRAP_") { exit 1 }' "$env_file" >/dev/null 2>&1; then
        echo 'ROOT_BOOTSTRAP_ keys are not allowed in the application .env' >&2
        return 1
    fi
}
reject_root_bootstrap_env_keys "$backend_dir/.env"
docker network inspect porsche-app >/dev/null

cd "$backend_dir"
branch="$(git branch --show-current)"
[[ "$branch" == feature/user-registration-management ]] || { echo "refusing migration from branch: $branch" >&2; exit 1; }
git fetch origin feature/user-registration-management
local_sha="$(git rev-parse HEAD)"
remote_sha="$(git rev-parse origin/feature/user-registration-management)"
[[ "$local_sha" == "$remote_sha" ]] || { echo 'backend checkout does not match its remote feature branch' >&2; exit 1; }

docker run --rm --env-file "$backend_dir/.env" --network porsche-app \
    -v "$backend_dir:/src:ro" -w /src golang:1.22-alpine \
    sh -ec 'go run ./cmd/migrate up'
