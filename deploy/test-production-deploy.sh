#!/usr/bin/env bash
# Behavioural regression checks for the production deployment safety contract.
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
script_file="$script_dir/production-deploy.sh"
repo_dir="$(cd -- "$script_dir/.." && pwd)"
test -f "$script_file"
test ! -e "$repo_dir/.env" || { echo 'test must not create the repository .env' >&2; exit 1; }
! grep -Eq 'docker[[:space:]].*compose[[:space:]]+down|docker[[:space:]].*prune|docker[[:space:]]+volume[[:space:]]+rm|docker[[:space:]]+network[[:space:]]+rm|docker[[:space:]]+image[[:space:]]+rm|(^|[[:space:]])mysql([[:space:]]|$)' "$script_file" || { echo 'deployment script contains a forbidden operation' >&2; exit 1; }

fixture_dir="$(mktemp -d "${TMPDIR:-/tmp}/production-deploy-test.XXXXXX")"
mock_dir="$fixture_dir/bin"; env_file="$fixture_dir/production.env"; command_log="$fixture_dir/commands.nul"
mkdir -p "$mock_dir"; printf 'DATABASE_URL=mysql://test:secret@db/test\n' >"$env_file"
cleanup() { rm -rf -- "$fixture_dir"; }; trap cleanup EXIT

write_mock() {
    local command_name="$1"
    {
        printf '%s\n' '#!/usr/bin/env bash' 'set -Eeuo pipefail'
        printf '%s\n' 'printf "%s\\0" "'"$command_name"'" "$@" "__END__" >>"$COMMAND_LOG"'
        printf '%s\n' 'case "'"$command_name"'" in'
        printf '%s\n' '  git) case "${1:-}" in rev-parse) printf "%s\\n" "${MOCK_WORKTREE:-true}" ;; diff) exit "${MOCK_GIT_DIRTY:-0}" ;; esac ;;'
        printf '%s\n' '  docker) case "${1:-}" in container) [[ "${2:-}" != inspect ]] || [[ "${MOCK_OLD_CONTAINER:-present}" == present ]] ;; run) [[ "${MOCK_RUN_RESULT:-success}" == success ]] || exit 71; printf "new-container-id\\n" ;; esac ;;'
        printf '%s\n' '  curl) [[ "${MOCK_HEALTH_RESULT:-success}" == success ]] ;;' 'esac'
    } >"$mock_dir/$command_name"
    chmod +x "$mock_dir/$command_name"
}
write_mock git; write_mock docker; write_mock curl; write_mock sleep

