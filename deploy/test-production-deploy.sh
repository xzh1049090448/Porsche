#!/usr/bin/env bash
# Static regression checks for the production deployment safety contract.
set -Eeuo pipefail

script_file="$(dirname "$0")/production-deploy.sh"

require_contains() {
    local expected="$1"

    if ! grep -Fq -- "$expected" "$script_file"; then
        echo "missing required deployment command: $expected" >&2
        exit 1
    fi
}

reject_contains() {
    local forbidden="$1"

    if grep -Fq -- "$forbidden" "$script_file"; then
        echo "unsafe deployment command: $forbidden" >&2
        exit 1
    fi
}

if [[ ! -f "$script_file" ]]; then
    echo "missing deployment script: $script_file" >&2
    exit 1
fi

require_contains 'git fetch origin main'
require_contains 'git reset --hard origin/main'
require_contains '--env-file .env'
require_contains '127.0.0.1:${HOST_PORT}:8000'
require_contains 'curl -fsS http://127.0.0.1:${HOST_PORT}/health'

reject_contains 'docker volume rm'
reject_contains 'system prune'
reject_contains 'compose down -v'

candidate_start="$(grep -nF -- 'docker run -d' "$script_file" | sed -n '1p')"
replacement_start="$(grep -nF -- 'docker rm -f' "$script_file" | sed -n '1p')"

if [[ -z "$candidate_start" || -z "$replacement_start" ]]; then
    echo 'deployment must start a candidate and then replace the old container' >&2
    exit 1
fi

candidate_line="${candidate_start%%:*}"
replacement_line="${replacement_start%%:*}"
if (( candidate_line >= replacement_line )); then
    echo 'candidate container must start before the old container is removed' >&2
    exit 1
fi
