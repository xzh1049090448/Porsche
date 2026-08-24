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

fixture_dir="$(mktemp -d "${TMPDIR:-/tmp}/production-deploy-test.XXXXXX")"
mock_dir="$fixture_dir/bin"
env_file="$fixture_dir/production.env"
command_log="$fixture_dir/commands.nul"
mkdir -p "$mock_dir"
printf 'DATABASE_URL=mysql://test:secret@db/test\n' >"$env_file"

cleanup() { rm -rf -- "$fixture_dir"; }
trap cleanup EXIT

write_mock() {
    local command_name="$1"
    {
        printf '%s\n' '#!/usr/bin/env bash'
        printf '%s\n' 'set -Eeuo pipefail'
        printf '%s\n' 'printf "%s\\0" "'"$command_name"'" "$@" "__END__" >>"$COMMAND_LOG"'
        printf '%s\n' 'case "'"$command_name"'" in'
        printf '%s\n' '  git) case "${1:-}" in rev-parse) printf "true\\n" ;; diff) exit "${MOCK_GIT_DIRTY:-0}" ;; esac ;;'
        printf '%s\n' '  docker) [[ "${1:-}" != run ]] || printf "candidate-container-id\\n" ;;'
        printf '%s\n' '  curl) [[ "${MOCK_HEALTH_RESULT:-success}" == success ]] ;;'
        printf '%s\n' 'esac'
    } >"$mock_dir/$command_name"
    chmod +x "$mock_dir/$command_name"
}

write_mock git; write_mock docker; write_mock curl; write_mock sleep

read_calls() {
    calls=()
    local record current=''
    while IFS= read -r -d '' record; do
        if [[ "$record" == '__END__' ]]; then calls+=("$current"); current=''; else current+="${current:+ }$record"; fi
    done <"$command_log"
}

run_deploy() {
    : >"$command_log"
    PATH="$mock_dir:$PATH" COMMAND_LOG="$command_log" ENV_FILE="$env_file" APP_NAME=existing-app IMAGE_NAME=test-image HOST_PORT="${1:-18000}" APP_DOCKER_NETWORK="${2:-}" "$script_file"
}

line_for() {
    local expression="$1" index
    read_calls
    for index in "${!calls[@]}"; do
        [[ "${calls[$index]}" == *"$expression"* ]] && { printf '%s\n' "$((index + 1))"; return; }
    done
    return 1
}

require_line() {
    local label="$1" expression="$2" line
    if ! line="$(line_for "$expression")"; then
        echo "missing expected command: $label" >&2; read_calls; printf 'calls: %s\n' "${calls[*]:-<none>}" >&2; exit 1
    fi
    printf '%s\n' "$line"
}

assert_after() {
    local earlier_label="$1" earlier_line="$2" later_label="$3" later_line="$4"
    (( earlier_line < later_line )) || { echo "$later_label must run after $earlier_label" >&2; exit 1; }
}

assert_no_forbidden_operations() {
    read_calls
    local call
    for call in "${calls[@]}"; do
        if [[ "$call" == docker\ * ]] && [[ "$call" == *' compose down'* || "$call" == *' prune'* || "$call" == *' volume '* || "$call" == *' network rm'* || "$call" == *' image rm'* || "$call" == *mysql* || "$call" == *MySQL* ]]; then
            echo "deployment attempted forbidden Docker operation: $call" >&2; exit 1
        fi
    done
}

assert_successful_deploy() {
    local network="$1" fetch_line switch_line reset_line build_line candidate_line health_line remove_old_line rename_line
    if ! run_deploy 18000 "$network"; then echo 'deployment must succeed when candidate health succeeds' >&2; exit 1; fi
    fetch_line="$(require_line 'git fetch origin main' 'git fetch origin main')"
    switch_line="$(require_line 'git switch main' 'git switch main')"
    reset_line="$(require_line 'git reset --hard origin/main' 'git reset --hard origin/main')"
    build_line="$(require_line 'docker build' 'docker build --tag test-image .')"
    candidate_line="$(require_line 'candidate docker run' 'docker run -d --name existing-app-candidate-')"
    require_line 'candidate env file' "--env-file $env_file" >/dev/null
    require_line 'candidate loopback publish' '--publish 127.0.0.1:18000:8000' >/dev/null
    health_line="$(require_line 'loopback health check' 'curl -fsS http://127.0.0.1:18000/health')"
    remove_old_line="$(require_line 'old application removal' 'docker rm -f existing-app')"
    rename_line="$(require_line 'candidate rename' 'docker rename existing-app-candidate-')"
    if [[ -n "$network" ]]; then require_line 'network validation' "docker network inspect $network" >/dev/null; require_line 'candidate network' "--network $network" >/dev/null; fi
    assert_after fetch "$fetch_line" switch "$switch_line"; assert_after switch "$switch_line" reset "$reset_line"; assert_after reset "$reset_line" build "$build_line"; assert_after build "$build_line" candidate "$candidate_line"; assert_after candidate "$candidate_line" health "$health_line"; assert_after health "$health_line" removal "$remove_old_line"; assert_after removal "$remove_old_line" rename "$rename_line"
    assert_no_forbidden_operations
}

assert_successful_deploy ''
assert_successful_deploy 'application-network'

if MOCK_HEALTH_RESULT=failure run_deploy; then echo 'deployment must fail when candidate health fails' >&2; exit 1; fi
read_calls
for call in "${calls[@]}"; do
    [[ "$call" != 'docker rm -f existing-app' ]] || { echo 'deployment removed old application after candidate health failure' >&2; exit 1; }
done
require_line 'failed candidate cleanup' 'docker rm -f existing-app-candidate-' >/dev/null
assert_no_forbidden_operations

for invalid_port in 0 65536 invalid; do
    if run_deploy "$invalid_port"; then echo "deployment accepted invalid port: $invalid_port" >&2; exit 1; fi
    [[ -z "$(line_for 'docker build' || true)" ]] || { echo "deployment built image with invalid port: $invalid_port" >&2; exit 1; }
done

if MOCK_GIT_DIRTY=1 run_deploy; then echo 'deployment accepted a dirty tracked worktree' >&2; exit 1; fi
[[ -z "$(line_for 'git fetch origin main' || true)" ]] || { echo 'deployment fetched after detecting a dirty tracked worktree' >&2; exit 1; }