read_calls() {
    calls=(); local record current=''
    while IFS= read -r -d '' record; do
        if [[ "$record" == '__END__' ]]; then calls+=("$current"); current=''; else current+="${current:+ }$record"; fi
    done <"$command_log"
}
run_deploy() {
    local port="${1:-18000}" network="${2:-}"
    : >"$command_log"
    if [[ -n "$network" ]]; then
        PATH="$mock_dir:$PATH" COMMAND_LOG="$command_log" USE_TEST_ENV_FILE=1 ENV_FILE="$env_file" APP_NAME=existing-app IMAGE_NAME=test-image HOST_PORT="$port" APP_DOCKER_NETWORK="$network" "$script_file"
    else
        PATH="$mock_dir:$PATH" COMMAND_LOG="$command_log" USE_TEST_ENV_FILE=1 ENV_FILE="$env_file" APP_NAME=existing-app IMAGE_NAME=test-image HOST_PORT="$port" "$script_file"
    fi
}
line_for() { local expression="$1" index; read_calls; for index in "${!calls[@]}"; do [[ "${calls[$index]}" == *"$expression"* ]] && { printf '%s\n' "$((index + 1))"; return; }; done; return 1; }
line_for_last() { local expression="$1" index line=''; read_calls; for index in "${!calls[@]}"; do [[ "${calls[$index]}" == *"$expression"* ]] && line="$((index + 1))"; done; [[ -n "$line" ]] && printf '%s\n' "$line"; }
require_line() { local label="$1" expression="$2" line; if ! line="$(line_for "$expression")"; then echo "missing expected command: $label" >&2; read_calls; printf 'calls: %s\n' "${calls[*]:-<none>}" >&2; exit 1; fi; printf '%s\n' "$line"; }
assert_after() { (( $2 < $4 )) || { echo "$3 must run after $1" >&2; exit 1; }; }
assert_no_forbidden_operations() {
    read_calls; local call
    for call in "${calls[@]}"; do
        if [[ "$call" == docker\ * ]] && [[ "$call" == *' compose down'* || "$call" == *' prune'* || "$call" == *' volume '* || "$call" == *' network rm'* || "$call" == *' image rm'* || "$call" == *mysql* || "$call" == *MySQL* ]]; then echo "deployment attempted forbidden operation: $call" >&2; exit 1; fi
    done
}
assert_successful_deploy() {
    local network="$1" fetch switch reset build stop rename run health remove
    run_deploy 18000 "$network"
    fetch="$(require_line fetch 'git fetch origin main')"; switch="$(require_line switch 'git switch main')"; reset="$(require_line reset 'git reset --hard origin/main')"; build="$(require_line build 'docker build --tag test-image .')"
    stop="$(require_line stop 'docker stop existing-app')"; rename="$(require_line rename 'docker rename existing-app existing-app-rollback-')"; run="$(require_line run 'docker run -d --name existing-app')"
    require_line env "--env-file $env_file" >/dev/null; require_line loopback '--publish 127.0.0.1:18000:8000' >/dev/null
    health="$(require_line health 'curl -fsS http://127.0.0.1:18000/health')"; remove="$(require_line remove 'docker rm existing-app-rollback-')"
    if [[ -n "$network" ]]; then require_line network-check "docker network inspect $network" >/dev/null; require_line network "--network $network" >/dev/null; fi
    assert_after fetch "$fetch" switch "$switch"; assert_after switch "$switch" reset "$reset"; assert_after reset "$reset" build "$build"; assert_after build "$build" stop "$stop"; assert_after stop "$stop" rename "$rename"; assert_after rename "$rename" run "$run"; assert_after run "$run" health "$health"; assert_after health "$health" remove "$remove"; assert_no_forbidden_operations
}
assert_rollback_after_failure() {
    local label="$1" run_result="$2" health_result="$3" stop rename run health remove restore start
    if MOCK_RUN_RESULT="$run_result" MOCK_HEALTH_RESULT="$health_result" run_deploy; then echo "deployment must fail when $label fails" >&2; exit 1; fi
    stop="$(require_line stop 'docker stop existing-app')"; rename="$(require_line rename 'docker rename existing-app existing-app-rollback-')"; run="$(require_line run 'docker run -d --name existing-app')"; health="$(line_for 'curl -fsS http://127.0.0.1:18000/health' || true)"
    remove="$(require_line remove-new 'docker rm -f existing-app')"; restore="$(line_for_last 'docker rename existing-app-rollback-')"; [[ -n "$restore" ]] || { echo 'missing expected command: restore-name' >&2; exit 1; }; start="$(require_line start-old 'docker start existing-app')"
    assert_after stop "$stop" rename "$rename"; assert_after rename "$rename" run "$run"
    if [[ "$label" == health ]]; then [[ -n "$health" ]] || { echo 'missing health check before rollback' >&2; exit 1; }; assert_after run "$run" health "$health"; assert_after health "$health" remove-new "$remove"; else [[ -z "$health" ]] || { echo 'health check ran after docker run failure' >&2; exit 1; }; assert_after run "$run" remove-new "$remove"; fi
    assert_after remove-new "$remove" restore-name "$restore"; assert_after restore-name "$restore" start-old "$start"; assert_no_forbidden_operations
}

assert_successful_deploy ''; assert_successful_deploy 'application-network'
assert_rollback_after_failure run failure success; assert_rollback_after_failure health success failure
if MOCK_OLD_CONTAINER=absent run_deploy; then :; else echo 'deployment without prior application must succeed' >&2; exit 1; fi
read_calls; [[ -z "$(line_for 'docker stop existing-app' || true)" ]] || { echo 'deployment stopped a non-existent application' >&2; exit 1; }; require_line run-without-old 'docker run -d --name existing-app' >/dev/null; assert_no_forbidden_operations
for invalid_port in 0 65536 invalid; do if run_deploy "$invalid_port"; then echo "deployment accepted invalid port: $invalid_port" >&2; exit 1; fi; [[ -z "$(line_for 'docker build' || true)" ]] || { echo "deployment built image with invalid port: $invalid_port" >&2; exit 1; }; done
if MOCK_GIT_DIRTY=1 run_deploy; then echo 'deployment accepted a dirty tracked worktree' >&2; exit 1; fi
[[ -z "$(line_for 'git fetch origin main' || true)" ]] || { echo 'deployment fetched after detecting a dirty tracked worktree' >&2; exit 1; }
if MOCK_WORKTREE=false run_deploy; then echo 'deployment accepted a non-worktree directory' >&2; exit 1; fi
[[ -z "$(line_for 'git fetch origin main' || true)" ]] || { echo 'deployment fetched outside a Git worktree' >&2; exit 1; }
if PATH="$mock_dir:$PATH" COMMAND_LOG="$command_log" ENV_FILE="$env_file" APP_NAME=existing-app IMAGE_NAME=test-image HOST_PORT=18000 "$script_file"; then
    echo 'production deployment accepted ENV_FILE override without explicit test mode' >&2; exit 1
fi
test ! -e "$repo_dir/.env" || { echo 'test created repository .env' >&2; exit 1; }
