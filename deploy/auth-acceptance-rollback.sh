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
        PORSCHE_AUTH_ACCEPTANCE_MANIFEST_DIR
fi

[[ $# == 1 && "$1" == --confirm-auth-acceptance-rollback ]] || {
    echo 'usage: auth-acceptance-rollback.sh --confirm-auth-acceptance-rollback' >&2; exit 64;
}
[[ "$(id -u)" == 0 ]] || { echo 'auth-acceptance-rollback.sh must run as root' >&2; exit 1; }
FRONTEND_ROOT=/var/www/porsche-web
MANIFEST_DIR=/var/lib/porsche-auth-acceptance
if [[ "${PORSCHE_AUTH_ACCEPTANCE_TEST_MODE:-0}" == 1 ]]; then
    FRONTEND_ROOT="${PORSCHE_AUTH_ACCEPTANCE_FRONTEND_ROOT:?}"
    MANIFEST_DIR="${PORSCHE_AUTH_ACCEPTANCE_MANIFEST_DIR:?}"
fi
manifest="$MANIFEST_DIR/rollback.env"
[[ -f "$manifest" ]] || { echo 'rollback manifest is missing' >&2; exit 1; }
[[ "$(stat -c %a "$manifest")" == 600 ]] || { echo 'rollback manifest mode must be 0600' >&2; exit 1; }
[[ "$(stat -c %u "$manifest")" == 0 ]] || { echo 'rollback manifest must be owned by root' >&2; exit 1; }
rollback_container="$(sed -n 's/^ROLLBACK_CONTAINER=//p' "$manifest")"
rollback_static="$(sed -n 's/^ROLLBACK_STATIC=//p' "$manifest")"
[[ "$rollback_container" =~ ^ai-gateway-go-acceptance-rollback-[0-9]+$ ]] || { echo 'invalid rollback container' >&2; exit 1; }
[[ "$(dirname -- "$rollback_static")" == "$MANIFEST_DIR" && "$(basename -- "$rollback_static")" =~ ^static\.[A-Za-z0-9]+$ ]] || { echo 'invalid rollback static path' >&2; exit 1; }
nginx -t
docker stop -- ai-gateway-go
docker rm -f -- ai-gateway-go
docker rename -- "$rollback_container" ai-gateway-go
docker start -- ai-gateway-go
rsync --archive --delete --delay-updates "$rollback_static/" "$FRONTEND_ROOT/"
systemctl reload nginx
rm -f -- "$manifest"
echo 'application and frontend rolled back; database migration remains applied'
