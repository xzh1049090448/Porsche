#!/usr/bin/env bash
# Behavioural regression checks for the production deployment safety contract.
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
script_file="$script_dir/production-deploy.sh"
repo_dir="$(cd -- "$script_dir/.." && pwd)"

if [[ ! -f "$script_file" ]]; then
    echo "missing deployment script: $script_file" >&2
    exit 1
fi

if [[ -e "$repo_dir/.env" ]]; then
    echo "refusing to replace existing .env while testing deployment behaviour" >&2
    exit 1
fi

fixture_dir="$(mktemp -d "${TMPDIR:-/tmp}/production-deploy-test.XXXXXX")"
mock_dir="$fixture_dir/bin"
command_log="$fixture_dir/commands.log"
mkdir -p "$mock_dir"
: >"$command_log"
printf 'DATABASE_URL=mysql://test:secret@db/test\n' >"$repo_dir/.env"

cleanup() {
    rm -f -- "$repo_dir/.env"
    rm -rf -- "$fixture_dir"
}
trap cleanup EXIT

write_mock() {
    local command_name="$1"

    {
        printf '%s\n' '#!/usr/bin/env bash'
        printf '%s\n' 'set -Eeuo pipefail'
        printf '%s\n' 'printf "'"$command_name"'" >>"$COMMAND_LOG"'
        printf '%s\n' 'printf "\\t%s" "$@" >>"$COMMAND_LOG"'
        printf '%s\n' 'printf "\\n" >>"$COMMAND_LOG"'
        printf '%s\n' 'case "'"$command_name"'" in'
        printf '%s\n' '  git)'
        printf '%s\n' '    if [[ "${1:-}" == "rev-parse" && "${2:-}" == "--is-inside-work-tree" ]]; then'
        printf '%s\n' '      printf "true\\n"'
        printf '%s\n' '    elif [[ "${1:-}" == "rev-parse" ]]; then'
        printf '%s\n' '      printf "test-revision\\n"'
        printf '%s\n' '    fi'
        printf '%s\n' '    ;;'
        printf '%s\n' '  docker)'
        printf '%s\n' '    if [[ "${1:-}" == "run" ]]; then printf "candidate-container-id\\n"; fi'
        printf '%s\n' '    ;;'
        printf '%s\n' '  curl)'
        printf '%s\n' '    [[ "${MOCK_HEALTH_RESULT:-success}" == success ]]'
        printf '%s\n' '    ;;'
        printf '%s\n' 'esac'
    } >"$mock_dir/$command_name"
    chmod +x "$mock_dir/$command_name"
}

write_mock git
write_mock docker
write_mock curl
write_mock sleep

run_deploy() {
    : >"$command_log"
    PATH="$mock_dir:$PATH" COMMAND_LOG="$command_log" \
        APP_NAME=existing-app IMAGE_NAME=test-image HOST_PORT=18000 \
        "$script_file"
}

line_for() {
    local expression="$1"
    awk -F '\t' "$expression { print NR; exit }" "$command_log"
}

require_line() {
    local label="$1"
    local expression="$2"
    local line
    line="$(line_for "$expression")"
    if [[ -z "$line" ]]; then
        echo "missing expected command: $label" >&2
        exit 1
    fi
    printf '%s\n' "$line"
}

assert_after() {
    local earlier_label="$1"
    local earlier_line="$2"
    local later_label="$3"
    local later_line="$4"
    if (( earlier_line >= later_line )); then
        echo "$later_label must run after $earlier_label" >&2
        exit 1
    fi
}

assert_no_database_destruction() {
    if awk -F '\t' '
        $1 == "docker" && (
            ($2 == "volume" && $3 == "rm") ||
            ($2 == "system" && $3 == "prune") ||
            ($2 == "compose" && $3 == "down" && $4 == "-v")
        ) { exit 1 }
    ' "$command_log"; then
        return
    fi
    echo 'deployment attempted a database or volume destructive command' >&2
    exit 1
}

if ! run_deploy; then
    echo 'deployment must succeed when the candidate health check succeeds' >&2
    exit 1
fi

fetch_line="$(require_line 'git fetch origin main' '$1 == "git" && $2 == "fetch" && $3 == "origin" && $4 == "main"')"
switch_line="$(require_line 'git switch main' '$1 == "git" && ($2 == "switch" || $2 == "checkout") && $3 == "main"')"
reset_line="$(require_line 'git reset --hard origin/main' '$1 == "git" && $2 == "reset" && $3 == "--hard" && $4 == "origin/main"')"
build_line="$(require_line 'docker build' '$1 == "docker" && $2 == "build"')"
candidate_line="$(require_line 'candidate docker run with env file and loopback publish' '
    $1 == "docker" && $2 == "run" {
        has_env = has_publish = 0
        for (i = 3; i <= NF; i++) {
            if ($i == "--env-file" && $(i + 1) == ".env") has_env = 1
            if ($i == "--publish" && $(i + 1) == "127.0.0.1:18000:8000") has_publish = 1
        }
        if (has_env && has_publish) { print NR; exit }
    }')"
health_line="$(require_line 'loopback health check' '$1 == "curl" { for (i = 2; i <= NF; i++) if ($i == "http://127.0.0.1:18000/health") { print NR; exit } }')"
remove_old_line="$(require_line 'old application container removal' '$1 == "docker" && $2 == "rm" && $3 == "-f" && $4 == "existing-app"')"
rename_line="$(require_line 'candidate rename to application name' '$1 == "docker" && $2 == "rename" && $NF == "existing-app"')"

assert_after 'fetch' "$fetch_line" 'switch' "$switch_line"
assert_after 'switch' "$switch_line" 'reset' "$reset_line"
assert_after 'reset' "$reset_line" 'build' "$build_line"
assert_after 'build' "$build_line" 'candidate start' "$candidate_line"
assert_after 'candidate start' "$candidate_line" 'health check' "$health_line"
assert_after 'health check' "$health_line" 'old application removal' "$remove_old_line"
assert_after 'old application removal' "$remove_old_line" 'candidate rename' "$rename_line"
assert_no_database_destruction

if MOCK_HEALTH_RESULT=failure run_deploy; then
    echo 'deployment must fail when the candidate health check fails' >&2
    exit 1
fi

if [[ -n "$(line_for '$1 == "docker" && $2 == "rm" && $3 == "-f" && $4 == "existing-app"')" ]]; then
    echo 'deployment removed the old application container after candidate health failure' >&2
    exit 1
fi
assert_no_database_destruction
