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
        "$resolved_entrypoint" == /fixture/Porsche/deploy/auth-acceptance-migrate.sh &&
        -f /.dockerenv && ! -S /var/run/docker.sock ]] || reject_untrusted_test_mode
    for override_name in "$@"; do
        override_value="${!override_name:-}"
        [[ "$override_value" == /fixture/* ]] || reject_untrusted_test_mode
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
